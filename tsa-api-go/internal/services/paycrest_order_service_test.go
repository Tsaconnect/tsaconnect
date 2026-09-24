package services

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// stubPaycrestOrderer implements PaycrestOrderer. Its methods panic when
// called — the kill-switch gate in CreateBuy/CreateSell must return
// ErrPaycrestDisabled before ever reaching the client, so a test that hits
// these methods has a bug in the gate, not in the stub.
type stubPaycrestOrderer struct{}

func (stubPaycrestOrderer) CreateOnrampOrder(context.Context, PaycrestOnrampRequest) (*PaycrestOrder, error) {
	panic("CreateOnrampOrder should not be reached when the currency's ramp provider is not paycrest")
}

func (stubPaycrestOrderer) CreateOfframpOrder(context.Context, PaycrestOfframpRequest) (*PaycrestOrder, error) {
	panic("CreateOfframpOrder should not be reached when the currency's ramp provider is not paycrest")
}

func (stubPaycrestOrderer) GetOrder(context.Context, string) (*PaycrestOrder, error) {
	panic("GetOrder should not be reached by this test")
}

// newPaycrestOrderTestService builds a PaycrestOrderService against an
// in-memory SQLite DB with both tables it touches before any provider call:
// paycrest_order_records (assertNoOpenOrder) and currencies (the kill-switch
// gate, once wired).
func newPaycrestOrderTestService(t *testing.T) (*PaycrestOrderService, *CurrencyRegistry) {
	t.Helper()
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate paycrest_order_records: %v", err)
	}
	// A buy is money in, so CreateBuy reads the buyer's verification. A test
	// that means to get past that gate seeds a verified buyer (seedVerifiedUser).
	createTestUsersTable(t, db)

	quotes := NewPaycrestQuoteService(stubPaycrestQuoter{}, nil, nil)
	svc := NewPaycrestOrderService(db, stubPaycrestOrderer{}, quotes, nil, "", "")
	if svc == nil {
		t.Fatal("NewPaycrestOrderService returned nil")
	}
	// A working directory, so the destination step (which now runs BEFORE the
	// quote, to avoid spending a throttled rates call on a request a bad
	// destination will refuse) resolves and lets a test reach the quoter.
	// Without it every request would stop at the destination with
	// ErrPaycrestDisabled — indistinguishable from the currency gate's own
	// refusal, which is exactly what the gate tests need to tell apart.
	svc.dir = NewPaycrestDirectory(
		&stubInstitutions{rows: []PaycrestInstitution{
			{Name: "Access Bank", Code: "ABNGNGLA", Type: "bank"},
			{Name: "Equity Bank Kenya", Code: "EQBLKENA", Type: "bank"},
		}},
		&stubBankDir{rows: []NombaBank{{Name: "Access Bank", Code: "044"}}},
	)

	registry := NewCurrencyRegistry(db)
	return svc, registry
}

// stubPaycrestQuoter satisfies PaycrestQuoter. Never called in the disabled
// path, same reasoning as stubPaycrestOrderer.
type stubPaycrestQuoter struct{}

func (stubPaycrestQuoter) Markets(context.Context) (*PaycrestMarkets, error) {
	panic("Markets should not be reached when the currency's ramp provider is not paycrest")
}

func (stubPaycrestQuoter) Rates(context.Context, string, string, float64, string) (*PaycrestRates, error) {
	panic("Rates should not be reached when the currency's ramp provider is not paycrest")
}

func (stubPaycrestQuoter) MarketRate(context.Context, string, string) (float64, error) {
	panic("MarketRate should not be reached when the currency's ramp provider is not paycrest")
}

// TestPaycrestOrderService_NilRegistryIsNoGate proves SetCurrencyRegistry is
// truly optional: without it, an order proceeds past the currency check (and
// fails downstream on the stub client, not on ErrPaycrestDisabled) — so
// existing/other construction sites that never call SetCurrencyRegistry keep
// working unmodified.
func TestPaycrestOrderService_NilRegistryIsNoGate(t *testing.T) {
	svc, _ := newPaycrestOrderTestService(t)
	// Deliberately do not call SetCurrencyRegistry.
	buyer := seedVerifiedUser(t, svc.db)

	// With no gate, the request proceeds past the currency check and reaches
	// the stub client (panic) instead of stopping on ErrPaycrestDisabled.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected the request to reach the stub client (panic) since no registry is wired")
		}
	}()
	_, _ = svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		UserID:        buyer,
		WalletAddress: "0xabc",
		FiatCode:      "KES",
		FiatAmount:    "1000",
		// A real KES institution and a plausible account, so the request
		// clears destination resolution (which now runs first) and reaches the
		// quoter — the panic is what proves the currency gate did not stop it.
		RefundBankCode:    "EQBLKENA",
		RefundAccount:     "01234567890123",
		RefundAccountName: "Test User",
	})
}

