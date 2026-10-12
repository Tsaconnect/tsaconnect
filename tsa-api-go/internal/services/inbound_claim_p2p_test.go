package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ─── Rig ─────────────────────────────────────────────────────────────────────

// createSettlementForwardTable adds p2p_settlement_forwards to the shared trade
// SQLite rig. Raw DDL, not AutoMigrate, matching every other rig in this
// package — and the two unique indexes are spelled out because they are the
// idempotency guarantee under test, not incidental schema.
func createSettlementForwardTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS p2p_settlement_forwards (
		id             TEXT PRIMARY KEY,
		trade_id       TEXT NOT NULL,
		pm_id          TEXT NOT NULL,
		provider_ref   TEXT NOT NULL,
		currency       TEXT NOT NULL,
		amount         TEXT NOT NULL,
		status         TEXT NOT NULL DEFAULT 'pending',
		attempts       INTEGER NOT NULL DEFAULT 0,
		reference      TEXT NOT NULL,
		payout_id      TEXT,
		failure_reason TEXT,
		alerted_at     DATETIME,
		submitted_at   DATETIME,
		settled_at     DATETIME,
		started_at     DATETIME,
		fee_kobo       TEXT,
		created_at     DATETIME,
		updated_at     DATETIME
	)`).Error; err != nil {
		t.Fatalf("create p2p_settlement_forwards: %v", err)
	}
	for _, idx := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fwd_trade ON p2p_settlement_forwards (trade_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fwd_ref ON p2p_settlement_forwards (provider_ref)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fwd_reference ON p2p_settlement_forwards (reference)`,
	} {
		if err := db.Exec(idx).Error; err != nil {
			t.Fatalf("index: %v", err)
		}
	}
}

// fwdRig is everything a forward test needs, assembled once.
type fwdRig struct {
	db      *gorm.DB
	pm      *models.PaymentMerchant
	trade   *models.P2PTrade
	src     *P2PTradeClaimSource
	fwd     *P2PSettlementForwarder
	gateway *fakeNombaGateway
	bal     *stubPayoutBalances
	signer  *fakeReleaseSigner
	esc     *mockEscrow
}

// stubPayoutBalances replays a fixed NGN balance, or an error.
type stubPayoutBalances struct {
	ngn   float64
	err   error
	calls int
}

func (s *stubPayoutBalances) FetchBalances(_ context.Context) ([]FWBalance, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return []FWBalance{{Currency: "NGN", AvailableBalance: s.ngn}}, nil
}

// newForwardRig builds an active merchant with a settlement bank, one open buy
// trade on its sell ad, and a claim source with the forward wired.
func newForwardRig(t *testing.T, fiat string) *fwdRig {
	t.Helper()
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	createSettlementForwardTable(t, db)
	trades, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	// The settlement bank is where proceeds are swept. Already on the model and
	// already documented as such — no onboarding change was needed for this.
	if err := db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).
		Updates(map[string]any{
			"settlement_bank_code":      "058",
			"settlement_account_number": "0987654321",
			"settlement_account_name":   "TSA CONNECT LTD",
		}).Error; err != nil {
		t.Fatalf("seed settlement bank: %v", err)
	}
	pm.SettlementBankCode = "058"
	pm.SettlementAccountNumber = "0987654321"

	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	trade := seedBuyTradeOnAd(t, db, ad, uuid.New(), fiat)
	setReleasedEvent(esc, trade)

	signer := &fakeReleaseSigner{
		addr: "0x1111111111111111111111111111111111111111",
		hash: "0xrelease",
	}
	pmSvc := NewPMService(db)
	gateway := &fakeNombaGateway{payoutID: "trf_1"}
	bal := &stubPayoutBalances{ngn: 1_000_000}
	fwd := NewP2PSettlementForwarder(db, pmSvc, gateway, bal, "TSA Connect")

	src := NewP2PTradeClaimSource(db, pmSvc, trades, signer)
	src.SetForwarder(fwd)

	return &fwdRig{db: db, pm: pm, trade: trade, src: src, fwd: fwd, gateway: gateway, bal: bal, signer: signer, esc: esc}
}

func p2pInboundCredit(ref, amountNaira string) *models.InboundCredit {
	kobo, _ := ngnToKobo(amountNaira)
	return &models.InboundCredit{
		ID:          uuid.New(),
		Provider:    models.InboundCreditProviderFlutterwave,
		ProviderRef: ref,
		Currency:    "NGN",
		AmountKobo:  kobo,
		GrossKobo:   kobo,
		SenderRaw:   "Received Money from GTBANK|JOHN DOE",
		SenderName:  "JOHN DOE",
	}
}

// availableNGN reads the merchant's ledger balance.
//
// Compared with ngnEqual rather than by string equality: the SQLite rig stores
// the column as TEXT and gives back "1500" where PostgreSQL's numeric(38,2)
// gives "1500.00". The money is the same either way, and pinning the rig's
// formatting would make these tests assert the driver, not the ledger.
func (r *fwdRig) availableNGN(t *testing.T) string {
	t.Helper()
	var pm models.PaymentMerchant
	if err := r.db.First(&pm, "id = ?", r.pm.ID).Error; err != nil {
		t.Fatalf("load pm: %v", err)
	}
	return pm.AvailableNGN
}

func (r *fwdRig) forward(t *testing.T) *models.P2PSettlementForward {
	t.Helper()
	var f models.P2PSettlementForward
	if err := r.db.First(&f, "trade_id = ?", r.trade.ID).Error; err != nil {
		t.Fatalf("load forward: %v", err)
	}
	return &f
}

// ─── OpenClaims ──────────────────────────────────────────────────────────────

// The claim's UserID is the BUYER. This is the single most load-bearing fact in
// this file: the buyer sends the naira, so the matcher compares their name.
// Offering the merchant's user id would compare the wrong person's name against
// every transfer and match nothing — or worse, match the merchant's own float.
func TestP2POpenClaims_UserIsTheBuyerNotTheMerchant(t *testing.T) {
	r := newForwardRig(t, "1500.00")

	claims, err := r.src.OpenClaims(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want 1", len(claims))
	}
	c := claims[0]
	if c.UserID != r.trade.BuyerID {
		t.Fatalf("claim UserID = %s, want the BUYER %s", c.UserID, r.trade.BuyerID)
	}
	if c.UserID == r.pm.UserID {
		t.Fatal("claim UserID is the merchant's user; the buyer is who sends the money")
	}
	if c.Type != InboundClaimTypeP2PTrade {
		t.Fatalf("type = %q, want %q", c.Type, InboundClaimTypeP2PTrade)
	}
	if c.ID != r.trade.ID {
		t.Fatalf("claim ID = %s, want the trade id %s", c.ID, r.trade.ID)
	}
	if c.AmountKobo != 150000 {
		t.Fatalf("amountKobo = %d, want 150000", c.AmountKobo)
	}
	if c.AmountKobo%100 != 0 {
		t.Fatalf("amountKobo = %d is not whole naira; matching compares at naira granularity", c.AmountKobo)
	}
}

func TestP2POpenClaims_RoundsAFractionalTradeRatherThanDroppingIt(t *testing.T) {
	// Matching is at naira granularity, so a trade priced at ₦1500.50 has to be
	// rounded before it can bind anything — and rounded with the SAME helper the
	// matcher rounds the credit with, or the two sides drift and a correct
	// payment becomes a hold.
	//
	// Rounded, not dropped. Dropping it would hide a trade somebody has already
	// paid for from the matcher entirely, including from the hold reason an
	// operator reads when deciding where the money goes.
	r := newForwardRig(t, "1500.50")

	claims, err := r.src.OpenClaims(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want 1 — a fractional trade must still be visible to the matcher", len(claims))
	}
	if got := claims[0].AmountKobo; got != RoundKoboToNaira(150050) {
		t.Fatalf("amountKobo = %d, want %d (the matcher's own rounding)", got, RoundKoboToNaira(150050))
	}
	if claims[0].AmountKobo%100 != 0 {
		t.Fatalf("amountKobo = %d violates the InboundClaim contract's whole-naira invariant", claims[0].AmountKobo)
	}
}

