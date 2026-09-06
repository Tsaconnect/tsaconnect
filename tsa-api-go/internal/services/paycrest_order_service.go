package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// paycrest_order_service.go — the brokered ramp lifecycle.
//
// Both legs are non-custodial. On a buy the user transfers naira straight to
// the liquidity provider's bank account and the stablecoin lands in their own
// connected wallet; on a sell they send from that same wallet and the provider
// pays their bank. We never hold either side, so this service brokers and
// records rather than settling.
//
// Consequences that shape the code below:
//
//   - We cannot make a user whole ourselves. When a provider fails, the money
//     is at a bank we have no relationship with, and there is no refund
//     endpoint. So the one thing this service must never do is let such an
//     order look like an ordinary expiry — see markUnmatched.
//   - There is no webhook endpoint yet, so progress comes from polling. Orders
//     are therefore designed to be safely re-read: Sync is idempotent and only
//     ever moves a row forward.

var (
	// ErrPaycrestOrderOpen enforces one open order per user per direction. A
	// second concurrent buy would leave the user staring at two provider
	// accounts with two countdowns and no way to tell which one to pay.
	ErrPaycrestOrderOpen = errors.New("you already have an order awaiting payment")
	// ErrPaycrestBankUnsupported means the user's bank exists for us but not
	// for Paycrest. Explicit, because the alternative is a payout that
	// silently goes nowhere.
	ErrPaycrestBankUnsupported = errors.New("this bank cannot receive payouts through this route")
	ErrPaycrestOrderNotYours   = errors.New("order not found")
)

// PaycrestOrderer is the client surface this service needs.
type PaycrestOrderer interface {
	CreateOnrampOrder(ctx context.Context, req PaycrestOnrampRequest) (*PaycrestOrder, error)
	CreateOfframpOrder(ctx context.Context, req PaycrestOfframpRequest) (*PaycrestOrder, error)
	GetOrder(ctx context.Context, id string) (*PaycrestOrder, error)
}

// PaycrestOrderService brokers ramp orders.
type PaycrestOrderService struct {
	db     *gorm.DB
	pc     PaycrestOrderer
	quotes *PaycrestQuoteService
	dir    *PaycrestDirectory

	// senderFeePercent is our margin. Empty defers to the dashboard profile
	// default, which is what produced the 0.5% observed on live orders.
	senderFeePercent string
	senderFeeAddress string
}

// NewPaycrestOrderService returns nil unless everything it needs is wired, so
// routes can be gated the same way the rest of the package is.
func NewPaycrestOrderService(db *gorm.DB, pc PaycrestOrderer, quotes *PaycrestQuoteService, dir *PaycrestDirectory, feePercent, feeAddress string) *PaycrestOrderService {
	if db == nil || pc == nil || quotes == nil {
		return nil
	}
	return &PaycrestOrderService{
		db: db, pc: pc, quotes: quotes, dir: dir,
		senderFeePercent: strings.TrimSpace(feePercent),
		senderFeeAddress: strings.TrimSpace(feeAddress),
	}
}

// ─── Buy (onramp) ──────────────────────────────────────────────────────────

// PaycrestBuyRequest is what the app sends to start a purchase.
type PaycrestBuyRequest struct {
	UserID uuid.UUID
	// WalletAddress is the user's connected wallet — where the stablecoin is
	// delivered. Already reconciled against the account by the handler.
	WalletAddress string
	FiatCode      string
	FiatAmount    string
	// ChainKey and Token name the venue the user picked from a fresh quote.
	ChainKey string
	Token    string
	// RefundAccount is where naira returns if the order fails. Defaults to the
	// user's own bank, so a refund goes back to whoever paid.
	RefundBankCode    string // NIBSS
	RefundAccount     string
	RefundAccountName string
}