// TestPaycrestOrderService_CreateBuy_RefusesWhenNotPaycrest is finding 2: the
// documented kill switch (flip a currency's ramp_provider column) must
// actually stop CreateBuy from opening a new order, not just change what
// GET /it/currencies advertises to app builds that haven't refreshed.
func TestPaycrestOrderService_CreateBuy_RefusesWhenNotPaycrest(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)

	// GHS is payout-enabled but carries no ramp provider — Paycrest does not
	// serve it at all — which is exactly the "not paycrest" case the gate
	// exists for. (KES used to play this role; it is a live Paycrest ramp now.)
	if got := registry.RampProvider("GHS"); got == RampProviderPaycrest {
		t.Fatalf("test fixture invalid: GHS resolves to paycrest (got %q)", got)
	}

	_, err := svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		UserID:            uuid.New(),
		WalletAddress:     "0xabc",
		FiatCode:          "GHS",
		FiatAmount:        "1000",
		RefundBankCode:    "044",
		RefundAccount:     "0123456789",
		RefundAccountName: "Test User",
	})
	if !errors.Is(err, ErrPaycrestDisabled) {
		t.Fatalf("CreateBuy err = %v, want ErrPaycrestDisabled", err)
	}
}

// TestPaycrestOrderService_CreateSell_RefusesWhenNotPaycrest mirrors the buy
// case for the offramp path.
func TestPaycrestOrderService_CreateSell_RefusesWhenNotPaycrest(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)

	_, err := svc.CreateSell(context.Background(), PaycrestSellRequest{
		UserID:        uuid.New(),
		WalletAddress: "0xabc",
		FiatCode:      "GHS",
		FiatAmount:    "1000",
		BankCode:      "044",
		AccountNo:     "0123456789",
		AccountName:   "Test User",
	})
	if !errors.Is(err, ErrPaycrestDisabled) {
		t.Fatalf("CreateSell err = %v, want ErrPaycrestDisabled", err)
	}
}

// TestPaycrestOrderService_CreateBuy_AllowsWhenPaycrest proves the gate does
// not over-refuse: NGN, whose default resolves to paycrest, must pass the
// gate and fail downstream instead (on the stub's panic guard, surfaced as a
// test failure with a clear message, or on a validation error before the
// stub is reached).
func TestPaycrestOrderService_CreateBuy_AllowsWhenPaycrest(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)

	if got := registry.RampProvider("NGN"); got != RampProviderPaycrest {
		t.Fatalf("test fixture invalid: NGN resolves to %q, want paycrest", got)
	}
	buyer := seedVerifiedUser(t, svc.db)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected the request to reach the stub client (panic) once past the currency gate")
		}
	}()
	_, _ = svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		UserID:            buyer,
		WalletAddress:     "0xabc",
		FiatCode:          "NGN",
		FiatAmount:        "1000",
		RefundBankCode:    "044",
		RefundAccount:     "0123456789",
		RefundAccountName: "Test User",
	})
}

// TestPaycrestBankCode_NGN_TranslatesNIBSSToPaycrest is the long-standing
// Nigerian path: the app's picker speaks NIBSS codes, so order creation must
// still translate one to Paycrest's own code before submitting.
func TestPaycrestBankCode_NGN_TranslatesNIBSSToPaycrest(t *testing.T) {
	svc, _ := newPaycrestOrderTestService(t)
	pc := &stubInstitutions{rows: []PaycrestInstitution{{Name: "Access Bank", Code: "ABNGNGLA"}}}
	local := &stubBankDir{rows: []NombaBank{{Name: "Access Bank", Code: "044"}}}
	svc.dir = NewPaycrestDirectory(pc, local)

	got, kind, err := svc.paycrestBankCode(context.Background(), "NGN", "044")
	if err != nil {
		t.Fatalf("paycrestBankCode: %v", err)
	}
	// NIBSS is a bank registry, so every NGN destination takes a NUBAN.
	if kind != PaycrestInstitutionBank {
		t.Errorf("NGN kind = %q, want bank", kind)
	}
	if got != "ABNGNGLA" {
		t.Errorf("got %q, want the NIBSS code translated to Paycrest's own", got)
	}
}

// TestPaycrestBankCode_NonNGN_PassesPaycrestCodeThroughUnchanged is the bug
// this fix closes: GET /paycrest/banks?currency=KES (via
// PaycrestDirectory.Institutions) already hands the app Paycrest's own
// institution codes for a non-NGN currency, not NIBSS ones. Running that code
// back through the NGN-only NIBSS map on order creation would look it up as
// if it were a NIBSS code and always miss — so every non-NGN order would fail
// with "this bank cannot receive payouts through this route" regardless of
// which bank was picked.
func TestPaycrestBankCode_NonNGN_PassesPaycrestCodeThroughUnchanged(t *testing.T) {
	svc, _ := newPaycrestOrderTestService(t)
	pc := &stubInstitutions{rows: []PaycrestInstitution{
		{Name: "Equity Bank Kenya", Code: "EQBLKENA"},
	}}
	// The Nigerian bank list must never be consulted for a non-NGN currency —
	// if it were, this is a regression back to the NIBSS-only lookup.
	local := &stubBankDir{rows: []NombaBank{{Name: "Access Bank", Code: "044"}}}
	svc.dir = NewPaycrestDirectory(pc, local)

	got, kind, err := svc.paycrestBankCode(context.Background(), "KES", "EQBLKENA")
	if err != nil {
		t.Fatalf("paycrestBankCode: %v", err)
	}
	// The kind travels with the code so the caller can check the identifier
	// shape against it.
	if kind != PaycrestInstitutionBank {
		t.Errorf("KES bank kind = %q, want bank", kind)
	}
	if got != "EQBLKENA" {
		t.Errorf("got %q, want the Paycrest code passed through unchanged", got)
	}
	if local.calls != 0 {
		t.Errorf("the Nigerian bank list was consulted %d times for KES; it is irrelevant there", local.calls)
	}
}

