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
)

// billEscalateAfter is how long an order may sit non-terminal before the
// reconciler starts shouting for a human. ClubKonnect's own retry window is
// 1 hour, so anything older is genuinely stuck.
const billEscalateAfter = 24 * time.Hour

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
		order.Status = models.BillStatusExpired
		s.db.WithContext(ctx).Save(&order)
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
		Update("status", models.BillStatusPaying)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		// Lost the race (another caller owns the flow) or expired under us.
		if err := s.db.WithContext(ctx).First(&order, "id = ?", order.ID).Error; err != nil {
			return nil, err
		}
		if order.IsExpired(time.Now()) {
			order.Status = models.BillStatusExpired
			s.db.WithContext(ctx).Save(&order)
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
		var pay models.BillPayment
		perr := s.db.WithContext(ctx).Where("order_id = ?", order.ID).First(&pay).Error
		switch {
		case errors.Is(perr, gorm.ErrRecordNotFound):
			order.Status = models.BillStatusQuoted
			order.FailureReason = truncateReason(err.Error())
			s.db.WithContext(ctx).Save(&order)
			return nil, err
		case perr != nil:
			return nil, err
		case pay.Status == models.BillPayStatusDebiting:
			order.FailureReason = truncateReason("debit unconfirmed: " + err.Error())
			s.db.WithContext(ctx).Save(&order)
			return nil, fmt.Errorf("payment pending confirmation; it will resolve shortly")
		case pay.Status == models.BillPayStatusFailed:
			order.Status = models.BillStatusFailed
			order.FailureReason = truncateReason(err.Error())
			s.db.WithContext(ctx).Save(&order)
			return nil, err
		default:
			// Debited (or beyond) by a concurrent caller — leave the flow to it
			// and the reconciler.
			return nil, fmt.Errorf("payment already in progress")
		}
	}

	order.Status = models.BillStatusPaid
	if err := s.db.WithContext(ctx).Save(&order).Error; err != nil {
		return nil, err
	}
	s.vend(ctx, &order)
	return &order, nil
}

