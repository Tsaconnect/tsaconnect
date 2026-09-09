package services

import (
	"context"
	"errors"
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

	// With no gate, the request proceeds past the currency check and reaches
	// the stub client (panic) instead of stopping on ErrPaycrestDisabled.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected the request to reach the stub client (panic) since no registry is wired")
		}
	}()
	_, _ = svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		UserID:        uuid.New(),
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

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected the request to reach the stub client (panic) once past the currency gate")
		}
	}()
	_, _ = svc.CreateBuy(context.Background(), PaycrestBuyRequest{
		UserID:            uuid.New(),
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