// TestPaycrestBankCode_NonNGN_UnknownCodeRefused proves the pass-through does
// not become a rubber stamp: a code Paycrest does not list for this currency
// is still refused, not silently forwarded.
func TestPaycrestBankCode_NonNGN_UnknownCodeRefused(t *testing.T) {
	svc, _ := newPaycrestOrderTestService(t)
	pc := &stubInstitutions{rows: []PaycrestInstitution{{Name: "Equity Bank Kenya", Code: "EQBLKENA"}}}
	svc.dir = NewPaycrestDirectory(pc, &stubBankDir{})

	_, _, err := svc.paycrestBankCode(context.Background(), "KES", "NOT-A-REAL-CODE")
	if !errors.Is(err, ErrPaycrestBankUnsupported) {
		t.Fatalf("err = %v, want ErrPaycrestBankUnsupported", err)
	}
}

// The user types what they will send. Paycrest adds its sender fee ON TOP of
// the submitted amount, so submitting the user's figure directly told someone
// who entered 800 to transfer 804.02 — and the screen then asserted both "you
// pay 800" and "transfer 804.02", which cannot both be true. Dividing first
// puts the fee in the rate instead.
func TestGrossDownForSenderFee(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		feePct    string
		want      string
		applied   bool
	}{
		{"0.5% on 800 round-trips to 800", "800", "0.5", "796.02", true},
		{"0.5% on 5000 round-trips to 5000", "5000", "0.5", "4975.12", true},
		{"1% on 1000", "1000", "1", "990.10", true},
		// Unknown fee: the percent lives in the dashboard profile, not our env.
		// Guessing would put the transfer a few kobo off the amount the provider
		// waits for, and an inexact transfer is what strands a payment.
		{"unknown fee leaves the amount alone", "800", "", "800", false},
		{"unparseable fee leaves the amount alone", "800", "abc", "800", false},
		{"zero fee leaves the amount alone", "800", "0", "800", false},
		// Config typos and ParseFloat's special values must never reach an amount.
		{"NaN fee leaves the amount alone", "800", "NaN", "800", false},
		{"infinite fee leaves the amount alone", "800", "Inf", "800", false},
		{"negative fee leaves the amount alone", "800", "-0.5", "800", false},
		{"implausible fee leaves the amount alone", "800", "50", "800", false},
		{"unparseable amount is passed through", "abc", "0.5", "abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, applied := grossDownForSenderFee(tc.requested, tc.feePct)
			if got != tc.want || applied != tc.applied {
				t.Fatalf("grossDownForSenderFee(%q, %q) = (%q, %v), want (%q, %v)",
					tc.requested, tc.feePct, got, applied, tc.want, tc.applied)
			}
		})
	}
}

// The whole point: whatever we submit, the provider's own arithmetic must land
// back on the number the user typed. This asserts the round trip rather than
// the intermediate value, because the intermediate is only ever a means to it.
func TestGrossDownForSenderFee_RoundTripsToTheUsersNumber(t *testing.T) {
	for _, requested := range []string{"800", "1000", "5000", "137500", "999.99"} {
		for _, pct := range []string{"0.5", "1", "2.5"} {
			submitted, applied := grossDownForSenderFee(requested, pct)
			if !applied {
				t.Fatalf("expected gross-down to apply for %s @ %s%%", requested, pct)
			}
			sub, _ := strconv.ParseFloat(submitted, 64)
			p, _ := strconv.ParseFloat(pct, 64)
			want, _ := strconv.ParseFloat(requested, 64)
			// What Paycrest will compute as amountToTransfer.
			got := sub * (1 + p/100)
			// One kobo of rounding slack; more than that and the user is told to
			// transfer a different number from the one they entered.
			if diff := got - want; diff > 0.01 || diff < -0.01 {
				t.Errorf("%s @ %s%%: submitted %s -> provider asks %.4f, wanted %.2f (%.4f off)",
					requested, pct, submitted, got, want, diff)
			}
		}
	}
}

// ─── Bug: abandoned orders alarming as unmatched deposits ─────────────────
//
// UnmatchedDeposit() reports a fact about Paycrest's response — expired +
// amountPaid=0 — but that signature is IDENTICAL for two very different
// orders: one where the user paid and the protocol lost track of it (a real
// operator alarm), and one the user simply never paid at all (routine).
// mapPaycrestStatus is where the two are told apart, using row.PaymentClaimedAt
// (whether the user tapped "I've sent the money"/"I've sent it") as the only
// signal our side actually has.