// vend calls ClubKonnect and resolves the order as far as the response allows.
// It never returns an error: the order status carries the outcome, and an
// unknown outcome parks the order in `vending` for the reconciler (a transport
// error does NOT mean the vend didn't happen).
func (s *BillService) vend(ctx context.Context, o *models.BillOrder) {
	res, err := s.callVendor(ctx, o)
	if err != nil {
		o.Status = models.BillStatusVending
		o.FailureReason = truncateReason("vend outcome unknown: " + err.Error())
		s.db.WithContext(ctx).Save(o)
		return
	}
	s.adoptVendResult(ctx, o, res)
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

// adoptVendResult moves an order (and its payment) to wherever a vend/query
// response says it is. Shared by the inline vend and the reconciler.
func (s *BillService) adoptVendResult(ctx context.Context, o *models.BillOrder, res BillVendResult) {
	if res.OrderID != "" {
		o.VendorOrderID = res.OrderID
	}
	if res.StatusCode != 0 {
		o.VendorStatusCode = res.StatusCode
	}
	if res.Delivery != "" {
		o.DeliveryPayload = truncate512(res.Delivery)
	}
	decision, floatLow := decideVend(res.StatusCode)
	if floatLow {
		// Our prepaid float ran dry mid-purchase. Page-level: bills are down for
		// everyone until treasury tops up.
		log.Printf("[bills] ALERT bill_float_exhausted_total=1 order=%s code=%d", o.ID, res.StatusCode)
	}
	switch decision {
	case VendSuccess:
		o.Status = models.BillStatusDelivered
		o.FailureReason = ""
		// The delivery (meter token / PIN) must be on disk BEFORE the debit is
		// settled: settling is irreversible, and settling a delivery we failed
		// to record charges the user for a product they cannot retrieve.
		if err := s.db.WithContext(ctx).Save(o).Error; err != nil {
			log.Printf("[bills] ALERT bill_delivery_persist_failed_total=1 order=%s: %v", o.ID, err)
			return // stays non-terminal; the reconciler re-queries and retries
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
		}
	case VendFailed:
		o.Status = models.BillStatusVendFail
		reason := res.Remark
		if reason == "" {
			reason = fmt.Sprintf("vend failed (code %d %s)", res.StatusCode, res.Status)
		}
		if floatLow {
			reason = "service temporarily unavailable" // never blame the user for our float
		}
		o.FailureReason = truncateReason(reason)
		if err := s.db.WithContext(ctx).Save(o).Error; err != nil {
			log.Printf("[bills] vend-fail persist failed order=%s: %v", o.ID, err)
			return // reconciler re-queries and re-adopts
		}
		if _, err := s.payer.Reverse(ctx, o.ID); err != nil {
			log.Printf("[bills] reverse deferred order=%s: %v", o.ID, err)
			return
		}
		o.Status = models.BillStatusRefunded
		s.db.WithContext(ctx).Save(o)
	default: // pending
		o.Status = models.BillStatusVending
		s.db.WithContext(ctx).Save(o)
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
	cutoff := time.Now().Add(-grace)

	// Expire dead quotes (cheap bulk update; no money involved).
	res := s.db.WithContext(ctx).Model(&models.BillOrder{}).
		Where("status = ? AND quote_expires_at < ?", models.BillStatusQuoted, cutoff).
		Updates(map[string]any{"status": models.BillStatusExpired})
	changed += int(res.RowsAffected)

	var orders []models.BillOrder
	if err := s.db.WithContext(ctx).
		Where("status IN ? AND updated_at < ?",
			[]string{models.BillStatusPaying, models.BillStatusPaid, models.BillStatusVending}, cutoff).
		Order("updated_at asc").Limit(100).Find(&orders).Error; err != nil {
		return changed, err
	}
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
		log.Printf("[bills] ALERT bill_delivered_without_payment_total=%d — vend without a confirmed debit", freeVends)
	}

	// Orders terminal but whose payment leg didn't resolve (settle/reverse
	// deferred earlier). Retry quietly.
	var unresolved []models.BillOrder
	if err := s.db.WithContext(ctx).
		Joins("JOIN bill_payments ON bill_payments.order_id = bill_orders.id").
		Where("bill_orders.status IN ? AND bill_payments.status = ?",
			[]string{models.BillStatusDelivered, models.BillStatusVendFail}, models.BillPayStatusDebited).
		Limit(50).Find(&unresolved).Error; err == nil {
		for i := range unresolved {
			o := &unresolved[i]
			if o.Status == models.BillStatusDelivered {
				if _, err := s.payer.Settle(ctx, o.ID); err == nil {
					changed++
				}
			} else {
				if _, err := s.payer.Reverse(ctx, o.ID); err == nil {
					o.Status = models.BillStatusRefunded
					s.db.WithContext(ctx).Save(o)
					changed++
				}
			}
		}
	}
	return changed, nil
}

// reconcileOrder resolves one stale non-terminal order. Returns true if the
// order moved.
func (s *BillService) reconcileOrder(ctx context.Context, o *models.BillOrder) bool {
	before := o.Status
	switch o.Status {
	case models.BillStatusPaying:
		// The debit's fate is in the payment row.
		var pay models.BillPayment
		err := s.db.WithContext(ctx).Where("order_id = ?", o.ID).First(&pay).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && pay.Status == models.BillPayStatusFailed):
			// No debit ever confirmed — reopen the quote path.
			o.Status = models.BillStatusFailed
			o.FailureReason = truncateReason("payment did not complete")
			s.db.WithContext(ctx).Save(o)
		case err == nil && pay.Status == models.BillPayStatusDebited:
			o.Status = models.BillStatusPaid
			s.db.WithContext(ctx).Save(o)
			s.vend(ctx, o)
		case err == nil && pay.Status == models.BillPayStatusDebiting:
			// Broadcast but never verified: needs a human (or the operator's
			// pendingSettle view) — money may be on-chain.
			if time.Since(o.CreatedAt) > billEscalateAfter {
				log.Printf("[bills] ALERT bill_payment_unresolved_total=1 order=%s payment=%s", o.ID, pay.ID)
			}
		}
	case models.BillStatusPaid:
		// Debit confirmed but the vend outcome was lost. Ask ClubKonnect; if it
		// has never seen the RequestID, the vend was never sent — send it.
		res, err := s.vendor.QueryOrder(ctx, o.ID.String())
		if err != nil {
			s.vend(ctx, o)
		} else {
			s.adoptVendResult(ctx, o, res)
		}
	case models.BillStatusVending:
		res, err := s.vendor.QueryOrder(ctx, o.ID.String())
		if err == nil {
			s.adoptVendResult(ctx, o, res)
		}
		if o.Status == models.BillStatusVending && time.Since(o.CreatedAt) > billEscalateAfter {
			log.Printf("[bills] ALERT bill_vend_unresolved_total=1 order=%s vendor_order=%s", o.ID, o.VendorOrderID)
		}
	}
	return o.Status != before
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
	return &o, nil
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

// truncate512 is rune-safe: a byte slice could cut a multi-byte character in
// half, and Postgres rejects invalid UTF-8 — which would fail the exact Save
// that records a delivered token/PIN.
func truncate512(s string) string {
	if len(s) <= 512 {
		return s
	}
	r := []rune(s)
	if len(r) > 512 {
		r = r[:512]
	}
	return string(r)
}