func TestP2POpenClaims_ExcludesNonMerchantTrades(t *testing.T) {
	// A manual P2P trade's buyer pays the seller's own bank; that money never
	// reaches the company account. Offering it as a claim would let an
	// unrelated credit settle a trade nobody was actually paid for.
	r := newForwardRig(t, "1500.00")
	manualAd := &models.P2PAd{
		ID: uuid.New(), SellerID: uuid.New(), Side: models.P2PAdSideSell,
		Token: "USDT", ChainID: 146, Amount: "1000000", Remaining: "1000000", Reserved: "0",
		FiatCurrency: "NGN", Rate: "1300", RateType: models.P2PAdRateTypeFixed,
		Status: models.P2PAdStatusActive,
	}
	if err := r.db.Create(manualAd).Error; err != nil {
		t.Fatalf("seed manual ad: %v", err)
	}
	seedBuyTradeOnAd(t, r.db, manualAd, uuid.New(), "2500.00")

	claims, err := r.src.OpenClaims(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want only the merchant trade", len(claims))
	}
	if claims[0].ID != r.trade.ID {
		t.Fatalf("claimed the manual trade %s", claims[0].ID)
	}
}

func TestP2POpenClaims_FiltersByCurrency(t *testing.T) {
	r := newForwardRig(t, "1500.00")

	claims, err := r.src.OpenClaims(context.Background(), "GHS")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("got %d claims for GHS, want 0", len(claims))
	}
}

// ─── Accept ──────────────────────────────────────────────────────────────────

func TestP2PAccept_CreditsTheMerchantAndSettlesTheTrade(t *testing.T) {
	r := newForwardRig(t, "1500.00")

	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000001", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	var trade models.P2PTrade
	r.db.First(&trade, "id = ?", r.trade.ID)
	if trade.Status != models.P2PTradeStatusReleased {
		t.Fatalf("trade status = %q, want released", trade.Status)
	}
	if trade.NombaInboundEventID != "inbound:999000000001" {
		t.Fatalf("attribution = %q, want the NIBSS session id", trade.NombaInboundEventID)
	}
	// Credited, then forwarded, then confirmed by nothing yet — so the balance
	// is still the merchant's.
	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00", got)
	}
	if len(r.gateway.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1", len(r.gateway.calls))
	}
	if r.gateway.calls[0].AccountNumber != "0987654321" {
		t.Fatalf("forwarded to %q, want the merchant's settlement account", r.gateway.calls[0].AccountNumber)
	}
	if f := r.forward(t); f.Status != models.P2PForwardSubmitted {
		t.Fatalf("forward status = %q, want submitted", f.Status)
	}
}

// A replayed credit must move nothing. The poller re-runs every 30 seconds and
// a crash between the settle and the status write is expected, so this is the
// ordinary case rather than an edge one.
func TestP2PAccept_IsIdempotentOnProviderRef(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	credit := p2pInboundCredit("999000000002", "1500.00")

	if err := r.src.Accept(context.Background(), r.trade.ID, credit); err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	if err := r.src.Accept(context.Background(), r.trade.ID, credit); err != nil {
		t.Fatalf("replayed Accept: %v", err)
	}

	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q after replay, want 1500.00 — the replay credited twice", got)
	}
	if len(r.gateway.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1 — the replay paid the merchant twice", len(r.gateway.calls))
	}
	var count int64
	r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Count(&count)
	if count != 1 {
		t.Fatalf("%d forward rows, want 1", count)
	}
}

// A settle that fails AFTER the ledger credit must be finishable by replaying
// the same credit. The credit is durable the moment it commits, so a replay
// that short-circuited on the idempotency row would leave the trade wedged at
// pending_payment with the money already booked, and nothing coming back for it.
//
// The failure is injected as a verify error, the way the sibling paid-orphan
// tests inject it, because that is the only injection that produces the state
// this test is about. Blanking the escrow's released event does NOT break the
// release: the mock falls through to a default receipt, the escrow pays out,
// and what fails is confirmReleaseCore's money-invariant — a check that returns
// after the on-chain release has already happened. Replaying that is a SECOND
// release of the same escrow, not a retry, which is why the intermediate state
// is asserted exactly rather than as "anything but released".
func TestP2PAccept_RetryFinishesASettleThatFailedAfterTheCredit(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	credit := p2pInboundCredit("999000000015", "1500.00")

	// markPaid commits, then the release fails at verify and AutoRelease rolls
	// its `releasing` flip back to `paid`.
	r.esc.verifyReleasedErr = errors.New("rpc timeout verifying the release")

	// Accept still reports success: the credit IS attributed and markPaid has
	// committed, so only the release is outstanding and PMRecoveryJob owns it.
	// Reporting an error here would strand the credit — see
	// TestP2PMatch_AFailedReleaseDoesNotHoldTheCreditOnAFalseReason.
	if err := r.src.Accept(context.Background(), r.trade.ID, credit); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	var mid models.P2PTrade
	r.db.First(&mid, "id = ?", r.trade.ID)
	if mid.Status != models.P2PTradeStatusPaid {
		t.Fatalf("trade status = %q after the failed settle, want paid — the replay below is a retry only from the paid-orphan state; from any other one it re-releases an escrow that already paid out",
			mid.Status)
	}
	if mid.ReleaseTxHash != "" {
		t.Fatalf("release_tx_hash = %q — something was released, so replaying the settle would release it twice", mid.ReleaseTxHash)
	}
	if !ngnEqual(r.availableNGN(t), "1500.00") {
		t.Fatalf("available_ngn = %q; the ledger credit must stand even when the settle fails", r.availableNGN(t))
	}

	// Repair and replay the same credit.
	r.esc.verifyReleasedErr = nil
	if err := r.src.Accept(context.Background(), r.trade.ID, credit); err != nil {
		t.Fatalf("retry Accept: %v", err)
	}
	var done models.P2PTrade
	r.db.First(&done, "id = ?", r.trade.ID)
	if done.Status != models.P2PTradeStatusReleased {
		t.Fatalf("trade status = %q after retry, want released — the replay wedged it", done.Status)
	}
	// And still exactly one credit, and exactly one transfer.
	if !ngnEqual(r.availableNGN(t), "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00 — the retry credited twice", r.availableNGN(t))
	}
	if len(r.gateway.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1 — the retry paid the merchant twice", len(r.gateway.calls))
	}
}

func TestP2PAccept_LedgerCreditPrecedesTheTransfer(t *testing.T) {
	// The ordering claim, tested directly: the gateway must already see the
	// merchant credited by the time it is asked to move money. If the credit
	// came second, a gateway that panicked or hung would leave the merchant
	// with neither the money nor the ledger entry.
	r := newForwardRig(t, "1500.00")
	var balanceAtTransfer string
	r.gateway.onCall = func() { balanceAtTransfer = r.availableNGN(t) }

	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000003", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !ngnEqual(balanceAtTransfer, "1500.00") {
		t.Fatalf("available_ngn was %q when the transfer fired, want it already credited (1500.00)", balanceAtTransfer)
	}
}

// ─── The invariant: never neither ────────────────────────────────────────────
//
// A merchant must never end up with neither the money nor the ledger entry.
// Each test below drives one way the transfer can fail and asserts the same
// thing: the balance is still credited, and an operator can find the row.

func TestP2PForward_RejectedAtSubmissionLeavesTheBalanceCredited(t *testing.T) {
	// THE failure mode the MCGP sell leg gets wrong. The provider refuses the
	// transfer outright, so no transfer exists, so no transfer.disburse webhook
	// is ever sent, so nothing downstream can ever correct it. The balance must
	// already be the merchant's and must stay that way.
	r := newForwardRig(t, "1500.00")
	r.gateway.err = &ProviderRejectedError{Err: errors.New("insufficient funds in payout balance")}

	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000004", "1500.00")); err != nil {
		t.Fatalf("Accept must not fail because the forward did: %v", err)
	}

	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00 — the merchant has neither the money nor the ledger entry", got)
	}
	f := r.forward(t)
	if f.Status != models.P2PForwardFailed {
		t.Fatalf("forward status = %q, want failed", f.Status)
	}
	if !f.NeedsAttention() {
		t.Fatal("a rejected transfer must surface to an operator; nothing else will ever revisit it")
	}
	if f.AlertedAt == nil {
		t.Fatal("no admin alert raised for a transfer that will never happen")
	}
	if f.PayoutID != "" {
		t.Fatalf("payout id = %q, want empty — nothing was ever created at the provider", f.PayoutID)
	}
}