// CreateBuy prices the chosen venue afresh, opens the order, and returns the
// provider account for the user to pay.
//
// The re-quote is not defensive padding: liquidity moved 4× overnight in
// testing, so a venue chosen even a minute ago may no longer be fillable at
// this size. Binding the order to a price we just confirmed is the only way to
// avoid showing a rate the order will not carry.
func (s *PaycrestOrderService) CreateBuy(ctx context.Context, req PaycrestBuyRequest) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		return nil, errors.New("connect a wallet before buying")
	}
	if req.RefundBankCode == "" || req.RefundAccount == "" {
		return nil, errors.New("a refund account is required in case the order fails")
	}
	if err := s.assertNoOpenOrder(ctx, req.UserID, models.PaycrestDirectionBuy); err != nil {
		return nil, err
	}

	fiat := strings.ToUpper(strings.TrimSpace(req.FiatCode))
	if fiat == "" {
		fiat = "NGN"
	}
	venue, err := s.quotes.VenueFor(ctx, "buy", fiat, req.FiatAmount, req.ChainKey, req.Token)
	if err != nil {
		return nil, err
	}
	refundInst, err := s.paycrestBankCode(ctx, fiat, req.RefundBankCode)
	if err != nil {
		return nil, err
	}

	ref := paycrestReference("PCB", req.UserID)
	order, err := s.pc.CreateOnrampOrder(ctx, PaycrestOnrampRequest{
		AmountNGN:        req.FiatAmount,
		FiatCode:         fiat,
		Token:            venue.Token,
		Network:          venue.Network,
		RecipientAddress: req.WalletAddress,
		RefundAccount: PaycrestBankAccount{
			Institution:       refundInst,
			AccountIdentifier: req.RefundAccount,
			AccountName:       req.RefundAccountName,
		},
		Reference:        ref,
		SenderFeePercent: s.senderFeePercent,
		SenderFeeAddress: s.senderFeeAddress,
	})
	if err != nil {
		return nil, err
	}

	row := &models.PaycrestOrderRecord{
		UserID:        req.UserID,
		Direction:     models.PaycrestDirectionBuy,
		Status:        models.PaycrestStatusAwaitingPayment,
		Reference:     ref,
		PaycrestID:    order.ID,
		FiatCode:      fiat,
		FiatAmount:    req.FiatAmount,
		Token:         venue.Token,
		ChainKey:      venue.ChainKey,
		Network:       venue.Network,
		Rate:          order.Rate,
		TokenAmount:   order.Amount,
		ProviderID:    paycrestProviderOf(order, venue),
		SenderFee:     order.SenderFee,
		WalletAddress: req.WalletAddress,
		// Refund destination is recorded so an operator chasing a stranded
		// deposit knows where the money was supposed to come back to.
		PayoutBankCode:    req.RefundBankCode,
		PayoutInstitution: refundInst,
		PayoutAccount:     req.RefundAccount,
		PayoutAccountName: req.RefundAccountName,
	}
	if pa := order.ProviderAccount; pa != nil {
		row.PayInstitution = pa.Institution
		row.PayAccountNumber = pa.AccountIdentifier
		row.PayAccountName = pa.AccountName
		row.PayAmount = pa.AmountToTransfer
		row.ValidUntil = parsePaycrestTime(pa.ValidUntil)
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		// The order exists at Paycrest but we failed to record it. Log loudly
		// with the reference: without this line the user could pay an account
		// nothing on our side knows about.
		log.Printf("[paycrest] ALARM created order %s (ref=%s) but could not persist it: %v",
			order.ID, ref, err)
		return nil, err
	}
	return row, nil
}

// ─── Sell (offramp) ────────────────────────────────────────────────────────

// PaycrestSellRequest starts a sale. The user sends the stablecoin themselves
// from their connected wallet.
type PaycrestSellRequest struct {
	UserID uuid.UUID
	// WalletAddress funds the sale and receives the refund if it fails.
	WalletAddress string
	FiatCode      string
	// FiatAmount is what the user wants to receive; the token amount is
	// derived from the live quote.
	FiatAmount string
	ChainKey   string
	Token      string
	// Payout bank, in OUR (NIBSS) vocabulary.
	BankCode    string
	AccountNo   string
	AccountName string
}