// TestMapPaycrestStatus_AbandonedOrderReadsAsOrdinaryExpired is the abandoned
// half: nobody claimed to have paid, so the identical expired+amountPaid=0
// signature must NOT read as unmatched.
func TestMapPaycrestStatus_AbandonedOrderReadsAsOrdinaryExpired(t *testing.T) {
	expired := &PaycrestOrder{Status: PaycrestStatusExpired, AmountPaid: "0"}

	got := mapPaycrestStatus(expired, models.PaycrestDirectionBuy, false /* not claimed */)

	if got != models.PaycrestStatusExpired {
		t.Fatalf("abandoned order mapped to %q, want %q", got, models.PaycrestStatusExpired)
	}
}

// TestMapPaycrestStatus_ClaimedOrderReadsAsUnmatched is the genuine-alarm
// half: the user asserted they paid, so the same signature must still surface
// to the operator queue.
func TestMapPaycrestStatus_ClaimedOrderReadsAsUnmatched(t *testing.T) {
	expired := &PaycrestOrder{Status: PaycrestStatusExpired, AmountPaid: "0"}

	got := mapPaycrestStatus(expired, models.PaycrestDirectionBuy, true /* claimed */)

	if got != models.PaycrestStatusUnmatched {
		t.Fatalf("claimed order mapped to %q, want %q", got, models.PaycrestStatusUnmatched)
	}
}

// TestMapPaycrestStatus_ClaimDoesNotOverrideAGenuinelySettledOrder guards the
// gate against over-firing: claimed=true must only matter for the
// expired+amountPaid=0 signature, never override an order that actually
// settled or is still progressing normally.
func TestMapPaycrestStatus_ClaimDoesNotOverrideAGenuinelySettledOrder(t *testing.T) {
	settled := &PaycrestOrder{Status: PaycrestStatusSettled, AmountPaid: "1000"}
	if got := mapPaycrestStatus(settled, models.PaycrestDirectionBuy, true); got != models.PaycrestStatusSettled {
		t.Fatalf("settled order with a claim mapped to %q, want %q", got, models.PaycrestStatusSettled)
	}
}

// stubGetOrderReturns implements PaycrestOrderer with a fixed GetOrder
// response, for exercising Sync end-to-end. Create* methods are unused by
// these tests and panic like the package's other stubs if reached.
type stubGetOrderReturns struct{ order *PaycrestOrder }

func (s stubGetOrderReturns) CreateOnrampOrder(context.Context, PaycrestOnrampRequest) (*PaycrestOrder, error) {
	panic("CreateOnrampOrder not used by this test")
}

func (s stubGetOrderReturns) CreateOfframpOrder(context.Context, PaycrestOfframpRequest) (*PaycrestOrder, error) {
	panic("CreateOfframpOrder not used by this test")
}

func (s stubGetOrderReturns) GetOrder(context.Context, string) (*PaycrestOrder, error) {
	return s.order, nil
}

// newSyncTestRow inserts a non-terminal order row directly (bypassing
// CreateBuy, which needs a live quote) so Sync has something to operate on.
func newSyncTestRow(t *testing.T, db *gorm.DB, userID uuid.UUID, claimed bool) *models.PaycrestOrderRecord {
	t.Helper()
	row := &models.PaycrestOrderRecord{
		ID:            uuid.New(),
		UserID:        userID,
		Direction:     models.PaycrestDirectionBuy,
		Status:        models.PaycrestStatusAwaitingPayment,
		Reference:     "ref-" + uuid.New().String(),
		PaycrestID:    "pc-" + uuid.New().String(),
		FiatCode:      "NGN",
		FiatAmount:    "1000",
		Token:         "USDT",
		ChainKey:      "bsc",
		Network:       "bnb-smart-chain",
		WalletAddress: "0xabc",
	}
	if claimed {
		now := time.Now()
		row.PaymentClaimedAt = &now
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("failed to seed order row: %v", err)
	}
	return row
}

// TestSync_AbandonedOrderExpiresWithoutAlarming is the end-to-end version of
// the mapPaycrestStatus test: an order nobody claimed to have paid, that
// Paycrest reports expired+amountPaid=0, must land on ordinary "expired" —
// not "unmatched" — and must not carry the operator-alarm failure reason.
func TestSync_AbandonedOrderExpiresWithoutAlarming(t *testing.T) {
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate paycrest_order_records: %v", err)
	}
	remote := &PaycrestOrder{ID: "pc-1", Status: PaycrestStatusExpired, AmountPaid: "0"}
	svc := NewPaycrestOrderService(db, stubGetOrderReturns{order: remote}, NewPaycrestQuoteService(stubPaycrestQuoter{}, nil, nil), nil, "", "")

	userID := uuid.New()
	row := newSyncTestRow(t, db, userID, false /* never claimed */)

	got, err := svc.Sync(context.Background(), row)
	if err != nil {
		t.Fatalf("Sync err = %v", err)
	}
	if got.Status != models.PaycrestStatusExpired {
		t.Fatalf("abandoned order Status = %q, want %q", got.Status, models.PaycrestStatusExpired)
	}
	if got.FailureReason != "" {
		t.Fatalf("abandoned order should not carry an alarm failure reason, got %q", got.FailureReason)
	}
}