func TestP2PForward_AcceptedWithNoPayoutIDIsHeldNeverFailed(t *testing.T) {
	// The provider took the transfer but gave us no id. It may pay, so the row
	// is never failed as unpaid (review C-2): it stays pending and started,
	// alerted as outcome unknown, and the reference lookup or the disburse
	// webhook (both keyed by reference) resolves it. Never debit on a guess.
	r := newForwardRig(t, "1500.00")
	r.gateway.payoutID = ""

	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000005", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00", got)
	}
	f := r.forward(t)
	if f.Status != models.P2PForwardPending || f.StartedAt == nil {
		t.Fatalf("forward status = %q started=%v, want pending and started", f.Status, f.StartedAt)
	}
	if !strings.Contains(f.FailureReason, "outcome unknown") {
		t.Fatalf("failure_reason = %q, want it to say the outcome is unknown", f.FailureReason)
	}
}

func TestP2PForward_ProviderReportedFailureLeavesTheBalanceCredited(t *testing.T) {
	// The transfer was accepted and later failed. Money never left, so the
	// ledger entry is already correct and must not be debited.
	r := newForwardRig(t, "1500.00")
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000006", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	ref := r.forward(t).Reference

	if err := r.fwd.Confirm(context.Background(), ref, false, "account closed"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00 — a failed transfer debited the merchant", got)
	}
	f := r.forward(t)
	if f.Status != models.P2PForwardFailed {
		t.Fatalf("forward status = %q, want failed", f.Status)
	}
	if f.AlertedAt == nil {
		t.Fatal("no alert raised for a failed disbursement")
	}
}

func TestP2PForward_NeverAcknowledgedIsSweptAndAlerted(t *testing.T) {
	// The silent case: accepted, then nothing. No webhook of either kind will
	// arrive, so only a sweep makes it visible. The balance stays credited —
	// the invariant holds — but "unpaid and unnoticed" is not a resting state.
	r := newForwardRig(t, "1500.00")
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000007", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if r.forward(t).Status != models.P2PForwardSubmitted {
		t.Fatal("precondition: forward should be submitted")
	}

	// Nothing to sweep yet — a transfer submitted seconds ago is just in flight.
	n, err := r.fwd.SweepUnconfirmed(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("swept %d fresh forwards, want 0", n)
	}

	// Move past the staleness window.
	r.fwd.withClock(func() time.Time { return time.Now().Add(2 * p2pForwardStaleAfter) })
	n, err = r.fwd.SweepUnconfirmed(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d stale forwards, want 1", n)
	}
	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00 — the sweep must never move money", got)
	}
	if r.forward(t).AlertedAt == nil {
		t.Fatal("stale forward was not alerted on")
	}
	// Alerting is once per problem, not once per tick.
	n, _ = r.fwd.SweepUnconfirmed(context.Background())
	if n != 0 {
		t.Fatalf("re-swept %d already-alerted forwards, want 0", n)
	}
}

func TestP2PForward_MissingSettlementBankLeavesTheBalanceCredited(t *testing.T) {
	// The merchant has nowhere to be paid. That is a merchant-record problem,
	// and the existing manual withdrawal path can still reach the money —
	// provided the ledger entry is there, which it must be.
	r := newForwardRig(t, "1500.00")
	if err := r.db.Model(&models.PaymentMerchant{}).Where("id = ?", r.pm.ID).
		Updates(map[string]any{"settlement_bank_code": "", "settlement_account_number": ""}).Error; err != nil {
		t.Fatalf("clear settlement bank: %v", err)
	}

	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000008", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00", got)
	}
	if len(r.gateway.calls) != 0 {
		t.Fatal("submitted a transfer with no destination")
	}
	if f := r.forward(t); f.Status != models.P2PForwardFailed {
		t.Fatalf("forward status = %q, want failed", f.Status)
	}
}

// ─── Balance pre-check ───────────────────────────────────────────────────────

func TestP2PForward_ShortPayoutBalanceIsNotSubmitted(t *testing.T) {
	// The pre-check the MCGP sell leg lacks. Refusing here is free — the
	// merchant is already credited — whereas submitting burns a provider
	// rejection and puts a row in front of a human for no reason.
	r := newForwardRig(t, "1500.00")
	r.bal.ngn = 10 // ₦10 against a ₦1,500 forward

	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000009", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	if len(r.gateway.calls) != 0 {
		t.Fatal("submitted a transfer the payout balance cannot fund")
	}
	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00", got)
	}
	f := r.forward(t)
	// Retryable, NOT terminal: nothing was sent, so a later attempt is safe and
	// the balance may simply not have cleared yet.
	if f.Status != models.P2PForwardPending {
		t.Fatalf("forward status = %q, want pending — nothing was submitted", f.Status)
	}
	if f.FailureReason == "" {
		t.Fatal("no reason recorded for a deferred forward")
	}
}

func TestP2PForward_UnreadableBalanceIsNotSubmitted(t *testing.T) {
	// A balance we cannot read is the same bet as a balance we know is short,
	// with the same downside. The upside of guessing is only ever "we happened
	// to be right".
	r := newForwardRig(t, "1500.00")
	r.bal.err = errors.New("provider 503")

	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000010", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	if len(r.gateway.calls) != 0 {
		t.Fatal("submitted a transfer against an unknown balance")
	}
	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00", got)
	}
	if f := r.forward(t); f.Status != models.P2PForwardPending {
		t.Fatalf("forward status = %q, want pending (retryable)", f.Status)
	}
}

func TestP2PForward_DeferredForwardIsRetryable(t *testing.T) {
	// The point of leaving a deferred forward `pending` rather than `failed`:
	// once the balance is visible the same row goes out without an operator.
	r := newForwardRig(t, "1500.00")
	r.bal.ngn = 10
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000011", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	r.bal.ngn = 1_000_000
	if err := r.fwd.Forward(context.Background(), r.forward(t).ID); err != nil {
		t.Fatalf("retry Forward: %v", err)
	}
	if len(r.gateway.calls) != 1 {
		t.Fatalf("gateway called %d times on retry, want 1", len(r.gateway.calls))
	}
	if f := r.forward(t); f.Status != models.P2PForwardSubmitted {
		t.Fatalf("forward status = %q, want submitted", f.Status)
	}
}

// ─── The only debit ──────────────────────────────────────────────────────────

func TestP2PForward_ConfirmedDisbursementIsTheOnlyThingThatDebits(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000012", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q before confirmation, want 1500.00 — submitting is not disbursing", got)
	}
	ref := r.forward(t).Reference

	if err := r.fwd.Confirm(context.Background(), ref, true, ""); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	if got := r.availableNGN(t); !ngnEqual(got, "0.00") {
		t.Fatalf("available_ngn = %q after a confirmed disbursement, want 0.00", got)
	}
	f := r.forward(t)
	if f.Status != models.P2PForwardSettled {
		t.Fatalf("forward status = %q, want settled", f.Status)
	}
	if f.SettledAt == nil {
		t.Fatal("settled_at not stamped")
	}
}

func TestP2PForward_ConfirmIsIdempotent(t *testing.T) {
	// Webhook redelivery must not debit twice.
	r := newForwardRig(t, "1500.00")
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000013", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	ref := r.forward(t).Reference

	for i := 0; i < 3; i++ {
		if err := r.fwd.Confirm(context.Background(), ref, true, ""); err != nil {
			t.Fatalf("Confirm #%d: %v", i+1, err)
		}
	}
	if got := r.availableNGN(t); !ngnEqual(got, "0.00") {
		t.Fatalf("available_ngn = %q after 3 confirmations, want 0.00", got)
	}
}

