package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// BillService orchestrates a utility-bill purchase end to end:
//
//	quote (₦ → MCGP at PremiumSell) → operator debit (BillPaymentService)
//	→ ClubKonnect vend → settle on delivery / reverse on terminal failure
//	→ reconcile everything that didn't resolve inline.
//
// It owns the ORDER; the money leg belongs to BillPaymentService and the vendor
// call to ClubkonnectClient — both behind narrow interfaces, so this service is
// unit-testable with fakes and the provider can be swapped again without
// touching money movement (the lesson Nomba taught).
var (
	ErrBillsDisabled    = errors.New("bill payments not configured")
	ErrBillNotFound     = errors.New("bill order not found")
	ErrBillRateStale    = errors.New("exchange rate unavailable; try again shortly")
	ErrBillQuoteExpired = errors.New("quote expired; request a new one")
	ErrBillBadState     = errors.New("bill order is not awaiting payment")
	ErrBillBadCategory  = errors.New("unknown bill category")
	ErrBillBadRequest   = errors.New("invalid bill request")
	// ErrBillPaymentConflict: the vend's known outcome contradicts the admin
	// action (e.g. reversing a delivered bill).
	ErrBillPaymentConflict = errors.New("the bill's vend state does not allow this payment action")
	// ErrBillVendUnverified: the vendor could not be asked what happened.
	ErrBillVendUnverified = errors.New("could not confirm the vend outcome with the provider")
	// ErrBillDebitUnverified: the swap contract could not be read to confirm
	// the payment's debit.
	ErrBillDebitUnverified = errors.New("could not confirm the payment's debit on-chain")
	// errBillOrderChanged: a guarded order transition found the order already
	// moved by someone else; the caller must not act on its stale copy.
	errBillOrderChanged = errors.New("bill order changed concurrently")
)

// billAdminInFlightGrace is how long after its last update a paid/vending
// order is assumed to possibly still have a vend in flight.
const billAdminInFlightGrace = 5 * time.Minute

// billScanLimit / billLateScanLimit are the reconciler page sizes.
const (
	billScanLimit     = 100
	billLateScanLimit = 50
	billDeepScanEvery = 24 * time.Hour
)

// billLateDebitScanWindow bounds how far back failed payments are re-checked
// for a debit that mined after they were given up on.
const billLateDebitScanWindow = 48 * time.Hour

// billEscalateAfter is how long an order may sit non-terminal before the
// reconciler starts shouting for a human. ClubKonnect's own retry window is
// 1 hour, so anything older is genuinely stuck.
const billEscalateAfter = 24 * time.Hour

// billVendingGrace is how long the reconciler leaves a vending order alone
// after the vendor last answered about it.
const billVendingGrace = 30 * time.Second

// billVendor is the ClubKonnect surface the service needs.
// *ClubkonnectClient satisfies it.
type billVendor interface {
	VendAirtime(ctx context.Context, network, phone string, amountNGN int64, requestID string) (BillVendResult, error)
	VendData(ctx context.Context, network string, plan BillPlan, phone, requestID string) (BillVendResult, error)
	VendElectricity(ctx context.Context, disco, meterNo, meterType string, amountNGN int64, requestID string) (BillVendResult, error)
	VendCableTV(ctx context.Context, cableTV, packageCode, smartCardNo, requestID string) (BillVendResult, error)
	VendBetting(ctx context.Context, company, customerID string, amountNGN int64, requestID string) (BillVendResult, error)
	VendEpin(ctx context.Context, provider, examType, phone, requestID string) (BillVendResult, error)
	QueryOrder(ctx context.Context, requestID string) (BillVendResult, error)
	VerifyElectricity(ctx context.Context, disco, meterNo string) (string, error)
	VerifyCableTV(ctx context.Context, cableTV, smartCardNo string) (string, error)
	AirtimeNetworks(ctx context.Context) ([]BillProvider, error)
	DataNetworks(ctx context.Context) ([]BillProvider, error)
	DataPlans(ctx context.Context, network string) ([]BillPlan, error)
	Discos(ctx context.Context) ([]BillProvider, error)
	CableProviders(ctx context.Context) ([]BillProvider, error)
	CablePackages(ctx context.Context, provider string) ([]BillPlan, error)
	BettingCompanies(ctx context.Context) ([]BillProvider, error)
	EpinPlans(ctx context.Context, provider string) ([]BillPlan, error)
}

// billPayer is the MCGP money leg. *BillPaymentService satisfies it.
type billPayer interface {
	IsEnabled() bool
	Debit(ctx context.Context, in DebitInput) (*models.BillPayment, error)
	Settle(ctx context.Context, orderID uuid.UUID) (*models.BillPayment, error)
	Reverse(ctx context.Context, orderID uuid.UUID) (*models.BillPayment, error)
	ResolveDebiting(ctx context.Context, orderID uuid.UUID) (string, bool, error)
	AdoptLateDebit(ctx context.Context, orderID uuid.UUID) (bool, error)
	StuckPayments(ctx context.Context, olderThan time.Duration, limit int) ([]models.BillPayment, error)
	AlertLateDebitsBeyondWindow(ctx context.Context) (int, error)
}

// billPriceSource quotes USDC (6dp) for an MCGP amount at a PriceKind.
// *MCGPSwapService satisfies it.
type billPriceSource interface {
	Quote(kind uint8, mcgpAmount *big.Int) (*big.Int, error)
}

type BillService struct {
	db            *gorm.DB
	vendor        billVendor
	payer         billPayer
	price         billPriceSource
	fx            FxProvider
	dataMarkupBps int
	quoteTTL      time.Duration
	// disputes stores user complaints. Optional at construction so existing
	// call sites compile unchanged; without it reporting is dormant rather
	// than writing somewhere nothing reads.
	disputes *DisputeService

	// scan pages the reconciler's scans; the daily late-debit audit claims its
	// run in the database (claimDailyRun).
	scan reconcileCursor
}

// SetDisputes wires the shared dispute queue in. Called at boot.
func (s *BillService) SetDisputes(d *DisputeService) {
	if s != nil {
		s.disputes = d
	}
}

func NewBillService(db *gorm.DB, vendor billVendor, payer billPayer, price billPriceSource, fx FxProvider, dataMarkupBps int, quoteTTL time.Duration) *BillService {
	if quoteTTL <= 0 {
		quoteTTL = 5 * time.Minute
	}
	return &BillService{
		db: db, vendor: vendor, payer: payer, price: price, fx: fx,
		dataMarkupBps: dataMarkupBps, quoteTTL: quoteTTL,
	}
}

func (s *BillService) IsEnabled() bool {
	return s != nil && s.db != nil && s.vendor != nil && s.payer != nil &&
		s.payer.IsEnabled() && s.price != nil && s.fx != nil
}

// ─── rate ────────────────────────────────────────────────────────────────────