// TestSync_ClaimedOrderAlarmsAsUnmatched is the mirror: a user who tapped
// "I've sent the money" before the order expired with amountPaid=0 must still
// reach the unmatched status and its operator-facing failure reason.
func TestSync_ClaimedOrderAlarmsAsUnmatched(t *testing.T) {
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate paycrest_order_records: %v", err)
	}
	remote := &PaycrestOrder{ID: "pc-1", Status: PaycrestStatusExpired, AmountPaid: "0"}
	svc := NewPaycrestOrderService(db, stubGetOrderReturns{order: remote}, NewPaycrestQuoteService(stubPaycrestQuoter{}, nil, nil), nil, "", "")

	userID := uuid.New()
	row := newSyncTestRow(t, db, userID, true /* claimed before expiry */)

	got, err := svc.Sync(context.Background(), row)
	if err != nil {
		t.Fatalf("Sync err = %v", err)
	}
	if got.Status != models.PaycrestStatusUnmatched {
		t.Fatalf("claimed order Status = %q, want %q", got.Status, models.PaycrestStatusUnmatched)
	}
	if got.FailureReason == "" {
		t.Fatal("expected an operator-facing failure reason on an unmatched deposit")
	}
}

// ─── ClaimPayment ───────────────────────────────────────────────────────────

func TestClaimPayment_SetsClaimedAtAndIsIdempotent(t *testing.T) {
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate paycrest_order_records: %v", err)
	}
	svc := NewPaycrestOrderService(db, stubPaycrestOrderer{}, NewPaycrestQuoteService(stubPaycrestQuoter{}, nil, nil), nil, "", "")

	userID := uuid.New()
	row := newSyncTestRow(t, db, userID, false)

	first, err := svc.ClaimPayment(context.Background(), userID, row.ID)
	if err != nil {
		t.Fatalf("ClaimPayment err = %v", err)
	}
	if first.PaymentClaimedAt == nil {
		t.Fatal("expected PaymentClaimedAt to be set")
	}
	if first.Status != models.PaycrestStatusAwaitingPayment {
		t.Fatalf("ClaimPayment must not move Status, got %q", first.Status)
	}
	claimedAt := *first.PaymentClaimedAt

	second, err := svc.ClaimPayment(context.Background(), userID, row.ID)
	if err != nil {
		t.Fatalf("second ClaimPayment err = %v", err)
	}
	if second.PaymentClaimedAt == nil || !second.PaymentClaimedAt.Equal(claimedAt) {
		t.Fatalf("expected the second claim to be a no-op, got %v want %v", second.PaymentClaimedAt, claimedAt)
	}
}

// TestClaimPayment_RefusesAnotherUsersOrder proves the endpoint this backs
// cannot be used to mark someone else's order — it must fail exactly like
// looking up an order that isn't yours.
func TestClaimPayment_RefusesAnotherUsersOrder(t *testing.T) {
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate paycrest_order_records: %v", err)
	}
	svc := NewPaycrestOrderService(db, stubPaycrestOrderer{}, NewPaycrestQuoteService(stubPaycrestQuoter{}, nil, nil), nil, "", "")

	owner := uuid.New()
	row := newSyncTestRow(t, db, owner, false)

	intruder := uuid.New()
	_, err := svc.ClaimPayment(context.Background(), intruder, row.ID)
	if !errors.Is(err, ErrPaycrestOrderNotYours) {
		t.Fatalf("ClaimPayment by a different user: err = %v, want ErrPaycrestOrderNotYours", err)
	}

	// And the real owner's order must be untouched.
	var reloaded models.PaycrestOrderRecord
	if err := db.Where("id = ?", row.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.PaymentClaimedAt != nil {
		t.Fatal("an intruder's claim attempt must not have set PaymentClaimedAt")
	}
}

// ─── Destination shape ─────────────────────────────────────────────────────