// CreateSell opens an offramp and returns the address the user must send to.
func (s *PaycrestOrderService) CreateSell(ctx context.Context, req PaycrestSellRequest) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		return nil, errors.New("connect a wallet before selling")
	}
	if req.BankCode == "" || req.AccountNo == "" {
		return nil, errors.New("a payout bank account is required")
	}
	if err := s.assertNoOpenOrder(ctx, req.UserID, models.PaycrestDirectionSell); err != nil {
		return nil, err
	}

	fiat := strings.ToUpper(strings.TrimSpace(req.FiatCode))
	if fiat == "" {
		fiat = "NGN"
	}
	venue, err := s.quotes.VenueFor(ctx, "sell", fiat, req.FiatAmount, req.ChainKey, req.Token)
	if err != nil {
		return nil, err
	}
	payoutInst, err := s.paycrestBankCode(ctx, fiat, req.BankCode)
	if err != nil {
		return nil, err
	}

	// Paycrest prices an offramp in token units.
	tokenAmount := strconv.FormatFloat(venue.TokenAmount, 'f', 6, 64)

	ref := paycrestReference("PCS", req.UserID)
	order, err := s.pc.CreateOfframpOrder(ctx, PaycrestOfframpRequest{
		AmountToken: tokenAmount,
		Token:       venue.Token,
		Network:     venue.Network,
		FiatCode:    fiat,
		Recipient: PaycrestBankAccount{
			Institution:       payoutInst,
			AccountIdentifier: req.AccountNo,
			AccountName:       req.AccountName,
		},
		// Refunds return to the user's own wallet — the same one they send
		// from — so a failed sale leaves them exactly where they started.
		RefundAddress:    req.WalletAddress,
		Reference:        ref,
		SenderFeePercent: s.senderFeePercent,
		SenderFeeAddress: s.senderFeeAddress,
	})
	if err != nil {
		return nil, err
	}

	row := &models.PaycrestOrderRecord{
		UserID:            req.UserID,
		Direction:         models.PaycrestDirectionSell,
		Status:            models.PaycrestStatusAwaitingDeposit,
		Reference:         ref,
		PaycrestID:        order.ID,
		FiatCode:          fiat,
		FiatAmount:        req.FiatAmount,
		Token:             venue.Token,
		ChainKey:          venue.ChainKey,
		Network:           venue.Network,
		Rate:              order.Rate,
		TokenAmount:       tokenAmount,
		ProviderID:        paycrestProviderOf(order, venue),
		SenderFee:         order.SenderFee,
		WalletAddress:     req.WalletAddress,
		PayoutBankCode:    req.BankCode,
		PayoutInstitution: payoutInst,
		PayoutAccount:     req.AccountNo,
		PayoutAccountName: req.AccountName,
	}
	if pa := order.ProviderAccount; pa != nil {
		row.DepositAddress = pa.ReceiveAddress
		row.ValidUntil = parsePaycrestTime(pa.ValidUntil)
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		log.Printf("[paycrest] ALARM created offramp %s (ref=%s) but could not persist it: %v",
			order.ID, ref, err)
		return nil, err
	}
	return row, nil
}

// ─── Progress ──────────────────────────────────────────────────────────────

// Sync re-reads one order from Paycrest and moves our row forward. Idempotent
// and safe to call from both the poller and a user refresh.
func (s *PaycrestOrderService) Sync(ctx context.Context, row *models.PaycrestOrderRecord) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	if row == nil || row.PaycrestID == "" {
		return row, nil
	}
	if models.PaycrestOrderTerminal(row.Status) {
		return row, nil // nothing upstream can change it
	}

	remote, err := s.pc.GetOrder(ctx, row.PaycrestID)
	if err != nil {
		// A 404 shortly after creation is read-after-write lag; later it means
		// the order never existed. Either way, do not mutate the row on a read
		// failure — the poller will come back.
		return row, err
	}

	now := time.Now()
	updates := map[string]any{"last_checked_at": &now}
	if remote.AmountPaid != "" {
		updates["amount_paid"] = remote.AmountPaid
	}
	if remote.AmountReturned != "" {
		updates["amount_returned"] = remote.AmountReturned
	}
	if h := latestPaycrestTxHash(remote); h != "" {
		updates["tx_hash"] = h
	}

	next := mapPaycrestStatus(remote, row.Direction)
	if next == models.PaycrestStatusUnmatched {
		// The user paid, the bank confirmed, and the protocol never saw it.
		// Loud, because nothing automated can resolve this.
		log.Printf("[paycrest] ALARM unmatched deposit order=%s ref=%s user=%s amount=%s %s "+
			"— funds are at the provider's bank with no API remedy; manual recovery required",
			remote.ID, row.Reference, row.UserID, row.PayAmount, row.FiatCode)
		updates["failure_reason"] = "Payment reached the provider but was never registered against the order. Manual recovery required."
	}
	if next != "" && next != row.Status {
		updates["status"] = next
		if next == models.PaycrestStatusSettled {
			updates["settled_at"] = &now
		}
	}

	if err := s.db.WithContext(ctx).Model(row).Updates(updates).Error; err != nil {
		return row, err
	}
	return s.Get(ctx, row.UserID, row.ID)
}