// billRateKobo returns kobo per 1 MCGP, valuing MCGP at Contract B's
// PremiumSell price (the platform's cost basis — the conservative side of the
// spread; PM decision 2026-08-03). Rounds DOWN so the user's MCGP is never
// over-valued.
func (s *BillService) billRateKobo(ctx context.Context) (*big.Int, error) {
	usdcPerMcgp, err := s.price.Quote(PriceKindPremiumSell, oneE18)
	if err != nil || usdcPerMcgp == nil || usdcPerMcgp.Sign() <= 0 {
		return nil, ErrBillRateStale
	}
	ngnPerUsd, stale, err := s.fx.NgnPerUsd(ctx)
	if err != nil || stale || ngnPerUsd <= 0 {
		return nil, ErrBillRateStale
	}
	// kobo/MCGP = usdcMicros/1e6 × ngnPerUsd × 100, floored.
	rateRat := new(big.Rat).SetFloat64(ngnPerUsd)
	if rateRat == nil || rateRat.Sign() <= 0 {
		return nil, ErrBillRateStale
	}
	kobo := new(big.Rat).SetInt(usdcPerMcgp)
	kobo.Mul(kobo, rateRat)
	kobo.Mul(kobo, big.NewRat(100, 1))
	kobo.Quo(kobo, big.NewRat(1_000_000, 1))
	out := new(big.Int).Quo(kobo.Num(), kobo.Denom())
	if out.Sign() <= 0 {
		return nil, ErrBillRateStale
	}
	return out, nil
}

// mcgpForNgnAmount converts whole ₦ to MCGP base units at rateKobo, rounding UP
// so a vend is never under-covered.
func mcgpForNgnAmount(ngn int64, rateKobo *big.Int) *big.Int {
	ngnKobo := new(big.Int).Mul(big.NewInt(ngn), big.NewInt(100))
	num := new(big.Int).Mul(ngnKobo, oneE18)
	q, m := new(big.Int).QuoRem(num, rateKobo, new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// ─── quote ───────────────────────────────────────────────────────────────────

type BillQuoteInput struct {
	UserID       uuid.UUID
	Category     string
	Provider     string
	CustomerID   string
	CustomerName string
	PhoneNumber  string
	PlanRef      string
	MeterType    string
	NgnAmount    int64 // user-priced categories only; plan-priced ones are server-priced
}

type BillQuoteResult struct {
	Order      *models.BillOrder `json:"order"`
	McgpAmount string            `json:"mcgpAmount"`
	RateKobo   string            `json:"rateKobo"`
	NgnAmount  int64             `json:"ngnAmount"`
	ExpiresAt  time.Time         `json:"expiresAt"`
}

// Quote validates the request against the live catalog, prices it in MCGP, and
// stages a quoted order. All amounts are re-derived server-side — a client can
// never name its own price for a plan-priced product.
func (s *BillService) Quote(ctx context.Context, in BillQuoteInput) (*BillQuoteResult, error) {
	if !s.IsEnabled() {
		return nil, ErrBillsDisabled
	}
	order := &models.BillOrder{
		UserID:       in.UserID,
		Category:     in.Category,
		Provider:     strings.TrimSpace(in.Provider),
		CustomerID:   strings.TrimSpace(in.CustomerID),
		CustomerName: strings.TrimSpace(in.CustomerName),
		PhoneNumber:  strings.TrimSpace(in.PhoneNumber),
		PlanRef:      strings.TrimSpace(in.PlanRef),
		MeterType:    normalizeMeterType(in.MeterType),
		NgnAmount:    in.NgnAmount,
	}
	if err := s.resolveProduct(ctx, order); err != nil {
		return nil, err
	}
	// Cheap abuse valve: quotes are free to request but each one is a DB row.
	// Nobody legitimately holds this many live quotes at once.
	var open int64
	if err := s.db.WithContext(ctx).Model(&models.BillOrder{}).
		Where("user_id = ? AND status = ? AND quote_expires_at > ?",
			in.UserID, models.BillStatusQuoted, time.Now()).
		Count(&open).Error; err == nil && open >= 20 {
		return nil, fmt.Errorf("%w: too many open quotes; pay or let one expire", ErrBillBadRequest)
	}
	rate, err := s.billRateKobo(ctx)
	if err != nil {
		return nil, err
	}
	mcgp := mcgpForNgnAmount(order.NgnAmount, rate)
	order.McgpAmount = mcgp.String()
	order.RateKobo = rate.String()
	order.Status = models.BillStatusQuoted
	order.QuoteExpiresAt = time.Now().Add(s.quoteTTL)
	if err := s.db.WithContext(ctx).Create(order).Error; err != nil {
		return nil, fmt.Errorf("persist bill order: %w", err)
	}
	return &BillQuoteResult{
		Order:      order,
		McgpAmount: order.McgpAmount,
		RateKobo:   order.RateKobo,
		NgnAmount:  order.NgnAmount,
		ExpiresAt:  order.QuoteExpiresAt,
	}, nil
}

// resolveProduct validates provider/plan/amount against the catalog and fills
// server-derived fields (prices, display names, vend amount).
func (s *BillService) resolveProduct(ctx context.Context, o *models.BillOrder) error {
	switch o.Category {
	case models.BillCategoryAirtime:
		if !validNigerianPhone(o.PhoneNumber) {
			return fmt.Errorf("%w: valid phone number required", ErrBillBadRequest)
		}
		p, err := findProvider(s.vendor.AirtimeNetworks)(ctx, o.Provider)
		if err != nil {
			return err
		}
		o.ProviderName = p.Name
		o.VendNgnAmount = o.NgnAmount
		return checkBounds(o.NgnAmount, p)

	case models.BillCategoryData:
		if !validNigerianPhone(o.PhoneNumber) {
			return fmt.Errorf("%w: valid phone number required", ErrBillBadRequest)
		}
		p, err := findProvider(s.vendor.DataNetworks)(ctx, o.Provider)
		if err != nil {
			return err
		}
		plan, err := s.findPlan(ctx, s.vendor.DataPlans, o.Provider, o.PlanRef)
		if err != nil {
			return err
		}
		o.ProviderName = p.Name
		o.PlanName = plan.Name
		// Retail = ClubKonnect cost + markup, rounded up to whole ₦.
		markup := int64(10000 + s.dataMarkupBps)
		o.NgnAmount = (plan.CostNGN*markup + 9999) / 10000
		o.VendNgnAmount = plan.CostNGN
		return nil

	case models.BillCategoryElectricity:
		if o.CustomerID == "" {
			return fmt.Errorf("%w: meter number required", ErrBillBadRequest)
		}
		if o.MeterType != "01" && o.MeterType != "02" {
			return fmt.Errorf("%w: meter type must be prepaid or postpaid", ErrBillBadRequest)
		}
		p, err := findProvider(s.vendor.Discos)(ctx, o.Provider)
		if err != nil {
			return err
		}
		o.ProviderName = p.Name
		o.VendNgnAmount = o.NgnAmount
		return checkBounds(o.NgnAmount, p)

	case models.BillCategoryCableTV:
		if o.CustomerID == "" {
			return fmt.Errorf("%w: smartcard number required", ErrBillBadRequest)
		}
		p, err := findProvider(s.vendor.CableProviders)(ctx, o.Provider)
		if err != nil {
			return err
		}
		plan, err := s.findPlan(ctx, s.vendor.CablePackages, o.Provider, o.PlanRef)
		if err != nil {
			return err
		}
		o.ProviderName = p.Name
		o.PlanName = plan.Name
		o.NgnAmount = plan.AmountNGN // face value
		o.VendNgnAmount = plan.AmountNGN
		return nil

	case models.BillCategoryBetting:
		if o.CustomerID == "" {
			return fmt.Errorf("%w: betting account id required", ErrBillBadRequest)
		}
		p, err := findProvider(s.vendor.BettingCompanies)(ctx, o.Provider)
		if err != nil {
			return err
		}
		o.ProviderName = p.Name
		o.VendNgnAmount = o.NgnAmount
		return checkBounds(o.NgnAmount, p)

	case models.BillCategoryEpin:
		if !validNigerianPhone(o.PhoneNumber) {
			return fmt.Errorf("%w: valid phone number required", ErrBillBadRequest)
		}
		prov := strings.ToLower(o.Provider)
		if prov != "waec" && prov != "jamb" {
			return fmt.Errorf("%w: e-PIN provider must be waec or jamb", ErrBillBadRequest)
		}
		plan, err := s.findPlan(ctx, s.vendor.EpinPlans, prov, o.PlanRef)
		if err != nil {
			return err
		}
		o.Provider = prov
		o.ProviderName = strings.ToUpper(prov)
		o.PlanName = plan.Name
		o.NgnAmount = plan.AmountNGN // face value
		o.VendNgnAmount = plan.AmountNGN
		return nil

	default:
		return ErrBillBadCategory
	}
}

func findProvider(list func(context.Context) ([]BillProvider, error)) func(context.Context, string) (*BillProvider, error) {
	return func(ctx context.Context, code string) (*BillProvider, error) {
		providers, err := list(ctx)
		if err != nil {
			return nil, fmt.Errorf("catalog unavailable: %w", err)
		}
		for i := range providers {
			if providers[i].Code == code {
				return &providers[i], nil
			}
		}
		return nil, fmt.Errorf("%w: unknown provider %q", ErrBillBadRequest, code)
	}
}

func (s *BillService) findPlan(ctx context.Context, list func(context.Context, string) ([]BillPlan, error), provider, code string) (*BillPlan, error) {
	if code == "" {
		return nil, fmt.Errorf("%w: plan required", ErrBillBadRequest)
	}
	plans, err := list(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("catalog unavailable: %w", err)
	}
	for i := range plans {
		if plans[i].Code == code {
			return &plans[i], nil
		}
	}
	return nil, fmt.Errorf("%w: unknown plan %q", ErrBillBadRequest, code)
}

func checkBounds(amount int64, p *BillProvider) error {
	if amount <= 0 {
		return fmt.Errorf("%w: amount must be positive", ErrBillBadRequest)
	}
	if p.MinAmount > 0 && amount < p.MinAmount {
		return fmt.Errorf("%w: minimum is ₦%d", ErrBillBadRequest, p.MinAmount)
	}
	if p.MaxAmount > 0 && amount > p.MaxAmount {
		return fmt.Errorf("%w: maximum is ₦%d", ErrBillBadRequest, p.MaxAmount)
	}
	return nil
}

func normalizeMeterType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "prepaid", "01":
		return "01"
	case "postpaid", "02":
		return "02"
	default:
		return strings.TrimSpace(t)
	}
}