// The app asks for the right thing per destination, but the app is not a trust
// boundary. An older build or a direct API caller can aim a bank account number
// at a mobile-money code, and the payout then fails at the provider AFTER the
// on-chain leg has moved — or worse, a short-but-real number pays a stranger.
func TestValidatePayoutIdentifier(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind, fiat string
		id         string
		wantErr    bool
	}{
		{"a phone number for mobile money", PaycrestInstitutionMobileMoney, "UGX", "0772123456", false},
		{"with a country code", PaycrestInstitutionMobileMoney, "UGX", "256772123456", false},
		{"too short to be anyone's number", PaycrestInstitutionMobileMoney, "UGX", "077", true},
		{"a single digit", PaycrestInstitutionMobileMoney, "UGX", "7", true},
		{"letters are not a phone number", PaycrestInstitutionMobileMoney, "UGX", "GB33BUKB2020", true},
		{"an IBAN aimed at mobile money", PaycrestInstitutionMobileMoney, "KES", "GB33BUKB20201555555555", true},
		{"a NUBAN is 10 digits", PaycrestInstitutionBank, "NGN", "0123456789", false},
		{"nine digits is not a NUBAN", PaycrestInstitutionBank, "NGN", "012345678", true},
		{"a phone number aimed at a Nigerian bank", PaycrestInstitutionBank, "NGN", "08031234567", true},
		// Outside NGN we do not know the bank format, and inventing one would
		// reject real accounts.
		{"a Kenyan bank account of unknown shape", PaycrestInstitutionBank, "KES", "01234567890123", false},
		{"an IBAN for a European bank", PaycrestInstitutionBank, "EUR", "GB33BUKB20201555555555", false},
		// An unset kind reads as bank, matching normalizePayoutType.
		{"unset kind is treated as a bank", "", "NGN", "0123456789", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePayoutIdentifier(tc.kind, tc.fiat, tc.id)
			if tc.wantErr && !errors.Is(err, ErrPaycrestAccountShape) {
				t.Errorf("err = %v, want ErrPaycrestAccountShape", err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}

// The check has to run BEFORE an order exists — after the on-chain leg there is
// nothing to correct.
func TestPaycrestOrderService_CreateSell_RefusesAMismatchedIdentifier(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)
	pc := &stubInstitutions{rows: []PaycrestInstitution{
		{Name: "MTN Mobile Money", Code: "MOMOUGPC", Type: "mobile_money"},
	}}
	svc.dir = NewPaycrestDirectory(pc, &stubBankDir{})

	_, err := svc.CreateSell(context.Background(), PaycrestSellRequest{
		UserID:        uuid.New(),
		WalletAddress: "0xabc",
		FiatCode:      "UGX",
		FiatAmount:    "100000",
		BankCode:      "MOMOUGPC",
		AccountNo:     "GB33BUKB20201555555555", // a bank account, aimed at MTN
		AccountName:   "Test User",
	})
	if !errors.Is(err, ErrPaycrestAccountShape) {
		t.Fatalf("CreateSell err = %v, want ErrPaycrestAccountShape", err)
	}

	var count int64
	svc.db.Model(&models.PaycrestOrderRecord{}).Count(&count)
	if count != 0 {
		t.Errorf("%d order rows written; the refusal must happen before an order exists", count)
	}
}

// ─── CreateSellForFiat: a sell that pays an exact naira amount ─────────────

func payoutReq(t *testing.T, r *orderConversionRig) PaycrestSellRequest {
	t.Helper()
	return PaycrestSellRequest{UserID: seedVerifiedUser(t, r.db), WalletAddress: orderTestUserWallet,
		FiatCode: "NGN", FiatAmount: "50000.00", ChainKey: "base", Token: "USDC", BankCode: "044", AccountNo: "0123456789"}
}

func TestCreateSellForFiat_PaysExactNairaToTheGivenBankAtTheConvertedFee(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	dep, err := r.svc.SellDepositFor(context.Background(), "NGN", "50000.00", "base", "USDC", true)
	if err != nil {
		t.Fatal(err)
	}
	order, err := r.svc.CreateSellForFiat(context.Background(), payoutReq(t, r), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.client.lastOfframp.SenderFeePercent != "0.5" || order.FiatAmount != "50000.00" ||
		r.client.lastOfframp.Recipient.AccountIdentifier != "0123456789" || r.client.lastOfframp.AmountToken != BaseUnitsToDecimal(dep, 6) {
		t.Fatalf("offramp %+v order %+v dep %s", r.client.lastOfframp, order, dep)
	}
	// Sized at the rate less a cent of headroom, for the naira owed plus the ₦1
	// drift tolerance, plus the fee-rounding allowance: (50001 / 1492.49) +
	// 0.0002 = 33.5019324…, rounded UP. The payout is never under the naira
	// promised, even when Paycrest pays at a rate that only ROUNDS to 1492.50,
	// rounds its fee against her, and the order rate drifts by up to ₦1's worth.
	if dep.String() != "33501933" {
		t.Fatalf("deposit = %s, want 33501933 ((50001 / 1492.49) + 0.0002, rounded up)", dep)
	}
}

func TestCreateSellForFiat_RefusesAShortfallBeforeCallingPaycrest(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	_, err := r.svc.CreateSellForFiat(context.Background(), payoutReq(t, r), true, big.NewInt(1_000000))
	var short *SellShortfallError
	if !errors.As(err, &short) || !errors.Is(err, ErrSellShortfall) || short.MissingBaseUnits.Sign() <= 0 || r.client.offrampCalls != 0 {
		t.Fatalf("err %v calls %d", err, r.client.offrampCalls)
	}
	if short.MissingBaseUnits.String() != "32501933" {
		t.Fatalf("missing = %s, want deposit 33501933 - cap 1000000", short.MissingBaseUnits)
	}
}

func TestCreateSellForFiat_CancelsAnOrderWhosePayoutCameInShort(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.client.offrampRate = "1400" // the provider's order rate is worse than the venue it was sized at
	req := payoutReq(t, r)
	if _, err := r.svc.CreateSellForFiat(context.Background(), req, true, nil); !errors.Is(err, ErrSellRateMoved) {
		t.Fatalf("err = %v", err)
	}
	var row models.PaycrestOrderRecord
	if err := r.db.Where("user_id = ?", req.UserID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != models.PaycrestStatusCancelled {
		t.Fatalf("order status = %q: a deposit address must never be left live for a short order", row.Status)
	}
}

// A stated naira figure is what the row records, whatever tokens × rate says:
// 34 USDC at 1492.50 is 50745.00, and a pay order owes the merchant 50000.00.
func TestCreateSellForToken_HonoursAStatedFiatAmount(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	req := payoutReq(t, r)
	order, err := r.svc.CreateSellForToken(context.Background(), req, "34", true)
	if err != nil {
		t.Fatal(err)
	}
	if order.FiatAmount != "50000.00" {
		t.Fatalf("fiat = %q, want the stated 50000.00", order.FiatAmount)
	}
}

// ─── I1: the payout is judged at Paycrest's UNROUNDED rate ─────────────────

// paidAtProvider is what Paycrest actually pays for the deposit the last
// offramp asked for: the tokens at the provider's own rate, unrounded, less
// the fee it took. The stored row rate is that rate rounded to the cent, which
// is the number a check must NOT be the only judge of.
func paidAtProvider(t *testing.T, r *orderConversionRig, providerRate string) *big.Rat {
	t.Helper()
	tokens, ok1 := new(big.Rat).SetString(r.client.lastOfframp.AmountToken)
	rate, ok2 := new(big.Rat).SetString(providerRate)
	fee, ok3 := new(big.Rat).SetString(r.client.lastOfframp.SenderFeePercent)
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("unreadable offramp %+v", r.client.lastOfframp)
	}
	net := new(big.Rat).Sub(big.NewRat(1, 1), new(big.Rat).Quo(fee, big.NewRat(100, 1)))
	return new(big.Rat).Mul(tokens, new(big.Rat).Mul(rate, net))
}

// 1499.995 × 0.995 = 1492.495025: the cent-rounded rate is 1492.50, half a
// cent ABOVE what Paycrest pays. At ₦5M that is ~₦17 the merchant never sees.
func TestCreateSellForFiat_ALargeOrderIsSizedForTheUnroundedRate(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.quotes.pc.(*stubQuoter).rates["base|USDC"] = &PaycrestRates{
		Buy: &PaycrestQuote{Rate: "1499.995"}, Sell: &PaycrestQuote{Rate: "1499.995"}}
	r.client.offrampRate = "1499.995"
	req := payoutReq(t, r)
	req.FiatAmount = "5000000.00"
	if _, err := r.svc.CreateSellForFiat(context.Background(), req, true, nil); err != nil {
		t.Fatal(err)
	}
	owed := big.NewRat(5_000_000, 1)
	paid := paidAtProvider(t, r, "1499.995")
	if paid.Cmp(new(big.Rat).Sub(owed, sellPayoutTolerance)) < 0 {
		short, _ := new(big.Rat).Sub(owed, paid).Float64()
		t.Fatalf("Paycrest would pay ₦%.2f short of the ₦5,000,000 owed: the deposit was sized at the cent-rounded rate", short)
	}
}

// The row's rate is rounded to the cent, so the check must assume the worst
// half-cent: 3350.083751 USDC × 1492.50 is exactly ₦5,000,000, yet the same
// tokens at 1492.495 pay ₦16.75 less — far outside the ₦1 tolerance.
func TestSellPayoutCovers_AssumesTheRateWasRoundedUp(t *testing.T) {
	row := &models.PaycrestOrderRecord{TokenAmount: "3350.083751", Rate: "1492.50"}
	if sellPayoutCovers(row, "5000000.00") {
		t.Fatal("a payout that is ₦16.75 short at the unrounded rate passed the check")
	}
}

// ─── I3: a short order is taken off the table even if her request dies ─────

// The request is cancelled the instant the order row is written — after
// Paycrest has an order, before the payout check runs. The cancel and the
// removal of the fundable deposit address must not depend on that context.
func TestCreateSellForFiat_ARateMovedOrderIsCancelledAndUnfundableEvenIfTheRequestDies(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.client.offrampRate = "1400"
	req := payoutReq(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.db.Callback().Create().After("gorm:create").Register("test:cancel_request", func(*gorm.DB) { cancel() }); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.CreateSellForFiat(ctx, req, true, nil); !errors.Is(err, ErrSellRateMoved) {
		t.Fatalf("err = %v, want ErrSellRateMoved", err)
	}
	var row models.PaycrestOrderRecord
	if err := r.db.Where("user_id = ?", req.UserID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != models.PaycrestStatusCancelled {
		t.Fatalf("status = %q: a dead request left a short order fundable", row.Status)
	}
	if row.DepositAddress != "" {
		t.Fatalf("deposit address %q still on a cancelled short order", row.DepositAddress)
	}
}

// A pay payment's order is marked at creation, so the customer's routes can
// mask the merchant's bank on it for good; an ordinary sale's is not.
func TestCreateSellForFiat_RecordsThePaymentAnOrderIsAnAttemptOf(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	pid := uuid.New()
	req := payoutReq(t, r)
	req.PayPaymentID = &pid
	order, err := r.svc.CreateSellForFiat(context.Background(), req, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var row models.PaycrestOrderRecord
	if err := r.db.First(&row, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.PayPaymentID == nil || *row.PayPaymentID != pid {
		t.Fatalf("PayPaymentID = %v", row.PayPaymentID)
	}
	plain := payoutReq(t, r)
	if o2, err := r.svc.CreateSell(context.Background(), plain); err != nil || o2.PayPaymentID != nil {
		t.Fatalf("an ordinary sell is marked as a payment's: %+v %v", o2, err)
	}
}

// ─── a sale that cannot prove nothing fundable was left behind ─────────────

// failOrderWrites makes the given kind of write on paycrest_order_records fail.
func failOrderWrites(t *testing.T, db *gorm.DB, kind string) {
	t.Helper()
	fail := func(tx *gorm.DB) {
		if tx.Statement.Table == "paycrest_order_records" {
			tx.AddError(errors.New("orders table is unwritable"))
		}
	}
	var err error
	switch kind {
	case "update":
		err = db.Callback().Update().Before("gorm:update").Register("test:fail_order_update", fail)
	case "create":
		err = db.Callback().Create().Before("gorm:create").Register("test:fail_order_create", fail)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// The short order could not be cancelled, so it is still awaiting a deposit at
// an address she can see: the caller must be told it may exist, not just that
// the rate moved — a payment paired with it must not be handed to another payer.
func TestCreateSellForFiat_AShortOrderThatCouldNotBeCancelledMayStillExist(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.client.offrampRate = "1400"
	req := payoutReq(t, r)
	failOrderWrites(t, r.db, "update")
	_, err := r.svc.CreateSellForFiat(context.Background(), req, true, nil)
	if !errors.Is(err, ErrSellAttemptMayExist) {
		t.Fatalf("err = %v, want ErrSellAttemptMayExist", err)
	}
	// The ordinary sell routes keep answering rate_moved for it.
	if !errors.Is(err, ErrSellRateMoved) {
		t.Fatalf("err = %v: the ordinary routes' rate_moved mapping is lost", err)
	}
	var row models.PaycrestOrderRecord
	if err := r.db.Where("user_id = ?", req.UserID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != models.PaycrestStatusAwaitingDeposit || row.DepositAddress == "" {
		t.Fatalf("row %+v: the test means an order left open", row)
	}
}

func TestCreateSellForFiat_AnOrderThatCouldNotBeRecordedMayStillExist(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	req := payoutReq(t, r)
	failOrderWrites(t, r.db, "create")
	_, err := r.svc.CreateSellForFiat(context.Background(), req, true, nil)
	if !errors.Is(err, ErrSellAttemptMayExist) || errors.Is(err, ErrSellRateMoved) {
		t.Fatalf("err = %v, want ErrSellAttemptMayExist alone", err)
	}
	if r.client.offrampCalls != 1 {
		t.Fatalf("the test means an order Paycrest opened: %d calls", r.client.offrampCalls)
	}
}

// A cancelled short order is the ordinary refusal, and stays a plain rate-moved.
func TestCreateSellForFiat_ACancelledShortOrderIsNotAnAttemptThatMayExist(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	r.client.offrampRate = "1400"
	_, err := r.svc.CreateSellForFiat(context.Background(), payoutReq(t, r), true, nil)
	if !errors.Is(err, ErrSellRateMoved) || errors.Is(err, ErrSellAttemptMayExist) {
		t.Fatalf("err = %v", err)
	}
}

// ─── S3: the merchant is paid in full, at the edge ─────────────────────────

// 100 USDC at 1500.00 is judged on 99.9998 USDC (the worst fee rounding) at
// 1499.995 (the worst rate that rounds to 1500.00): ₦149,999.20. That covers
// owing exactly ₦149,999.20 and not one kobo more — no tolerance comes out of
// the merchant's naira.
func TestSellPayoutCovers_TheWholeAmountOwedAtTheWorstRounding(t *testing.T) {
	row := &models.PaycrestOrderRecord{TokenAmount: "100", Rate: "1500.00"}
	if !sellPayoutCovers(row, "149999.20") {
		t.Fatal("a payout that covers the amount exactly was refused")
	}
	if sellPayoutCovers(row, "149999.21") {
		t.Fatal("a payout one kobo short passed")
	}
}

// Whatever the amount, a deposit sized by SellDepositFor, opened at a rate up to
// ₦1's worth worse than the venue it was sized at, still pays the merchant the
// whole amount at the worst rounding.
func TestSellDepositFor_CoversTheAmountThroughDriftAndRounding(t *testing.T) {
	r := newOrderServiceWithFakes(t)
	for _, owed := range []string{"1500.00", "50000.00", "149999.00", "5000000.00"} {
		dep, err := r.svc.SellDepositFor(context.Background(), "NGN", owed, "base", "USDC", true)
		if err != nil {
			t.Fatal(err)
		}
		tokens := new(big.Rat).SetFrac(dep, pow10(6))
		// The venue is 1492.50; drift the order rate down by ₦1 over the deposit.
		drift := new(big.Rat).Quo(big.NewRat(1, 1), tokens)
		rate := new(big.Rat).Sub(big.NewRat(149250, 100), drift)
		row := &models.PaycrestOrderRecord{TokenAmount: tokens.FloatString(6), Rate: rate.FloatString(2)}
		if !sellPayoutCovers(row, owed) {
			t.Fatalf("owed %s: deposit %s at %s does not cover it", owed, dep, row.Rate)
		}
	}
}