func TestP2PForward_ConfirmIgnoresForeignReferences(t *testing.T) {
	// The disburse webhook is shared with the sell-payout flow. A reference
	// that is not ours must be left entirely alone, not guessed at.
	r := newForwardRig(t, "1500.00")
	if err := r.fwd.Confirm(context.Background(), uuid.New().String(), true, ""); err != nil {
		t.Fatalf("Confirm on a foreign reference: %v", err)
	}
	var count int64
	r.db.Model(&models.P2PSettlementForward{}).Count(&count)
	if count != 0 {
		t.Fatal("a foreign reference created or touched a forward row")
	}
}

func TestP2PForward_ListNeedingAttentionSurfacesUnpaidMerchants(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.gateway.err = &ProviderRejectedError{Err: errors.New("rejected")}
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999000000014", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	rows, err := r.fwd.ListNeedingAttention(context.Background())
	if err != nil {
		t.Fatalf("ListNeedingAttention: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Amount != "1500.00" || rows[0].PMID != r.pm.ID {
		t.Fatalf("queue row does not identify the unpaid merchant: %+v", rows[0])
	}
}

// ─── one trade, one credit ───────────────────────────────────────────────────

// trade_id is unique as well as provider_ref, so a SECOND, DISTINCT credit
// bound to the same trade violates the trade_id index — not the provider_ref
// one. Reading the winner back on either key hands this credit the other
// credit's row and reports success, and the matcher then stamps this credit
// `matched` with no ledger entry anywhere for its money.
//
// That is a silent absorption of real naira: a payer's transfer looks
// attributed and nobody is ever credited for it.
func TestP2PAccept_RefusesASecondDistinctCreditOnTheSameTrade(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()

	if err := r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("999000000020", "1500.00")); err != nil {
		t.Fatalf("first Accept: %v", err)
	}

	// A different payer's money, same amount, bound to the same trade — the
	// shape a duplicate transfer or a mis-keyed manual assign produces.
	second := p2pInboundCredit("999000000021", "1500.00")
	if err := r.src.Accept(ctx, r.trade.ID, second); err == nil {
		t.Fatal("a second, distinct credit reported success; the matcher will mark it matched with nothing behind it")
	}

	// Nothing of the second credit's money may have moved, and no row may claim it.
	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00 — the second credit moved money", got)
	}
	var claimed int64
	r.db.Model(&models.P2PSettlementForward{}).Where("provider_ref = ?", "999000000021").Count(&claimed)
	if claimed != 0 {
		t.Fatalf("%d forward rows claim the second credit's ref, want 0", claimed)
	}
	var total int64
	r.db.Model(&models.P2PSettlementForward{}).Count(&total)
	if total != 1 {
		t.Fatalf("%d forward rows, want 1", total)
	}
}

// ─── pending forwards are re-driven ──────────────────────────────────────────

// A held forward is left `pending` on the promise that "a later attempt can
// pick it up". Nothing did: Forward was only ever called from Accept, the
// staleness sweep filters `submitted`, and the operator queue filters `failed`.
// A merchant hit by an unreadable balance was therefore credited, never paid,
// and invisible on every surface.
func TestP2PForward_PendingForwardIsRedriven(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()
	r.bal.err = errors.New("provider 503")

	if err := r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("999000000022", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if f := r.forward(t); f.Status != models.P2PForwardPending {
		t.Fatalf("precondition: forward status = %q, want pending", f.Status)
	}

	// The outage clears. Past the redrive grace, so the sweep is not racing the
	// synchronous attempt Accept already made.
	r.bal.err = nil
	r.fwd.withClock(func() time.Time { return time.Now().Add(2 * p2pForwardRedriveAfter) })

	n, err := r.fwd.RedrivePending(ctx)
	if err != nil {
		t.Fatalf("RedrivePending: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-drove %d forwards, want 1", n)
	}
	if len(r.gateway.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1 — the merchant is still unpaid", len(r.gateway.calls))
	}
	if f := r.forward(t); f.Status != models.P2PForwardSubmitted {
		t.Fatalf("forward status = %q, want submitted", f.Status)
	}
	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q — submitting is not disbursing and must not debit", got)
	}
}