// ─── pay → vend ──────────────────────────────────────────────────────────────

// Pay drives the money leg then the vend. The debit is idempotent per order
// (on-chain ref derived from the order ID), so a retried Pay cannot
// double-charge; a repeat call on an already-progressed order just returns it.
func (s *BillService) Pay(ctx context.Context, userID uuid.UUID, walletAddress string, orderID uuid.UUID) (*models.BillOrder, error) {
	if !s.IsEnabled() {
		return nil, ErrBillsDisabled
	}
	var order models.BillOrder
	if err := s.db.WithContext(ctx).First(&order, "id = ? AND user_id = ?", orderID, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillNotFound
		}
		return nil, err
	}
	switch order.Status {
	case models.BillStatusQuoted:
		// proceed
	case models.BillStatusPaying, models.BillStatusPaid, models.BillStatusVending,
		models.BillStatusDelivered, models.BillStatusVendFail, models.BillStatusRefunded:
		return &order, nil // idempotent replay
	default:
		return nil, ErrBillBadState
	}
	if order.IsExpired(time.Now()) {
		s.moveOrder(ctx, &order, []string{models.BillStatusQuoted}, map[string]any{"status": models.BillStatusExpired})
		return nil, ErrBillQuoteExpired
	}

	mcgp, ok := new(big.Int).SetString(order.McgpAmount, 10)
	if !ok || mcgp.Sign() <= 0 {
		return nil, fmt.Errorf("corrupt quote amount on order %s", order.ID)
	}
	rate, _ := new(big.Int).SetString(order.RateKobo, 10)

	// Atomic quoted→paying transition. Two concurrent Pay calls both read
	// `quoted` above; without this CAS both would debit-and-vend, and the vend
	// has no on-chain/DB idempotency backstop (only ClubKonnect's RequestID
	// dedupe, which we cannot verify). Exactly one caller wins the row. The
	// expiry re-check lives INSIDE the predicate: the IsExpired() check above
	// used the earlier read, and a quote that lapsed in between must not lock
	// in its stale ₦/MCGP rate.
	res := s.db.WithContext(ctx).Model(&models.BillOrder{}).
		Where("id = ? AND status = ? AND quote_expires_at > ?",
			order.ID, models.BillStatusQuoted, time.Now()).
		Updates(map[string]any{"status": models.BillStatusPaying, "claim_seq": gorm.Expr("claim_seq + 1")})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		// Lost the race (another caller owns the flow) or expired under us.
		if err := s.db.WithContext(ctx).First(&order, "id = ?", order.ID).Error; err != nil {
			return nil, err
		}
		if order.IsExpired(time.Now()) {
			s.moveOrder(ctx, &order, []string{models.BillStatusQuoted}, map[string]any{"status": models.BillStatusExpired})
			return nil, ErrBillQuoteExpired
		}
		return &order, nil
	}
	order.Status = models.BillStatusPaying

	_, err := s.payer.Debit(ctx, DebitInput{
		UserID:        userID,
		OrderID:       order.ID,
		WalletAddress: walletAddress,
		McgpAmount:    mcgp,
		NgnKobo:       new(big.Int).Mul(big.NewInt(order.NgnAmount), big.NewInt(100)),
		RateKobo:      rate,
		// Bills may consume self-exitable (bought) premium — paying bills with
		// MCGP you bought is the point of the feature.
		AllowBought: mcgp,
	})
	if err != nil {
		// What happens to the order depends on how far the debit got:
		//   no payment row   → nothing was broadcast (balance/validation
		//                      failure) — safe to reopen the quote for a retry.
		//   row `debiting`   → broadcast but unverified; may be live on-chain —
		//                      hold for the reconciler.
		//   row `failed`     → a broadcast attempt failed. TERMINAL for this
		//                      order: the on-chain ref is order-derived, so
		//                      replaying this order could only ever surface the
		//                      failed row again — a retry needs a fresh order.
		//   anything else    → a concurrent Pay moved money; don't touch it.
		// Every order write is guarded on `paying`, so none of them can
		// overwrite an order the reconciler has already moved on.
		var pay models.BillPayment
		perr := s.db.WithContext(ctx).Where("order_id = ?", order.ID).First(&pay).Error
		paying := []string{models.BillStatusPaying}
		switch {
		case errors.Is(perr, gorm.ErrRecordNotFound):
			s.moveOrder(ctx, &order, paying, map[string]any{"status": models.BillStatusQuoted, "failure_reason": truncateReason(err.Error())})
			return nil, err
		case perr != nil:
			return nil, err
		case pay.Status == models.BillPayStatusDebiting:
			s.moveOrder(ctx, &order, paying, map[string]any{"failure_reason": truncateReason("debit unconfirmed: " + err.Error())})
			return nil, fmt.Errorf("payment pending confirmation; it will resolve shortly")
		case pay.Status == models.BillPayStatusFailed:
			s.moveOrder(ctx, &order, paying, map[string]any{"status": models.BillStatusFailed, "failure_reason": truncateReason(err.Error())})
			return nil, err
		default:
			// Debited (or beyond) by a concurrent caller — leave the flow to it
			// and the reconciler.
			return nil, fmt.Errorf("payment already in progress")
		}
	}

	if !s.moveOrder(ctx, &order, []string{models.BillStatusPaying}, map[string]any{"status": models.BillStatusPaid}) {
		// Something else moved the order while the debit ran; it owns the vend.
		if err := s.db.WithContext(ctx).First(&order, "id = ?", order.ID).Error; err != nil {
			return nil, err
		}
		return &order, nil
	}
	s.vend(ctx, &order)
	return &order, nil
}