// mapPaycrestStatus translates their vocabulary into ours, and detects the one
// state they have no word for.
func mapPaycrestStatus(o *PaycrestOrder, direction string) string {
	if o == nil {
		return ""
	}
	if o.UnmatchedDeposit() {
		return models.PaycrestStatusUnmatched
	}
	switch strings.ToLower(strings.TrimSpace(o.Status)) {
	case PaycrestStatusSettled:
		return models.PaycrestStatusSettled
	case PaycrestStatusSettling:
		return models.PaycrestStatusSettling
	case PaycrestStatusRefunded:
		return models.PaycrestStatusRefunded
	case PaycrestStatusExpired:
		return models.PaycrestStatusExpired
	case PaycrestStatusCancelled:
		return models.PaycrestStatusFailed
	case PaycrestStatusPending:
		// "pending" covers both "waiting for the user" and "provider assigned,
		// funds seen". amountPaid is what separates them.
		if paid := strings.TrimSpace(o.AmountPaid); paid != "" && paid != "0" {
			return models.PaycrestStatusPending
		}
		if direction == models.PaycrestDirectionSell {
			return models.PaycrestStatusAwaitingDeposit
		}
		return models.PaycrestStatusAwaitingPayment
	}
	return ""
}

// ─── Reads ─────────────────────────────────────────────────────────────────

// Get fetches one of the user's own orders.
func (s *PaycrestOrderService) Get(ctx context.Context, userID, orderID uuid.UUID) (*models.PaycrestOrderRecord, error) {
	var row models.PaycrestOrderRecord
	err := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", orderID, userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPaycrestOrderNotYours
	}
	return &row, err
}