// A forward fresh out of Accept is not re-driven: Accept has just tried it
// synchronously, and a concurrent second attempt would submit the transfer
// twice.
func TestP2PForward_RedriveSkipsAForwardAccepJustTried(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()
	r.bal.err = errors.New("provider 503")
	if err := r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("999000000023", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	r.bal.err = nil

	n, err := r.fwd.RedrivePending(ctx)
	if err != nil {
		t.Fatalf("RedrivePending: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-drove %d forwards inside the grace window, want 0", n)
	}
}

// Retrying forever is its own silence: a forward that can never be paid would
// be re-driven every five minutes indefinitely and still never reach an
// operator, because only `failed` rows are queued. After the ceiling it
// escalates instead.
func TestP2PForward_UnpayableForwardEscalatesRatherThanRetryingForever(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()
	r.bal.err = errors.New("provider 503")

	if err := r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("999000000024", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	r.fwd.withClock(func() time.Time { return time.Now().Add(2 * p2pForwardRedriveAfter) })

	for i := 0; i < p2pForwardMaxAttempts+2; i++ {
		if _, err := r.fwd.RedrivePending(ctx); err != nil {
			t.Fatalf("RedrivePending #%d: %v", i+1, err)
		}
	}

	f := r.forward(t)
	if f.Status != models.P2PForwardFailed {
		t.Fatalf("forward status = %q after %d attempts, want failed — it retries forever and no operator ever sees it",
			f.Status, p2pForwardMaxAttempts+2)
	}
	if f.AlertedAt == nil {
		t.Fatal("escalated without raising an alert")
	}
	if len(r.gateway.calls) != 0 {
		t.Fatal("submitted a transfer against an unreadable balance")
	}
	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00 — escalating must never move money", got)
	}
	// And once terminal it stops consuming attempts.
	n, err := r.fwd.RedrivePending(ctx)
	if err != nil {
		t.Fatalf("RedrivePending after escalation: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-drove %d already-failed forwards, want 0", n)
	}
}

// The operator queue is "merchant credited, bank not paid". A pending forward
// that has sat for half an hour is exactly that, and was previously visible
// nowhere.
func TestP2PForward_ListNeedingAttentionIncludesAgedPending(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()
	r.bal.err = errors.New("provider 503")
	if err := r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("999000000025", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	// Fresh: still ordinary in-flight work, not an operator's problem yet.
	rows, err := r.fwd.ListNeedingAttention(ctx)
	if err != nil {
		t.Fatalf("ListNeedingAttention: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("queued %d fresh pending forwards, want 0", len(rows))
	}

	if err := r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).
		Update("created_at", time.Now().Add(-2*p2pForwardStaleAfter)).Error; err != nil {
		t.Fatalf("age the forward: %v", err)
	}
	rows, err = r.fwd.ListNeedingAttention(ctx)
	if err != nil {
		t.Fatalf("ListNeedingAttention: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("queued %d rows, want 1 — an aged pending forward is a credited-but-unpaid merchant", len(rows))
	}
	if rows[0].Status != models.P2PForwardPending || rows[0].PMID != r.pm.ID {
		t.Fatalf("queue row does not identify the unpaid merchant: %+v", rows[0])
	}
}

// ─── currency ────────────────────────────────────────────────────────────────

func TestP2POpenClaims_StampsTheTradesOwnCurrency(t *testing.T) {
	r := newForwardRig(t, "1500.00")

	claims, err := r.src.OpenClaims(context.Background(), "ngn")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want 1", len(claims))
	}
	if claims[0].Currency == "" {
		t.Fatal("claim carries no currency; the matcher holds such a claim's credit instead of matching it")
	}
	if claims[0].Currency != "NGN" {
		t.Fatalf("claim currency = %q, want the trade's own NGN", claims[0].Currency)
	}
}

// Accept is separated from OpenClaims by the whole bind, including the
// manual-assign path where no currency filter runs at all. Crediting a
// merchant's naira ledger out of a cedi transfer books money that never landed
// in that currency.
func TestP2PAccept_RefusesACreditInAnotherCurrency(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	foreign := p2pInboundCredit("999000000026", "1500.00")
	foreign.Currency = "GHS"

	if err := r.src.Accept(context.Background(), r.trade.ID, foreign); err == nil {
		t.Fatal("a GHS credit settled an NGN trade")
	}
	if got := r.availableNGN(t); !ngnEqual(got, "0") {
		t.Fatalf("available_ngn = %q, want nothing credited", got)
	}
	var trade models.P2PTrade
	r.db.First(&trade, "id = ?", r.trade.ID)
	if trade.Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("trade status = %q — foreign money released the buyer's crypto", trade.Status)
	}
	var rows int64
	r.db.Model(&models.P2PSettlementForward{}).Count(&rows)
	if rows != 0 {
		t.Fatalf("%d forward rows, want 0", rows)
	}
}

// ─── configuration guard ─────────────────────────────────────────────────────

// The MCGP sibling returns an error the matcher can log; this one dereferenced
// a nil handle and took the poller down with a panic, which loses the whole
// tick's worth of attribution rather than one source's.
func TestP2POpenClaims_UnwiredSourceErrorsRatherThanPanicking(t *testing.T) {
	src := NewP2PTradeClaimSource(nil, nil, nil, nil)

	claims, err := src.OpenClaims(context.Background(), "NGN")
	if err == nil {
		t.Fatal("an unwired source reported success")
	}
	if claims != nil {
		t.Fatalf("got %d claims from an unwired source", len(claims))
	}
}

// ─── A settle that dies after markPaid ───────────────────────────────────────

// createInboundMatchTables adds the two tables the matcher reads to the trade
// rig, so a test can drive the real poller sequence instead of calling Accept by
// hand. Raw DDL for the same reason every other rig in this package uses it: the
// models carry PostgreSQL-only defaults SQLite rejects.
func createInboundMatchTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE inbound_credits (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			provider_ref TEXT NOT NULL,
			currency TEXT NOT NULL,
			amount_kobo INTEGER NOT NULL,
			fee_kobo INTEGER NOT NULL DEFAULT 0,
			gross_kobo INTEGER NOT NULL,
			sender_raw TEXT NOT NULL,
			sender_bank TEXT,
			sender_name TEXT,
			transaction_date DATETIME NOT NULL,
			seen_at DATETIME NOT NULL,
			status TEXT NOT NULL DEFAULT 'unmatched',
			hold_reason TEXT,
			hold_reason_code TEXT,
			matched_claim_type TEXT,
			matched_claim_id TEXT,
			matched_user_id TEXT,
			matched_at DATETIME,
			match_strength TEXT,
			resolved_by TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE UNIQUE INDEX idx_fwd_rig_inbound_ref ON inbound_credits (provider_ref)`,
		// No users table here: setupTradeDB creates it (testUsersDDL), because
		// opening a trade as the buyer now reads verification_status from it.
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("inbound match ddl: %v\nSQL=%s", err, stmt)
		}
	}
}

// The sequence TestP2PAccept_RetryFinishesASettleThatFailedAfterTheCredit
// cannot see. That test replays Accept by hand; the matcher never would.
//
// settleTrade marks the trade paid before it releases, so a release that fails
// afterwards leaves the trade at `paid` — a status OpenClaims does not offer.
// If Accept reported that as an error the credit would stay `unmatched`, and on
// the next tick the matcher would find nothing asking its amount and hold it as
// "no open claim at this amount". That reason is false, BindManually refuses the
// credit because the claim is closed, and the only actions left to an operator
// are refund and ignore — while PMRecoveryJob is about to release the buyer's
// crypto. Refunding the naira then gives the crypto away for nothing.
func TestP2PMatch_AFailedReleaseDoesNotHoldTheCreditOnAFalseReason(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	createInboundMatchTables(t, r.db)
	ctx := context.Background()

	// The BUYER sends the naira, so it is the buyer's verified name the matcher
	// compares the transfer against.
	if err := r.db.Exec(`INSERT INTO users (id, name, verified_legal_name) VALUES (?, ?, ?)`,
		r.trade.BuyerID, "Display Name", "JOHN DOE").Error; err != nil {
		t.Fatalf("seed buyer: %v", err)
	}
	credit := seedCredit(t, r.db, "999000000030", 150000, "JOHN DOE")

	m := NewInboundMatcher(r.db)
	m.Register(InboundClaimTypeP2PTrade, r.src)

	// The release breaks after markPaid has already committed, leaving exactly
	// the paid-orphan AutoRelease rolls a failed attempt back to.
	r.esc.verifyReleasedErr = errors.New("rpc timeout verifying the release")

	// Tick 1 binds and settles as far as it can get.
	_, _ = m.MatchPending(ctx, 10)

	// The precondition that makes this different from a replayable Accept: the
	// trade is no longer on offer, so nothing will ever re-drive the credit.
	claims, err := r.src.OpenClaims(ctx, "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("precondition: %d claim(s) still offered — the trade never left pending_payment", len(claims))
	}

	// Tick 2: the poller comes back 30 seconds later.
	if _, err := m.MatchPending(ctx, 10); err != nil {
		t.Fatalf("second MatchPending: %v", err)
	}

	got := reloadCredit(t, r.db, credit.ID)
	if got.Status == models.InboundCreditHeld {
		t.Fatalf("credit held as %q — the trade it paid for is settled and awaiting release, so the refund this reason invites gives the crypto away",
			got.HoldReasonCode)
	}
	if got.Status != models.InboundCreditMatched {
		t.Fatalf("credit status = %q, want matched", got.Status)
	}

	// The recoverable shape, and the one PMRecoveryJob already owns: merchant
	// credited, trade at `paid` with no release.
	if bal := r.availableNGN(t); !ngnEqual(bal, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00", bal)
	}
	var trade models.P2PTrade
	r.db.First(&trade, "id = ?", r.trade.ID)
	if trade.Status != models.P2PTradeStatusPaid {
		t.Fatalf("trade status = %q, want paid — the paid-orphan shape RecoverStuckTrades sweeps", trade.Status)
	}
}

// ─── The trade moving under the matcher ──────────────────────────────────────
//
// The claims the matcher binds against are a snapshot. Between OpenClaims
// reading them and Accept settling one, the trade can legitimately move: the
// timeout watchdog cancels expired pending_payment trades on a one-minute
// ticker, so a transfer landing on the deadline is exactly when this happens.
//
// Each test below drives the real poller sequence, because Accept called by
// hand cannot show the window at all.

// tradeMover is a ClaimSource that runs one side effect between the real
// source's OpenClaims and its Accept — the exact window the watchdog moves a
// trade in, and a window nothing else in this file can reach. Everything else
// is the real source, so the matcher, the guard and the settle all run for
// real.
type tradeMover struct {
	inner  ClaimSource
	move   func()
	called bool
}

func (s *tradeMover) OpenClaims(ctx context.Context, currency string) ([]InboundClaim, error) {
	claims, err := s.inner.OpenClaims(ctx, currency)
	if s.move != nil && !s.called {
		s.called = true
		s.move()
	}
	return claims, err
}

func (s *tradeMover) Accept(ctx context.Context, claimID uuid.UUID, credit *models.InboundCredit) error {
	return s.inner.Accept(ctx, claimID, credit)
}

// matchRig seeds the buyer and the credit the matcher will bind, and returns a
// matcher with the P2P source registered.
//
// moveTo, when set, is the status the trade is driven to after the claims have
// been gathered and before Accept sees them.
func (r *fwdRig) matchRig(t *testing.T, ref, moveTo string) (*InboundMatcher, *models.InboundCredit) {
	t.Helper()
	createInboundMatchTables(t, r.db)
	if err := r.db.Exec(`INSERT INTO users (id, name, verified_legal_name) VALUES (?, ?, ?)`,
		r.trade.BuyerID, "Display Name", "JOHN DOE").Error; err != nil {
		t.Fatalf("seed buyer: %v", err)
	}
	credit := seedCredit(t, r.db, ref, 150000, "JOHN DOE")

	var src ClaimSource = r.src
	if moveTo != "" {
		mover := &tradeMover{inner: r.src}
		mover.move = func() {
			if err := r.db.Model(&models.P2PTrade{}).Where("id = ?", r.trade.ID).
				Update("status", moveTo).Error; err != nil {
				t.Errorf("move trade to %s: %v", moveTo, err)
			}
		}
		// A mover that never ran would make every assertion below vacuous.
		t.Cleanup(func() {
			if !mover.called {
				t.Error("the trade was never moved — this test proved nothing")
			}
		})
		src = mover
	}

	m := NewInboundMatcher(r.db)
	m.Register(InboundClaimTypeP2PTrade, src)
	return m, credit
}

func TestP2PMatch_ATradeCancelledMidMatchDoesNotCreditTheMerchant(t *testing.T) {
	// The regression this file was carrying. The watchdog cancels the trade
	// after the matcher gathered it, so the crypto behind it can never be
	// released — and paying the merchant anyway forwards naira to their bank
	// against escrow nobody will ever open, with the credit reading `matched`
	// and nothing anywhere to alert on it.
	r := newForwardRig(t, "1500.00")
	m, credit := r.matchRig(t, "999000000040", models.P2PTradeStatusCancelled)

	if _, err := m.MatchPending(context.Background(), 10); err == nil {
		t.Fatal("MatchPending reported success settling a cancelled trade")
	}

	if bal := r.availableNGN(t); ngnEqual(bal, "1500.00") {
		t.Fatalf("available_ngn = %q — the merchant was paid for a trade that can never release", bal)
	}
	got := reloadCredit(t, r.db, credit.ID)
	if got.Status == models.InboundCreditMatched {
		t.Fatal("credit marked matched — the money is unattributed and nothing will ever say so")
	}
	var count int64
	r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Count(&count)
	if count != 0 {
		t.Fatalf("%d forward row(s) written — the merchant's bank transfer was queued anyway", count)
	}

	// And the operator can see it: the next tick has no claim to offer, so the
	// credit lands in the hold queue rather than sitting silently.
	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("second MatchPending: %v", err)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditHeld {
		t.Fatalf("credit status = %q after the second tick, want held — an unsettleable credit must reach an operator", got.Status)
	}
}

func TestP2PMatch_ADisputedTradeMidMatchDoesNotCreditTheMerchant(t *testing.T) {
	// Same window, the other exit from pending_payment that AutoRelease
	// refuses. A dispute is an operator's decision about who the money belongs
	// to, and settling through it pays the merchant before that is answered.
	r := newForwardRig(t, "1500.00")
	m, credit := r.matchRig(t, "999000000041", models.P2PTradeStatusDisputed)

	if _, err := m.MatchPending(context.Background(), 10); err == nil {
		t.Fatal("MatchPending reported success settling a disputed trade")
	}
	if bal := r.availableNGN(t); ngnEqual(bal, "1500.00") {
		t.Fatalf("available_ngn = %q — the merchant was paid while the trade was under dispute", bal)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status == models.InboundCreditMatched {
		t.Fatal("credit marked matched — a disputed trade was settled silently")
	}
}

func TestP2PMatch_ACancelledTradeIsNotDeferredToARecoveryJobThatSkipsIt(t *testing.T) {
	// Defence in depth, reached through the one door the guard holds open.
	//
	// A first attempt credits the merchant and fails to settle, so a forward
	// row now owns this ref and the guard must re-drive the credit. Between
	// that and the replay the trade is cancelled. Only settleTrade can catch
	// this — and it must, because RecoverStuckTrades selects `status = paid`,
	// so deferring a cancelled trade marks the credit `matched` for a release
	// nothing will ever perform.
	r := newForwardRig(t, "1500.00")
	m, credit := r.matchRig(t, "999000000042", "")
	ctx := context.Background()

	// Tick 1: credit the merchant, then break the release the same way the
	// paid-orphan test does, so markPaid commits and AutoRelease does not.
	r.esc.verifyReleasedErr = errors.New("rpc timeout verifying the release")
	if _, err := m.MatchPending(ctx, 10); err != nil {
		t.Fatalf("first MatchPending: %v", err)
	}
	if !ngnEqual(r.availableNGN(t), "1500.00") {
		t.Fatalf("precondition: available_ngn = %q, want the merchant credited", r.availableNGN(t))
	}

	// The trade is then cancelled out from under the half-finished settle.
	if err := r.db.Model(&models.P2PTrade{}).Where("id = ?", r.trade.ID).
		Update("status", models.P2PTradeStatusCancelled).Error; err != nil {
		t.Fatalf("cancel trade: %v", err)
	}
	if err := r.db.Model(&models.InboundCredit{}).Where("id = ?", credit.ID).
		Update("status", models.InboundCreditUnmatched).Error; err != nil {
		t.Fatalf("reopen credit: %v", err)
	}

	// The replay must refuse rather than defer.
	err := r.src.Accept(ctx, r.trade.ID, reloadCredit(t, r.db, credit.ID))
	if err == nil {
		t.Fatal("Accept deferred a cancelled trade to a recovery job that only sweeps `paid`")
	}
	if !strings.Contains(err.Error(), models.P2PTradeStatusCancelled) {
		t.Fatalf("error %q does not name the status that made it unrecoverable", err)
	}
	if errors.Is(err, errP2PReleaseDeferred) {
		t.Fatalf("error is still the deferral sentinel, so the matcher will mark trade %s settled", r.trade.ID)
	}
}

func TestP2PAccept_ReplayOnADifferentTradeIsStillRefused(t *testing.T) {
	// The guard's exception is scoped to a forward row for THIS ref on THIS
	// trade. A ref that already settled some other trade is not a replay of
	// this one, and letting it through would clear the guard and then hand
	// settleTrade a trade no credit ever paid for.
	r := newForwardRig(t, "1500.00")
	ref := "999000000043"

	// A forward row owning the ref, but belonging to a different trade.
	other := &models.P2PSettlementForward{
		ID: uuid.New(), TradeID: uuid.New(), PMID: r.pm.ID,
		ProviderRef: ref, Currency: "NGN", Amount: "1500.00",
		Status: models.P2PForwardPending, Reference: p2pForwardRefPrefix + uuid.New().String(),
	}
	if err := r.db.Create(other).Error; err != nil {
		t.Fatalf("seed foreign forward: %v", err)
	}
	// This trade is at `paid` — releasable, and nobody has paid for it. A
	// ref-only replay check admits the credit here, claimAndCredit then reads
	// the FOREIGN row back as "already ours" and credits nothing, and the
	// release fires: the buyer gets the crypto, paid for by money that settled
	// somebody else's trade, and Accept reports success.
	if err := r.db.Model(&models.P2PTrade{}).Where("id = ?", r.trade.ID).
		Update("status", models.P2PTradeStatusPaid).Error; err != nil {
		t.Fatalf("advance trade: %v", err)
	}

	err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit(ref, "1500.00"))
	if err == nil {
		t.Fatal("Accept settled this trade off another trade's forward row")
	}
	var trade models.P2PTrade
	r.db.First(&trade, "id = ?", r.trade.ID)
	if trade.Status == models.P2PTradeStatusReleased {
		t.Fatal("the crypto was released against a credit that belongs to a different trade")
	}
	if bal := r.availableNGN(t); ngnEqual(bal, "1500.00") {
		t.Fatalf("available_ngn = %q — one ref credited the merchant twice", bal)
	}
}

// ─── Bounded sweeps ──────────────────────────────────────────────────────────

// seedForwardRow inserts one forward directly, bypassing Accept, so a test can
// build a backlog without building a trade per row.
func seedForwardRow(t *testing.T, db *gorm.DB, pmID uuid.UUID, status string, at time.Time) {
	t.Helper()
	id := uuid.New()
	if err := db.Exec(`INSERT INTO p2p_settlement_forwards
		(id, trade_id, pm_id, provider_ref, currency, amount, status, attempts, reference, submitted_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,0,?,?,?,?)`,
		id, uuid.New(), pmID, "ref-"+id.String(), "NGN", "1500.00", status,
		p2pForwardRefPrefix+id.String(), at, at, at).Error; err != nil {
		t.Fatalf("seed forward row: %v", err)
	}
}

// The three sweeps run on a timer against a table that only ever grows, and two
// of them then do per-row work. Loading the whole backlog on a single tick is
// how one bad afternoon becomes an outage, so each takes a batch and leaves the
// rest for the next tick.
func TestP2PForward_SweepsTakeABatchRatherThanTheWholeTable(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()
	stale := time.Now().Add(-2 * p2pForwardStaleAfter)

	over := p2pForwardSweepBatch + 5
	for i := 0; i < over; i++ {
		seedForwardRow(t, r.db, r.pm.ID, models.P2PForwardPending, stale)
		seedForwardRow(t, r.db, r.pm.ID, models.P2PForwardSubmitted, stale)
	}

	swept, err := r.fwd.SweepUnconfirmed(ctx)
	if err != nil {
		t.Fatalf("SweepUnconfirmed: %v", err)
	}
	if swept != p2pForwardSweepBatch {
		t.Fatalf("SweepUnconfirmed took %d of %d rows, want the %d-row batch", swept, over, p2pForwardSweepBatch)
	}

	queued, err := r.fwd.ListNeedingAttention(ctx)
	if err != nil {
		t.Fatalf("ListNeedingAttention: %v", err)
	}
	if len(queued) != p2pForwardSweepBatch {
		t.Fatalf("ListNeedingAttention returned %d of %d rows, want the %d-row batch", len(queued), over, p2pForwardSweepBatch)
	}

	redrove, err := r.fwd.RedrivePending(ctx)
	if err != nil {
		t.Fatalf("RedrivePending: %v", err)
	}
	if redrove != p2pForwardSweepBatch {
		t.Fatalf("RedrivePending took %d of %d rows, want the %d-row batch", redrove, over, p2pForwardSweepBatch)
	}
}

// Two sweeps can overlap on the same row — RedrivePending reads its whole batch
// up front, and a re-drive still talking to the provider has written nothing
// yet. Both read the same Attempts, so a count computed in Go writes the same
// number twice and the second hold costs nothing. A forward that can never be
// paid then never reaches the ceiling, and so never reaches an operator, which
// is the one thing the ceiling exists to prevent.
func TestP2PForward_OverlappingHoldsBothCountTowardTheCeiling(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()
	r.bal.err = errors.New("provider 503")

	if err := r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("999000000031", "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	read := *r.forward(t)
	if read.Attempts != 1 {
		t.Fatalf("precondition: attempts = %d after Accept's own hold, want 1", read.Attempts)
	}

	// The same read, held twice: what two overlapping re-drives do.
	if err := r.fwd.hold(ctx, &read, "provider 503"); err != nil {
		t.Fatalf("first hold: %v", err)
	}
	if err := r.fwd.hold(ctx, &read, "provider 503"); err != nil {
		t.Fatalf("second hold: %v", err)
	}

	if got := r.forward(t).Attempts; got != 3 {
		t.Fatalf("attempts = %d, want 3 — an overlapping re-drive was free, so the ceiling never arrives", got)
	}
}

// ─── The overlap that pays a merchant twice ──────────────────────────────────
//
// The same overlap the ceiling tests describe has a second, worse outcome. Once
// one of the two attempts SUBMITS, the merchant's money is in flight and the
// other attempt is holding a read that predates it. Anything that attempt then
// writes is a write to somebody else's row — and `failed` on a submitted row
// puts an already-paid merchant into the operator queue as unpaid, which is an
// invitation to pay them by hand a second time. The later disburse webhook then
// arrives for a row that is no longer submitted and cannot debit it.

// submitInFlightForward drives one forward all the way to `submitted` and hands
// back the `pending` read a concurrent attempt would still be holding.
//
// The seeded attempt count is the state a hold that reached the ceiling and
// then failed to write its escalation leaves behind: counted in the database,
// still `pending`, and re-drivable.
func submitInFlightForward(t *testing.T, r *fwdRig, ref string, attempts int) models.P2PSettlementForward {
	t.Helper()
	ctx := context.Background()
	r.bal.err = errors.New("provider 503")
	if err := r.src.Accept(ctx, r.trade.ID, p2pInboundCredit(ref, "1500.00")); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).
		Update("attempts", attempts).Error; err != nil {
		t.Fatalf("seed attempts: %v", err)
	}
	stale := *r.forward(t)
	if stale.Status != models.P2PForwardPending {
		t.Fatalf("precondition: forward status = %q, want pending", stale.Status)
	}

	// The balance recovers and the re-drive submits.
	r.bal.err = nil
	r.fwd.withClock(func() time.Time { return time.Now().Add(2 * p2pForwardRedriveAfter) })
	if _, err := r.fwd.RedrivePending(ctx); err != nil {
		t.Fatalf("RedrivePending: %v", err)
	}
	if got := r.forward(t).Status; got != models.P2PForwardSubmitted {
		t.Fatalf("precondition: forward status = %q, want submitted — the merchant's money must be in flight", got)
	}
	return stale
}

// A forward that has been submitted is not `fail`'s to resolve, whatever the
// caller read before it. Flipping it to `failed` lists a merchant whose money
// is in flight as one nobody paid, and the ledger entry `fail` deliberately
// leaves alone is then the only thing standing between them and a second,
// manual payment.
func TestP2PForward_FailDoesNotResolveAForwardThatMovedUnderIt(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()
	stale := submitInFlightForward(t, r, "999000000032", p2pForwardMaxAttempts)

	if err := r.fwd.fail(ctx, &stale, "held 6 times and still unsendable: provider 503"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	f := r.forward(t)
	if f.Status != models.P2PForwardSubmitted {
		t.Fatalf("forward status = %q, want submitted — a stale caller resolved an in-flight transfer, and the operator queue now invites a second payment", f.Status)
	}
	if f.AlertedAt != nil {
		t.Fatal("alerted that an in-flight transfer was not paid")
	}
	if f.FailureReason != "" {
		t.Fatalf("failure_reason = %q, want empty — a stale caller overwrote a row it does not own", f.FailureReason)
	}
}

// And `hold` must not even try. Its whole premise is that nothing was sent, so
// a later attempt is safe; the moment the row leaves `pending` that premise is
// somebody else's to judge, and the attempt count it would escalate on is a
// number this attempt did not earn.
func TestP2PForward_HoldDoesNotEscalateAForwardItNoLongerOwns(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()
	stale := submitInFlightForward(t, r, "999000000033", p2pForwardMaxAttempts)
	// The read the concurrent attempt holds has caught up on status but not on
	// ownership — so `fail`'s own guard cannot be what saves this one.
	stale.Status = models.P2PForwardSubmitted

	if err := r.fwd.hold(ctx, &stale, "p2p forward: payout balance NGN short"); err != nil {
		t.Fatalf("hold: %v", err)
	}

	f := r.forward(t)
	if f.Status != models.P2PForwardSubmitted {
		t.Fatalf("forward status = %q, want submitted — a hold escalated a transfer that was already in flight", f.Status)
	}
	if f.AlertedAt != nil {
		t.Fatal("alerted that an in-flight transfer was not paid")
	}
	if f.Attempts != p2pForwardMaxAttempts {
		t.Fatalf("attempts = %d, want %d — the hold counted against a row it does not own", f.Attempts, p2pForwardMaxAttempts)
	}
}

// The race end to end, and the reason the two guards above are worth having:
// the disburse webhook must still find the row it was promised.
func TestP2PForward_ConcurrentHoldLeavesAnInFlightTransferConfirmable(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	ctx := context.Background()
	stale := submitInFlightForward(t, r, "999000000034", p2pForwardMaxAttempts)

	// The overlapping attempt finishes its short-balance read and holds.
	if err := r.fwd.hold(ctx, &stale, "p2p forward: payout balance NGN short"); err != nil {
		t.Fatalf("stale hold: %v", err)
	}
	if got := r.forward(t).Status; got != models.P2PForwardSubmitted {
		t.Fatalf("forward status = %q, want submitted", got)
	}

	if err := r.fwd.Confirm(ctx, r.forward(t).Reference, true, ""); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := r.forward(t).Status; got != models.P2PForwardSettled {
		t.Fatalf("forward status = %q after the disburse webhook, want settled — the merchant was paid and nothing recorded it", got)
	}
	if got := r.availableNGN(t); !ngnEqual(got, "0") {
		t.Fatalf("available_ngn = %q, want 0 — a confirmed disbursement must debit the ledger exactly once", got)
	}
}

// ─── Merchant review gate ────────────────────────────────────────────────────
//
// The VA path refuses to auto-settle for a paused merchant or a trade over the
// merchant's caps (HandleInbound steps 3 and 6). The central path must refuse
// the same trades, and must refuse them as a HOLD: the money has arrived and
// is correctly attributed, so it needs an operator, not a retry every tick.

func (r *fwdRig) setMerchant(t *testing.T, field string, value any) {
	t.Helper()
	if err := r.db.Model(&models.PaymentMerchant{}).Where("id = ?", r.pm.ID).
		Update(field, value).Error; err != nil {
		t.Fatalf("set merchant %s: %v", field, err)
	}
}

// assertHeldUnsettled checks the whole "nothing moved" picture, not just the
// credit: a hold that still credited or forwarded would pay the merchant for a
// trade an operator has not yet approved.
func (r *fwdRig) assertHeldUnsettled(t *testing.T, creditID uuid.UUID, wantCode string) {
	t.Helper()
	got := reloadCredit(t, r.db, creditID)
	if got.Status != models.InboundCreditHeld {
		t.Fatalf("credit status = %q, want held", got.Status)
	}
	if got.HoldReasonCode != wantCode {
		t.Fatalf("hold code = %q, want %q", got.HoldReasonCode, wantCode)
	}
	var trade models.P2PTrade
	r.db.First(&trade, "id = ?", r.trade.ID)
	if trade.Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("trade status = %q, want pending_payment — the crypto was released without review", trade.Status)
	}
	if bal := r.availableNGN(t); !ngnEqual(bal, "0") {
		t.Fatalf("available_ngn = %q, want 0 — the merchant was credited for an unreviewed trade", bal)
	}
	var count int64
	r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Count(&count)
	if count != 0 {
		t.Fatalf("%d forward row(s) written — the sweep would pay the merchant anyway", count)
	}
}

func TestP2PMatch_PausedMerchantHoldsTheCreditUnsettled(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.setMerchant(t, "status", models.PaymentMerchantStatusPaused)
	m, credit := r.matchRig(t, "999000000101", "")

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v — a merchant review is a hold, not a retryable failure", err)
	}
	r.assertHeldUnsettled(t, credit.ID, models.InboundHoldMerchantNotActive)
}

func TestP2PMatch_TradeOverPerTradeCapHoldsTheCreditUnsettled(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.setMerchant(t, "per_trade_cap_usd", 0.5) // the rig's trade is $1
	m, credit := r.matchRig(t, "999000000102", "")

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	r.assertHeldUnsettled(t, credit.ID, models.InboundHoldOutsideMerchantCap)
}

func TestP2PMatch_TradeOverDailyCapHoldsTheCreditUnsettled(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.setMerchant(t, "daily_cap_usd", 0.5)
	m, credit := r.matchRig(t, "999000000103", "")

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	r.assertHeldUnsettled(t, credit.ID, models.InboundHoldOutsideMerchantCap)
}

func TestP2PMatch_UnvaluableTradeHoldsRatherThanPassingTheCap(t *testing.T) {
	// A trade we cannot value cannot be shown to be within the cap. The VA
	// path routes it to manual review; passing it here would make the cap
	// something a malformed row walks straight through.
	r := newForwardRig(t, "1500.00")
	r.setMerchant(t, "per_trade_cap_usd", 100)
	if err := r.db.Model(&models.P2PTrade{}).Where("id = ?", r.trade.ID).
		Update("token_amount", "not-a-number").Error; err != nil {
		t.Fatalf("corrupt token_amount: %v", err)
	}
	m, credit := r.matchRig(t, "999000000104", "")

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	r.assertHeldUnsettled(t, credit.ID, models.InboundHoldOutsideMerchantCap)
}

func TestP2PMatch_ManualBindSettlesAHeldMerchantReview(t *testing.T) {
	// The hold is a question for an operator, and assigning the credit is their
	// answer. Re-applying the cap on that path would leave the operator only
	// refund-or-ignore for money they have already decided to accept.
	r := newForwardRig(t, "1500.00")
	r.setMerchant(t, "per_trade_cap_usd", 0.5)
	m, credit := r.matchRig(t, "999000000105", "")
	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}

	if err := m.BindManually(context.Background(), credit.ID, InboundClaimTypeP2PTrade, r.trade.ID, uuid.New()); err != nil {
		t.Fatalf("BindManually: %v", err)
	}

	var trade models.P2PTrade
	r.db.First(&trade, "id = ?", r.trade.ID)
	if trade.Status != models.P2PTradeStatusReleased {
		t.Fatalf("trade status = %q, want released", trade.Status)
	}
	if got := r.availableNGN(t); !ngnEqual(got, "1500.00") {
		t.Fatalf("available_ngn = %q, want 1500.00", got)
	}
	if got := reloadCredit(t, r.db, credit.ID); got.Status != models.InboundCreditMatched {
		t.Fatalf("credit status = %q, want matched", got.Status)
	}
}

func TestP2PAccept_ReplayOfAnAdmittedSettleIsNotReReviewed(t *testing.T) {
	// A credit that already passed the gate and credited the merchant, then
	// failed to finish settling, is re-driven by the next tick. Pausing the
	// merchant in between must not hold it: the ledger is already credited and
	// the forward row already exists, so a hold would strand both.
	r := newForwardRig(t, "1500.00")
	credit := p2pInboundCredit("999000000106", "1500.00")
	if _, _, err := r.src.claimAndCredit(context.Background(), r.trade, r.pm, credit.ProviderRef); err != nil {
		t.Fatalf("seed the half-done settle: %v", err)
	}
	r.setMerchant(t, "status", models.PaymentMerchantStatusPaused)

	if err := r.src.Accept(context.Background(), r.trade.ID, credit); err != nil {
		t.Fatalf("Accept replay: %v", err)
	}
	var trade models.P2PTrade
	r.db.First(&trade, "id = ?", r.trade.ID)
	if trade.Status != models.P2PTradeStatusReleased {
		t.Fatalf("trade status = %q, want released — the replay was blocked by a gate it already passed", trade.Status)
	}
}

func TestP2PMatch_TradeUnderTheTransferFloorIsHeld(t *testing.T) {
	// Flutterwave refuses a transfer under ₦100. Settling a ₦90 trade would
	// release the crypto and then fail the merchant's bank transfer, so it is
	// held before anything moves.
	r := newForwardRig(t, "90.00")
	createInboundMatchTables(t, r.db)
	if err := r.db.Exec(`INSERT INTO users (id, name, verified_legal_name) VALUES (?, ?, ?)`,
		r.trade.BuyerID, "Display Name", "JOHN DOE").Error; err != nil {
		t.Fatalf("seed buyer: %v", err)
	}
	credit := seedCredit(t, r.db, "999000000301", 9000, "JOHN DOE")
	m := NewInboundMatcher(r.db)
	m.Register(InboundClaimTypeP2PTrade, r.src)

	if _, err := m.MatchPending(context.Background(), 10); err != nil {
		t.Fatalf("MatchPending: %v", err)
	}
	r.assertHeldUnsettled(t, credit.ID, models.InboundHoldPayoutBelowMinimum)
}