// moveOrder applies updates only while the order is still in one of from,
// then refreshes o. Reports whether this call moved it. Every order write on
// the pay/vend/reconcile paths goes through here: a full-row Save from a stale
// copy could overwrite a concurrent delivery (and its token/PIN).
func (s *BillService) moveOrder(ctx context.Context, o *models.BillOrder, from []string, updates map[string]any) bool {
	if _, taken := updates["claim_seq"]; !taken {
		updates["claim_seq"] = gorm.Expr("claim_seq + 1")
	}
	res := s.db.WithContext(ctx).Model(&models.BillOrder{}).
		Where("id = ? AND status IN ?", o.ID, from).
		Updates(updates)
	if res.Error != nil {
		log.Printf("[bills] order=%s transition %v→%v failed: %v", o.ID, from, updates["status"], res.Error)
		return false
	}
	if res.RowsAffected != 1 {
		return false
	}
	if err := s.db.WithContext(ctx).First(o, "id = ?", o.ID).Error; err != nil {
		log.Printf("[bills] order=%s reload after transition failed: %v", o.ID, err)
	}
	return true
}

// claimOrder takes an order for work that must happen once (a re-vend), by
// writing the exact version the caller read. Exactly one concurrent caller
// wins; the rest see the bumped version and stand down.
func (s *BillService) claimOrder(ctx context.Context, o *models.BillOrder, status string) bool {
	res := s.db.WithContext(ctx).Model(&models.BillOrder{}).
		Where("id = ? AND status = ? AND claim_seq = ?", o.ID, status, o.ClaimSeq).
		Update("claim_seq", gorm.Expr("claim_seq + 1"))
	if res.Error != nil || res.RowsAffected != 1 {
		return false
	}
	o.ClaimSeq++
	return true
}

// vend calls ClubKonnect and resolves the order as far as the response allows.
// The caller must own the order (have moved it to `paid` itself). The order
// status carries the outcome, and an unknown outcome parks the order in
// `vending` for the reconciler (a transport error does NOT mean the vend
// didn't happen).
func (s *BillService) vend(ctx context.Context, o *models.BillOrder) {
	res, err := s.callVendor(ctx, o)
	if err != nil {
		s.moveOrder(ctx, o, []string{models.BillStatusPaid, models.BillStatusVending}, map[string]any{
			"status":         models.BillStatusVending,
			"failure_reason": truncateReason("vend outcome unknown: " + err.Error()),
		})
		return
	}
	if aerr := s.adoptVendResult(ctx, o, res); aerr != nil {
		log.Printf("[bills] order=%s vend result not fully applied: %v", o.ID, aerr)
	}
}

func (s *BillService) callVendor(ctx context.Context, o *models.BillOrder) (BillVendResult, error) {
	requestID := o.ID.String()
	switch o.Category {
	case models.BillCategoryAirtime:
		return s.vendor.VendAirtime(ctx, o.Provider, o.PhoneNumber, o.VendNgnAmount, requestID)
	case models.BillCategoryData:
		plan, err := s.findPlan(ctx, s.vendor.DataPlans, o.Provider, o.PlanRef)
		if err != nil {
			// Two very different failures share this branch. Plan confirmed
			// absent from a good catalog → terminal, nothing was sent. Catalog
			// fetch itself failed → transient; erroring parks the order in
			// `vending` for the reconciler instead of refunding a bill whose
			// plan is still perfectly vendable.
			if errors.Is(err, ErrBillBadRequest) {
				return BillVendResult{StatusCode: 400, Status: "ORDER_ERROR", Remark: "plan no longer available"}, nil
			}
			return BillVendResult{}, err
		}
		return s.vendor.VendData(ctx, o.Provider, *plan, o.PhoneNumber, requestID)
	case models.BillCategoryElectricity:
		return s.vendor.VendElectricity(ctx, o.Provider, o.CustomerID, o.MeterType, o.VendNgnAmount, requestID)
	case models.BillCategoryCableTV:
		return s.vendor.VendCableTV(ctx, o.Provider, o.PlanRef, o.CustomerID, requestID)
	case models.BillCategoryBetting:
		return s.vendor.VendBetting(ctx, o.Provider, o.CustomerID, o.VendNgnAmount, requestID)
	case models.BillCategoryEpin:
		return s.vendor.VendEpin(ctx, o.Provider, o.PlanRef, o.PhoneNumber, requestID)
	default:
		return BillVendResult{}, ErrBillBadCategory
	}
}

// vendAnswerChangesOrder reports whether a vendor answer carries anything the
// order does not already record (the same fields adoptVendResult writes).
func vendAnswerChangesOrder(o *models.BillOrder, res BillVendResult) bool {
	return (res.OrderID != "" && res.OrderID != o.VendorOrderID) ||
		(res.StatusCode != 0 && res.StatusCode != o.VendorStatusCode) ||
		(res.Delivery != "" && truncate512(res.Delivery) != o.DeliveryPayload)
}