// List returns the user's recent orders, newest first.
func (s *PaycrestOrderService) List(ctx context.Context, userID uuid.UUID, limit int) ([]models.PaycrestOrderRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var rows []models.PaycrestOrderRecord
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// DueForSync selects non-terminal orders for the poller, oldest check first.
func (s *PaycrestOrderService) DueForSync(ctx context.Context, limit int) ([]models.PaycrestOrderRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	terminal := []string{
		models.PaycrestStatusSettled, models.PaycrestStatusRefunded,
		models.PaycrestStatusExpired, models.PaycrestStatusUnmatched,
		models.PaycrestStatusFailed,
	}
	var rows []models.PaycrestOrderRecord
	err := s.db.WithContext(ctx).
		Where("status NOT IN ? AND paycrest_id <> ''", terminal).
		Order("last_checked_at ASC NULLS FIRST").Limit(limit).Find(&rows).Error
	return rows, err
}

// ─── Helpers ───────────────────────────────────────────────────────────────

// assertNoOpenOrder keeps one live order per direction per user.
func (s *PaycrestOrderService) assertNoOpenOrder(ctx context.Context, userID uuid.UUID, direction string) error {
	open := []string{
		models.PaycrestStatusAwaitingPayment, models.PaycrestStatusAwaitingDeposit,
		models.PaycrestStatusPending, models.PaycrestStatusSettling,
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("user_id = ? AND direction = ? AND status IN ?", userID, direction, open).
		Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrPaycrestOrderOpen
	}
	return nil
}

// paycrestBankCode translates a NIBSS code into Paycrest's, refusing rather
// than guessing — a wrong institution sends someone's payout to another bank.
func (s *PaycrestOrderService) paycrestBankCode(ctx context.Context, fiat, nibss string) (string, error) {
	if s.dir == nil {
		return "", ErrPaycrestDisabled
	}
	m, err := s.dir.BankMap(ctx, fiat)
	if err != nil {
		return "", err
	}
	code, ok := m.NIBSSToPaycrest[strings.TrimSpace(nibss)]
	if !ok {
		return "", fmt.Errorf("%w (bank %s)", ErrPaycrestBankUnsupported, nibss)
	}
	return code, nil
}

// paycrestReference mints our idempotency key. The user id is embedded so an
// operator reading a Paycrest dashboard row can identify the customer without
// our database.
func paycrestReference(prefix string, userID uuid.UUID) string {
	return fmt.Sprintf("%s-%s-%s", prefix,
		strings.ReplaceAll(userID.String(), "-", "")[:12],
		strings.ReplaceAll(uuid.NewString(), "-", "")[:10])
}

// paycrestProviderOf prefers the provider the order actually landed on, and
// falls back to the quote's first ranked provider.
func paycrestProviderOf(o *PaycrestOrder, v *PaycrestVenue) string {
	if o != nil {
		var dest struct {
			ProviderID string `json:"providerId"`
		}
		if len(o.Destination) > 0 {
			_ = json.Unmarshal(o.Destination, &dest)
		}
		if dest.ProviderID != "" {
			return dest.ProviderID
		}
	}
	if v != nil && len(v.ProviderIDs) > 0 {
		return v.ProviderIDs[0]
	}
	return ""
}

// latestPaycrestTxHash returns the most recent on-chain hash in the audit log.
func latestPaycrestTxHash(o *PaycrestOrder) string {
	for i := len(o.TransactionLogs) - 1; i >= 0; i-- {
		if h := strings.TrimSpace(o.TransactionLogs[i].TxHash); h != "" {
			return h
		}
	}
	return ""
}

// parsePaycrestTime accepts the RFC3339 variants their API mixes (with and
// without sub-second precision). An unparseable value yields nil rather than a
// zero time, so a bad expiry never reads as "already expired".
func parsePaycrestTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

// ─── Webhooks ──────────────────────────────────────────────────────────────

// ErrPaycrestOrderUnknown means an event named an order we have no row for.
// Not retryable: a redelivery cannot make the row appear, and the likeliest
// cause is an order created by another environment sharing the API key.
var ErrPaycrestOrderUnknown = errors.New("paycrest: webhook names an order we do not have")

// HandleWebhook advances an order from a verified webhook.
//
// The event is a HINT, never an authority. It tells us which order moved; Sync
// then re-reads that order from the API and decides. That matters here more
// than usual: the failure this integration must never mishandle is an expired
// order whose deposit was never registered, and only the API's amountPaid can
// distinguish it from an ordinary expiry.
//
// Looking up by OUR reference rather than their order id is deliberate — the
// reference exists on our row from the moment we create it, whereas their id
// only arrives with the response, so a webhook racing our own persist still
// finds its order.
func (s *PaycrestOrderService) HandleWebhook(ctx context.Context, ev *PaycrestWebhookEvent) error {
	if s == nil {
		return ErrPaycrestDisabled
	}
	if ev == nil {
		return errors.New("paycrest webhook: nil event")
	}

	var row models.PaycrestOrderRecord
	q := s.db.WithContext(ctx)
	var err error
	switch {
	case strings.TrimSpace(ev.Reference) != "":
		err = q.Where("reference = ?", ev.Reference).First(&row).Error
	case strings.TrimSpace(ev.OrderID) != "":
		err = q.Where("paycrest_id = ?", ev.OrderID).First(&row).Error
	default:
		return errors.New("paycrest webhook: event carries no identifier")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w (ref=%q id=%q)", ErrPaycrestOrderUnknown, ev.Reference, ev.OrderID)
	}
	if err != nil {
		return err
	}

	// An event can arrive before our own create call has finished persisting
	// the provider's id. Fill it in so the poller and any later event agree on
	// which remote order this row is.
	if row.PaycrestID == "" && strings.TrimSpace(ev.OrderID) != "" {
		row.PaycrestID = ev.OrderID
		if err := s.db.WithContext(ctx).Model(&row).
			Update("paycrest_id", ev.OrderID).Error; err != nil {
			return err
		}
	}
	if models.PaycrestOrderTerminal(row.Status) {
		return nil // nothing an event can add
	}

	_, err = s.Sync(ctx, &row)
	return err
}