// adoptVendResult moves an order (and its payment) to wherever a vend/query
// response says it is. Shared by the inline vend, the reconciler and the admin
// actions. Every order write is guarded on the order still being paid/vending,
// so a stale caller can neither overwrite a delivered bill nor reverse it;
// that case returns errBillOrderChanged. A settle or reverse that fails is
// returned (the MCGP stays in pendingSettle and the reconciler retries).
func (s *BillService) adoptVendResult(ctx context.Context, o *models.BillOrder, res BillVendResult) error {
	open := []string{models.BillStatusPaid, models.BillStatusVending}
	updates := map[string]any{}
	if res.OrderID != "" {
		updates["vendor_order_id"] = res.OrderID
	}
	if res.StatusCode != 0 {
		updates["vendor_status_code"] = res.StatusCode
	}
	if res.Delivery != "" {
		updates["delivery_payload"] = truncate512(res.Delivery)
	}
	decision, floatLow := decideVend(res.StatusCode)
	if floatLow {
		// Our prepaid float ran dry mid-purchase. Page-level: bills are down for
		// everyone until treasury tops up.
		log.Printf("[bills] ALERT bill_float_exhausted_total=1 order=%s code=%d", o.ID, res.StatusCode)
	}
	switch decision {
	case VendSuccess:
		updates["status"] = models.BillStatusDelivered
		updates["failure_reason"] = ""
		// The delivery (meter token / PIN) must be on disk BEFORE the debit is
		// settled: settling is irreversible, and settling a delivery we failed
		// to record charges the user for a product they cannot retrieve.
		if !s.moveOrder(ctx, o, open, updates) {
			return errBillOrderChanged
		}
		if _, err := s.payer.Settle(ctx, o.ID); err != nil {
			if errors.Is(err, ErrBillPayBadState) || errors.Is(err, ErrBillPayNotFound) {
				// A delivered bill whose payment cannot settle means a vend
				// happened without a matching confirmed debit — the signature
				// of a free vend. Page-level, never routine.
				log.Printf("[bills] ALERT bill_settle_impossible_total=1 order=%s: %v", o.ID, err)
			} else {
				// Transient — MCGP is safe in pendingSettle; the reconciler
				// sweep retries the settle.
				log.Printf("[bills] settle deferred order=%s: %v", o.ID, err)
			}
			return fmt.Errorf("bill delivered but settle failed: %w", err)
		}
		return nil
	case VendFailed:
		reason := res.Remark
		if reason == "" {
			reason = fmt.Sprintf("vend failed (code %d %s)", res.StatusCode, res.Status)
		}
		if floatLow {
			reason = "service temporarily unavailable" // never blame the user for our float
		}
		updates["status"] = models.BillStatusVendFail
		updates["failure_reason"] = truncateReason(reason)
		if !s.moveOrder(ctx, o, open, updates) {
			return errBillOrderChanged
		}
		if _, err := s.payer.Reverse(ctx, o.ID); err != nil {
			log.Printf("[bills] reverse deferred order=%s: %v", o.ID, err)
			return fmt.Errorf("vend failed but reverse failed: %w", err)
		}
		s.moveOrder(ctx, o, []string{models.BillStatusVendFail}, map[string]any{"status": models.BillStatusRefunded})
		return nil
	default: // pending
		if o.Status == models.BillStatusVending && !vendAnswerChangesOrder(o, res) {
			// Nothing new: writing would bump updated_at, and the reconciler
			// re-asks a vending order every tick, so the order would always look
			// "just changed" and the admin in-flight grace would refuse it forever.
			return nil
		}
		updates["status"] = models.BillStatusVending
		if !s.moveOrder(ctx, o, open, updates) {
			return errBillOrderChanged
		}
		return nil
	}
}

// ─── validate + catalogs ─────────────────────────────────────────────────────

// Validate resolves/validates the customer before payment. Electricity and
// cable do a live holder lookup; the rest are format checks.
func (s *BillService) Validate(ctx context.Context, category, provider, customerID string) (string, error) {
	if !s.IsEnabled() {
		return "", ErrBillsDisabled
	}
	customerID = strings.TrimSpace(customerID)
	switch category {
	case models.BillCategoryElectricity:
		if err := checkLookupInput(customerID); err != nil {
			return "", err
		}
		if _, err := findProvider(s.vendor.Discos)(ctx, provider); err != nil {
			return "", err
		}
		return s.vendor.VerifyElectricity(ctx, provider, customerID)
	case models.BillCategoryCableTV:
		if err := checkLookupInput(customerID); err != nil {
			return "", err
		}
		if _, err := findProvider(s.vendor.CableProviders)(ctx, provider); err != nil {
			return "", err
		}
		return s.vendor.VerifyCableTV(ctx, provider, customerID)
	case models.BillCategoryAirtime, models.BillCategoryData, models.BillCategoryEpin:
		if !validNigerianPhone(customerID) {
			return "", fmt.Errorf("%w: invalid phone number", ErrBillBadRequest)
		}
		return "", nil
	case models.BillCategoryBetting:
		if customerID == "" {
			return "", fmt.Errorf("%w: betting account id required", ErrBillBadRequest)
		}
		return "", nil
	default:
		return "", ErrBillBadCategory
	}
}

// Providers lists the operators for a category.
func (s *BillService) Providers(ctx context.Context, category string) ([]BillProvider, error) {
	if !s.IsEnabled() {
		return nil, ErrBillsDisabled
	}
	switch category {
	case models.BillCategoryAirtime:
		return s.vendor.AirtimeNetworks(ctx)
	case models.BillCategoryData:
		return s.vendor.DataNetworks(ctx)
	case models.BillCategoryElectricity:
		return s.vendor.Discos(ctx)
	case models.BillCategoryCableTV:
		return s.vendor.CableProviders(ctx)
	case models.BillCategoryBetting:
		return s.vendor.BettingCompanies(ctx)
	case models.BillCategoryEpin:
		return []BillProvider{{Code: "waec", Name: "WAEC"}, {Code: "jamb", Name: "JAMB"}}, nil
	default:
		return nil, ErrBillBadCategory
	}
}

// Plans lists fixed-price products for a provider within a category, at the
// price the USER pays (data plans get the retail markup here).
func (s *BillService) Plans(ctx context.Context, category, provider string) ([]BillPlan, error) {
	if !s.IsEnabled() {
		return nil, ErrBillsDisabled
	}
	switch category {
	case models.BillCategoryData:
		plans, err := s.vendor.DataPlans(ctx, provider)
		if err != nil {
			return nil, err
		}
		markup := int64(10000 + s.dataMarkupBps)
		out := make([]BillPlan, 0, len(plans))
		for _, p := range plans {
			p.AmountNGN = (p.CostNGN*markup + 9999) / 10000
			out = append(out, p)
		}
		return out, nil
	case models.BillCategoryCableTV:
		return s.vendor.CablePackages(ctx, provider)
	case models.BillCategoryEpin:
		return s.vendor.EpinPlans(ctx, provider)
	default:
		return nil, ErrBillBadCategory
	}
}

// ─── reconcile ───────────────────────────────────────────────────────────────

// Reconcile drives every non-terminal order toward a terminal state:
//
//	quoted   past TTL          → expired
//	paying   (stale)           → resolve from the payment row
//	paid     (stale, vend lost)→ query ClubKonnect, else re-vend (RequestID dedupes)
//	vending                    → query ClubKonnect, adopt the answer
//	delivered/vend_failed with an unresolved payment → retry settle/reverse
//
// Every branch is idempotent; grace keeps it from second-guessing in-flight
// requests. Returns how many orders changed state.
func (s *BillService) Reconcile(ctx context.Context, grace time.Duration) (int, error) {
	if !s.IsEnabled() {
		return 0, ErrBillsDisabled
	}
	changed := 0
	now := time.Now()
	cutoff := now.Add(-grace)

	// Expire dead quotes (cheap bulk update; no money involved).
	res := s.db.WithContext(ctx).Model(&models.BillOrder{}).
		Where("status = ? AND quote_expires_at < ?", models.BillStatusQuoted, cutoff).
		Updates(map[string]any{"status": models.BillStatusExpired, "claim_seq": gorm.Expr("claim_seq + 1")})
	changed += int(res.RowsAffected)

	// Scans are cursor-paged: orders the reconciler can only alert on never
	// change, and an ordered LIMIT would otherwise refill every page with them.
	// A vending order is only waiting on the vendor — the grace for paying/paid
	// guards a Pay call still in flight, which a vending order no longer has —
	// so it is asked again soon after the vendor last answered.
	vendingCutoff := now.Add(-min(grace, billVendingGrace))
	var orders []models.BillOrder
	if err := s.scan.page("orders", "", s.db.WithContext(ctx).Model(&models.BillOrder{}).
		Where("(status IN ? AND updated_at < ?) OR (status = ? AND updated_at < ?)",
			[]string{models.BillStatusPaying, models.BillStatusPaid}, cutoff,
			models.BillStatusVending, vendingCutoff),
		billScanLimit).Find(&orders).Error; err != nil {
		return changed, err
	}
	s.advanceOrders("orders", orders, billScanLimit)
	for i := range orders {
		if s.reconcileOrder(ctx, &orders[i]) {
			changed++
		}
	}

	// Invariant: every delivered order must have a payment that at least
	// reached `debited`. A delivered order whose payment failed or never
	// existed is a vend nobody paid for — the free-vend signature. Alert-only.
	var freeVends int64
	if err := s.db.WithContext(ctx).Model(&models.BillOrder{}).
		Joins("LEFT JOIN bill_payments ON bill_payments.order_id = bill_orders.id").
		Where("bill_orders.status = ? AND (bill_payments.id IS NULL OR bill_payments.status IN ?)",
			models.BillStatusDelivered,
			[]string{models.BillPayStatusFailed, models.BillPayStatusDebiting}).
		Count(&freeVends).Error; err == nil && freeVends > 0 {
		alertf("bills-free-vend",
			"[bills] ALERT bill_delivered_without_payment_total=%d — vend without a confirmed debit", freeVends)
	}

	// Payments given up on whose debit mined anyway. The order decides what
	// the MCGP was for: a delivered bill settles and one that never vended
	// (failed) is reversed, both by the sweep below. Anything else is left for
	// a human.
	var late []models.BillPayment
	if err := s.scan.page("late", "", s.db.WithContext(ctx).Model(&models.BillPayment{}).
		Where("purpose = ? AND status = ? AND updated_at > ?", models.BillPayPurposeBill, models.BillPayStatusFailed, now.Add(-billLateDebitScanWindow)),
		billLateScanLimit).Find(&late).Error; err == nil {
		if n := len(late); n > 0 {
			s.scan.advance("late", n, billLateScanLimit, late[n-1].UpdatedAt, late[n-1].ID)
		} else {
			s.scan.advance("late", 0, billLateScanLimit, time.Time{}, uuid.Nil)
		}
		for i := range late {
			adopted, err := s.payer.AdoptLateDebit(ctx, late[i].OrderID)
			if err != nil || !adopted {
				continue
			}
			changed++
			var o models.BillOrder
			if err := s.db.WithContext(ctx).First(&o, "id = ?", late[i].OrderID).Error; err == nil &&
				o.Status != models.BillStatusDelivered && o.Status != models.BillStatusFailed {
				log.Printf("[bills] ALERT bill_late_debit_needs_review order=%s status=%s", o.ID, o.Status)
			}
		}
	}

	// Orders terminal but whose payment leg didn't resolve (settle/reverse
	// deferred earlier, or a late debit just adopted). Retry quietly. A
	// `failed` order never reached the vend (Pay only vends once paid), so its
	// debit can only be returned.
	var unresolved []models.BillOrder
	if err := s.scan.page("unresolved", "bill_orders", s.db.WithContext(ctx).Model(&models.BillOrder{}).
		Select("bill_orders.*").
		Joins("JOIN bill_payments ON bill_payments.order_id = bill_orders.id").
		Where("bill_orders.status IN ? AND bill_payments.status = ?",
			[]string{models.BillStatusDelivered, models.BillStatusVendFail, models.BillStatusFailed}, models.BillPayStatusDebited),
		billLateScanLimit).Find(&unresolved).Error; err == nil {
		s.advanceOrders("unresolved", unresolved, billLateScanLimit)
		for i := range unresolved {
			o := &unresolved[i]
			switch o.Status {
			case models.BillStatusDelivered:
				if _, err := s.payer.Settle(ctx, o.ID); err == nil {
					changed++
				}
			case models.BillStatusFailed:
				if _, err := s.payer.Reverse(ctx, o.ID); err == nil {
					changed++
				}
			default:
				if _, err := s.payer.Reverse(ctx, o.ID); err == nil {
					s.moveOrder(ctx, o, []string{models.BillStatusVendFail}, map[string]any{"status": models.BillStatusRefunded})
					changed++
				}
			}
		}
	}

	// One claim a day, persisted (models.JobRunMarker) exactly as the sell-side
	// audit claims its own: a restart or a second instance must not repeat it.
	// A run that then fails leaves the day claimed — acceptable for an
	// alert-only audit, where the cost is a day's delay on a warning rather
	// than anything that moves money.
	if claimDailyRun(ctx, s.db, "bill-late-debit-audit", billDeepScanEvery) {
		if _, err := s.payer.AlertLateDebitsBeyondWindow(ctx); err != nil {
			log.Printf("[bills] late-debit audit failed: %v", err)
		}
	}
	return changed, nil
}

func (s *BillService) advanceOrders(key string, rows []models.BillOrder, limit int) {
	if n := len(rows); n > 0 {
		s.scan.advance(key, n, limit, rows[n-1].UpdatedAt, rows[n-1].ID)
		return
	}
	s.scan.advance(key, 0, limit, time.Time{}, uuid.Nil)
}

// reconcileOrder resolves one stale non-terminal order. Returns true if the
// order moved. o may be stale: every write is guarded on the status it read,
// and a vend only happens after this call itself moved the order to `paid`.
func (s *BillService) reconcileOrder(ctx context.Context, o *models.BillOrder) bool {
	before := o.Status
	switch o.Status {
	case models.BillStatusPaying:
		paying := []string{models.BillStatusPaying}
		// The debit's fate is in the payment row.
		var pay models.BillPayment
		err := s.db.WithContext(ctx).Where("order_id = ?", o.ID).First(&pay).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && pay.Status == models.BillPayStatusFailed):
			// No debit ever confirmed — the order is over.
			s.moveOrder(ctx, o, paying, map[string]any{"status": models.BillStatusFailed, "failure_reason": truncateReason("payment did not complete")})
		case err == nil && pay.Status == models.BillPayStatusDebited:
			if s.moveOrder(ctx, o, paying, map[string]any{"status": models.BillStatusPaid}) {
				s.vend(ctx, o)
			}
		case err == nil && pay.Status == models.BillPayStatusDebiting:
			// Broadcast (or attempted) but never verified. The contract's debit
			// record for the order's ref is the evidence: a landed debit carries
			// on to the vend, a debit that never landed fails the order. Only the
			// caller that won the payment transition acts; a loser leaves the
			// order to the winner (or to the `debited` branch next tick).
			status, won, rerr := s.payer.ResolveDebiting(ctx, o.ID)
			switch {
			case rerr == nil && won && status == models.BillPayStatusDebited:
				if s.moveOrder(ctx, o, paying, map[string]any{"status": models.BillStatusPaid}) {
					s.vend(ctx, o)
				}
			case rerr == nil && won && status == models.BillPayStatusFailed:
				s.moveOrder(ctx, o, paying, map[string]any{"status": models.BillStatusFailed, "failure_reason": truncateReason("payment did not complete")})
			default:
				if rerr != nil {
					log.Printf("[bills] order=%s debit check failed: %v", o.ID, rerr)
				}
				if time.Since(o.CreatedAt) > billEscalateAfter {
					alertf("bill-payment-unresolved:"+o.ID.String(),
						"[bills] ALERT bill_payment_unresolved_total=1 order=%s payment=%s", o.ID, pay.ID)
				}
			}
		}
	case models.BillStatusPaid:
		// Debit confirmed but the vend outcome was lost. Claim the order first
		// (a CAS on the exact row version read) so two passes cannot both
		// re-vend, then ask ClubKonnect; if it has never seen the RequestID, the
		// vend was never sent — send it.
		if !s.claimOrder(ctx, o, models.BillStatusPaid) {
			return false
		}
		res, err := s.vendor.QueryOrder(ctx, o.ID.String())
		if err != nil {
			s.vend(ctx, o)
		} else if aerr := s.adoptVendResult(ctx, o, res); aerr != nil {
			log.Printf("[bills] order=%s adopt query result: %v", o.ID, aerr)
		}
	case models.BillStatusVending:
		res, err := s.vendor.QueryOrder(ctx, o.ID.String())
		if err == nil {
			if aerr := s.adoptVendResult(ctx, o, res); aerr != nil {
				log.Printf("[bills] order=%s adopt query result: %v", o.ID, aerr)
			}
		}
		if o.Status == models.BillStatusVending && time.Since(o.CreatedAt) > billEscalateAfter {
			alertf("bill-vend-unresolved:"+o.ID.String(),
				"[bills] ALERT bill_vend_unresolved_total=1 order=%s vendor_order=%s", o.ID, o.VendorOrderID)
		}
	}
	return o.Status != before
}

// ─── admin payment resolution ────────────────────────────────────────────────

// queryConfirmsVendFailed reports whether a QueryOrder answer is positive
// evidence the vend failed: it must identify the order (the vendor order id,
// matching the one recorded if any) and carry a code from the ORDER_CANCELLED
// group. A 4xx on a query is about the query, not the vend, and proves nothing.
func queryConfirmsVendFailed(o *models.BillOrder, res BillVendResult) bool {
	if res.OrderID == "" || (o.VendorOrderID != "" && res.OrderID != o.VendorOrderID) {
		return false
	}
	return res.StatusCode >= 500 && res.StatusCode < 600
}

// queryConfirmsVendDelivered is the success counterpart: code 200 for this order.
func queryConfirmsVendDelivered(o *models.BillOrder, res BillVendResult) bool {
	if res.OrderID == "" || (o.VendorOrderID != "" && res.OrderID != o.VendorOrderID) {
		return false
	}
	return res.StatusCode == 200
}

// AdminReversePayment returns a bill's MCGP, but only on evidence the bill was
// not delivered: the vend failed (recorded, or reported terminal for this
// order by the vendor now), or the order never reached the vend. A delivered,
// successful or still-pending vend is refused — reversing it gives the user
// the bill for free — and so is an order touched too recently to rule out a
// vend in flight.
func (s *BillService) AdminReversePayment(ctx context.Context, orderID uuid.UUID) (*models.BillOrder, error) {
	o, err := s.adminPaymentOrder(ctx, orderID, models.BillPayStatusReversed)
	if err != nil {
		return nil, err
	}
	switch o.Status {
	case models.BillStatusVendFail:
		if _, err := s.payer.Reverse(ctx, o.ID); err != nil {
			return nil, err
		}
		s.moveOrder(ctx, o, []string{models.BillStatusVendFail}, map[string]any{"status": models.BillStatusRefunded})
		return o, nil
	case models.BillStatusFailed, models.BillStatusQuoted, models.BillStatusExpired:
		// Pay only vends once the order is paid, so these never reached it.
		if _, err := s.payer.Reverse(ctx, o.ID); err != nil {
			return nil, err
		}
		return o, nil
	case models.BillStatusPaid, models.BillStatusVending:
		if time.Since(o.UpdatedAt) < billAdminInFlightGrace {
			return nil, fmt.Errorf("%w: the order changed %s ago; a vend may still be in flight", ErrBillVendUnverified, time.Since(o.UpdatedAt).Round(time.Second))
		}
		res, err := s.vendor.QueryOrder(ctx, o.ID.String())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBillVendUnverified, err)
		}
		switch decision, _ := decideVend(res.StatusCode); decision {
		case VendSuccess:
			return nil, fmt.Errorf("%w: the vendor reports this bill delivered", ErrBillPaymentConflict)
		case VendPending:
			return nil, fmt.Errorf("%w: the vend is still pending at the vendor (code %d)", ErrBillPaymentConflict, res.StatusCode)
		}
		if !queryConfirmsVendFailed(o, res) {
			return nil, fmt.Errorf("%w: the vendor's answer (order %q, code %d %s) does not confirm the vend failed", ErrBillVendUnverified, res.OrderID, res.StatusCode, res.Status)
		}
		switch aerr := s.adoptVendResult(ctx, o, res); {
		case errors.Is(aerr, errBillOrderChanged):
			return nil, fmt.Errorf("%w: the order changed while it was being checked; reload and retry", ErrBillPaymentConflict)
		case aerr != nil:
			return nil, aerr
		}
		return o, nil
	default:
		return nil, fmt.Errorf("%w: order is %s", ErrBillPaymentConflict, o.Status)
	}
}

// AdminSettlePayment frees a bill's backing, but only on evidence the bill was
// delivered: recorded as delivered, or reported successful for this order by
// the vendor now. A settle that fails is returned.
func (s *BillService) AdminSettlePayment(ctx context.Context, orderID uuid.UUID) (*models.BillOrder, error) {
	o, err := s.adminPaymentOrder(ctx, orderID, models.BillPayStatusSettled)
	if err != nil {
		return nil, err
	}
	switch o.Status {
	case models.BillStatusDelivered:
		if _, err := s.payer.Settle(ctx, o.ID); err != nil {
			return nil, err
		}
		return o, nil
	case models.BillStatusPaid, models.BillStatusVending:
		res, err := s.vendor.QueryOrder(ctx, o.ID.String())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBillVendUnverified, err)
		}
		if !queryConfirmsVendDelivered(o, res) {
			return nil, fmt.Errorf("%w: the vendor does not report this bill delivered (order %q, code %d)", ErrBillPaymentConflict, res.OrderID, res.StatusCode)
		}
		switch aerr := s.adoptVendResult(ctx, o, res); {
		case errors.Is(aerr, errBillOrderChanged):
			return nil, fmt.Errorf("%w: the order changed while it was being checked; reload and retry", ErrBillPaymentConflict)
		case aerr != nil:
			return nil, aerr
		}
		return o, nil
	default:
		return nil, fmt.Errorf("%w: order is %s", ErrBillPaymentConflict, o.Status)
	}
}

// adminPaymentOrder loads an order whose payment is `debited` and so can be
// settled or reversed. A `debiting` payment is first resolved from chain
// state; an unreadable chain is ErrBillDebitUnverified, and an unconfirmed
// debit, or a payment already `done` or otherwise terminal, is refused with
// ErrBillPayBadState.
func (s *BillService) adminPaymentOrder(ctx context.Context, orderID uuid.UUID, done string) (*models.BillOrder, error) {
	if !s.IsEnabled() {
		return nil, ErrBillsDisabled
	}
	var o models.BillOrder
	if err := s.db.WithContext(ctx).First(&o, "id = ?", orderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillNotFound
		}
		return nil, err
	}
	status, _, err := s.payer.ResolveDebiting(ctx, o.ID)
	if err != nil {
		if errors.Is(err, ErrBillPayNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrBillDebitUnverified, err)
	}
	switch status {
	case done:
		return nil, fmt.Errorf("%w: payment is already %s", ErrBillPayBadState, status)
	case models.BillPayStatusDebited:
		return &o, nil
	case models.BillPayStatusDebiting:
		return nil, fmt.Errorf("%w: the debit is not confirmed on-chain yet", ErrBillPayBadState)
	default:
		return nil, fmt.Errorf("%w: payment is %s", ErrBillPayBadState, status)
	}
}

// BillStuckPayment is a non-terminal bill payment with the order evidence an
// operator needs to decide between settle and reverse.
type BillStuckPayment struct {
	models.BillPayment
	OrderStatus      string `json:"orderStatus"`
	Category         string `json:"category"`
	Provider         string `json:"provider"`
	NgnAmount        int64  `json:"ngnAmount"`
	VendorOrderID    string `json:"vendorOrderId,omitempty"`
	VendorStatusCode int    `json:"vendorStatusCode,omitempty"`
	OrderFailure     string `json:"orderFailureReason,omitempty"`
}

// ListStuckPayments lists debiting/debited bill payments older than olderThan,
// joined to their orders.
func (s *BillService) ListStuckPayments(ctx context.Context, olderThan time.Duration, limit int) ([]BillStuckPayment, error) {
	if !s.IsEnabled() {
		return nil, ErrBillsDisabled
	}
	pays, err := s.payer.StuckPayments(ctx, olderThan, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(pays))
	for _, p := range pays {
		ids = append(ids, p.OrderID)
	}
	orders := map[uuid.UUID]models.BillOrder{}
	if len(ids) > 0 {
		var rows []models.BillOrder
		if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, o := range rows {
			orders[o.ID] = o
		}
	}
	out := make([]BillStuckPayment, 0, len(pays))
	for _, p := range pays {
		row := BillStuckPayment{BillPayment: p}
		if o, ok := orders[p.OrderID]; ok {
			row.OrderStatus, row.Category, row.Provider = o.Status, o.Category, o.Provider
			row.NgnAmount, row.VendorOrderID, row.VendorStatusCode = o.NgnAmount, o.VendorOrderID, o.VendorStatusCode
			row.OrderFailure = o.FailureReason
		}
		out = append(out, row)
	}
	return out, nil
}

// ─── queries ─────────────────────────────────────────────────────────────────

func (s *BillService) GetOrder(ctx context.Context, userID, orderID uuid.UUID) (*models.BillOrder, error) {
	var o models.BillOrder
	if err := s.db.WithContext(ctx).First(&o, "id = ? AND user_id = ?", orderID, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillNotFound
		}
		return nil, err
	}
	s.refreshVending(ctx, &o)
	return &o, nil
}

// vendRefreshAfter keeps a polling client from turning into a query per tick:
// adopting a still-pending answer bumps updated_at, so one query per window.
const vendRefreshAfter = 2 * time.Second

// refreshVending asks ClubKonnect where a vending order stands when the user
// looks at it. ClubKonnect usually answers a vend "received" (100) and delivers
// seconds later; without this the order waits for the reconciler, so a customer
// who already has their airtime keeps seeing Processing. Best-effort: a failed
// query leaves the stored status.
func (s *BillService) refreshVending(ctx context.Context, o *models.BillOrder) {
	if o.Status != models.BillStatusVending || !s.IsEnabled() || time.Since(o.UpdatedAt) < vendRefreshAfter {
		return
	}
	s.syncFromVendor(ctx, o)
}

// syncFromVendor queries the vendor for a vending order and adopts the answer
// through the same guarded path the reconciler uses, so a race with it, with a
// callback or with a user refresh is harmless. o is reloaded afterwards.
func (s *BillService) syncFromVendor(ctx context.Context, o *models.BillOrder) {
	qctx, cancel := context.WithTimeout(ctx, ckReadTimeout)
	defer cancel()
	res, err := s.vendor.QueryOrder(qctx, o.ID.String())
	if err != nil {
		return
	}
	if aerr := s.adoptVendResult(qctx, o, res); aerr != nil {
		log.Printf("[bills] order=%s sync from vendor: %v", o.ID, aerr)
	}
	_ = s.db.WithContext(ctx).First(o, "id = ?", o.ID).Error
}

// ResolveFromCallback is the vendor-callback entry point. The callback itself
// is unauthenticated and its payload untrusted (see ClubkonnectClient.QueryOrder),
// so it is only a prompt to ask the vendor: nothing in it decides an outcome.
// requestID is our order id (the RequestID sent with the vend); vendorOrderID
// is ClubKonnect's, used when the payload carries only that. An unknown or
// already-resolved order is not an error — the vendor must not retry it.
func (s *BillService) ResolveFromCallback(ctx context.Context, requestID, vendorOrderID string) error {
	if !s.IsEnabled() {
		return ErrBillsDisabled
	}
	q := s.db.WithContext(ctx).Where("status = ?", models.BillStatusVending)
	switch id, err := uuid.Parse(strings.TrimSpace(requestID)); {
	case err == nil:
		q = q.Where("id = ?", id)
	case strings.TrimSpace(vendorOrderID) != "":
		q = q.Where("vendor_order_id = ?", strings.TrimSpace(vendorOrderID))
	default:
		return nil
	}
	var o models.BillOrder
	if err := q.First(&o).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	s.syncFromVendor(ctx, &o)
	return nil
}

func (s *BillService) ListOrders(ctx context.Context, userID uuid.UUID, limit int) ([]models.BillOrder, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var out []models.BillOrder
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("created_at DESC").Limit(limit).Find(&out).Error
	return out, err
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// validNigerianPhone accepts an 11-digit local (0…) or 13-digit international
// (234…) Nigerian MSISDN.
func validNigerianPhone(p string) bool {
	p = strings.TrimSpace(p)
	if len(p) == 11 && strings.HasPrefix(p, "0") {
		return allDigits(p)
	}
	if len(p) == 13 && strings.HasPrefix(p, "234") {
		return allDigits(p)
	}
	return false
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// checkLookupInput bounds what may be forwarded to the vendor's verify
// endpoints. Meter and smartcard numbers are digit strings; anything else is
// rejected before it burns paid vendor-API quota. The bound also keeps the
// endpoint from being a free-text relay.
func checkLookupInput(customerID string) error {
	if len(customerID) < 6 || len(customerID) > 20 || !allDigits(customerID) {
		return fmt.Errorf("%w: invalid customer number", ErrBillBadRequest)
	}
	return nil
}

// truncate512 caps a stored vend payload at the column's 512 characters. Rune-
// safe (truncateRunes): a byte slice could cut a multi-byte character in half,
// and Postgres rejects invalid UTF-8 — which would fail the exact Save that
// records a delivered token/PIN.
func truncate512(s string) string { return truncateRunes(s, 512) }
