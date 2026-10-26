package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// testCentralAccount is the company NUBAN the design sends every payer to.
func testCentralAccount() CentralAccount {
	return CentralAccount{
		AccountNumber: "9078978706",
		BankName:      "Flutterwave MFB",
		AccountName:   "TSA CONNECT LTD",
	}
}

// newCentralStableBuyService is the standard stablecoin-buy rig switched into
// central mode. The user still HAS a virtual account, deliberately: central
// mode must ignore it rather than merely cope with its absence.
func newCentralStableBuyService(t *testing.T) (*ITService, uuid.UUID) {
	t.Helper()
	s, userID := newStableBuyDBService(t)
	s.SetInboundCentral(true, testCentralAccount())
	return s, userID
}

func stableInboundCredit(ref string, grossKobo int64) *models.InboundCredit {
	return &models.InboundCredit{
		ID:          uuid.New(),
		Provider:    models.InboundCreditProviderFlutterwave,
		ProviderRef: ref,
		Currency:    "NGN",
		AmountKobo:  grossKobo,
		GrossKobo:   grossKobo,
		SenderRaw:   "Received Money from GTBANK|ADA OKAFOR",
		SenderName:  "ADA OKAFOR",
	}
}

// ─── Pay-into details ────────────────────────────────────────────────────────

func TestCreateStableBuyIntent_CentralModeReturnsTheCompanyAccount(t *testing.T) {
	s, userID := newCentralStableBuyService(t)

	_, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := testCentralAccount()
	if pay.AccountNumber != want.AccountNumber {
		t.Fatalf("account = %q, want the CENTRAL account %q (got the user's VA?)", pay.AccountNumber, want.AccountNumber)
	}
	if pay.BankName != want.BankName || pay.AccountName != want.AccountName {
		t.Fatalf("bank/name = %q/%q, want %q/%q", pay.BankName, pay.AccountName, want.BankName, want.AccountName)
	}
	if pay.Reference != "" {
		t.Fatalf("reference = %q, want empty — nothing we mint survives a bank transfer into the company account", pay.Reference)
	}
	if pay.AmountNGN == "" {
		t.Fatal("no amount to transfer; the payer's name plus this figure is the whole attribution key")
	}
}

func TestCreateStableBuyIntent_CentralModeMintsWholeNaira(t *testing.T) {
	// Matching compares at naira granularity, so the ask has to be a figure the
	// payer can type into a bank app and reproduce exactly.
	//
	// The quote MUST carry sub-naira kobo. testBuyQuote's fixture is already a
	// whole number of naira, so asserting over it passed whether or not the
	// service rounded at all — the rule went unprotected until this test drove
	// a figure that actually needs rounding.
	s, userID := newCentralStableBuyService(t)

	quote := testBuyQuote()
	quote.FiatMinor = big.NewInt(16502549) // ₦165,025.49
	const wantKobo = "16502500"

	intent, pay, err := s.CreateStableBuyIntent(context.Background(), userID, quote, "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	kobo, ok := ngnToKobo(pay.AmountNGN)
	if !ok {
		t.Fatalf("AmountNGN = %q is not a usable figure", pay.AmountNGN)
	}
	if kobo%100 != 0 {
		t.Fatalf("AmountNGN = %q is not whole naira", pay.AmountNGN)
	}
	if itoa(kobo) != wantKobo {
		t.Fatalf("AmountNGN = %q (%d kobo), want %s — the 49 kobo must be rounded away, "+
			"not carried into a figure no payer can reproduce", pay.AmountNGN, kobo, wantKobo)
	}
	// The intent must be minted at the SAME figure the payer was told, or the
	// claim the matcher offers is not the amount anybody was asked to send.
	if intent.NgnKobo != wantKobo {
		t.Fatalf("intent ngn_kobo = %q but the payer was asked for %q (%d kobo)", intent.NgnKobo, pay.AmountNGN, kobo)
	}
}

func TestCreateStableBuyIntent_CentralModeNeverConsultsTheVirtualAccount(t *testing.T) {
	// ErrStableBuyNoVA has to be genuinely unreachable in central mode, not
	// just unlikely — a "cannot happen" error with a live code path is how a
	// retired feature comes back as a 500 nobody can reproduce. Deleting every
	// VA row proves the lookup is gone rather than merely usually satisfied.
	s, userID := newCentralStableBuyService(t)
	if err := s.db.Exec(`DELETE FROM fiat_virtual_accounts`).Error; err != nil {
		t.Fatalf("clear VAs: %v", err)
	}

	_, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if errors.Is(err, ErrStableBuyNoVA) {
		t.Fatal("central mode still refuses on a missing virtual account")
	}
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pay.AccountNumber != testCentralAccount().AccountNumber {
		t.Fatalf("account = %q, want the central account", pay.AccountNumber)
	}
}

func TestCreateStableBuyIntent_VAModeIsUnchanged(t *testing.T) {
	// The flag defaults off and today's behaviour must survive it untouched.
	s, userID := newStableBuyDBService(t)

	_, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pay.AccountNumber != "1234567890" {
		t.Fatalf("account = %q, want the user's standing VA", pay.AccountNumber)
	}
	if pay.Reference == "" {
		t.Fatal("VA mode must still return the VA reference the charge webhook echoes")
	}
	if pay.AmountNGN != "" {
		t.Fatalf("AmountNGN = %q in VA mode; the charge reference is the attribution key there", pay.AmountNGN)
	}
}

func TestCreateStableBuyIntent_ConfiguredAccountWithoutCentralModeStillUsesTheVA(t *testing.T) {
	// Pre-seeding the account ahead of a cutover must not retire virtual
	// accounts on its own. Mirrors the regression pinned in
	// inbound_central_account_test.go, from the buy path's point of view.
	s, userID := newStableBuyDBService(t)
	s.SetInboundCentral(false, testCentralAccount())

	_, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pay.AccountNumber != "1234567890" {
		t.Fatalf("account = %q, want the user's VA — the flag is still off", pay.AccountNumber)
	}
}

// ─── Rounding ────────────────────────────────────────────────────────────────

// The buy path and the matcher must round identically or a correct payment
// becomes a hold. This asserts they are literally the same function rather than
// two that happen to agree today.
func TestStableBuyAskRoundsExactlyAsTheMatcherDoes(t *testing.T) {
	cases := []int64{150000, 150049, 150050, 150099, 16502500, 16502549, 49, 50, 0}
	for _, kobo := range cases {
		asked, ok := new(big.Int).SetString(wholeNairaKobo(big.NewInt(kobo).String()), 10)
		if !ok {
			t.Fatalf("wholeNairaKobo(%d) did not parse back", kobo)
		}
		if want := RoundKoboToNaira(kobo); asked.Int64() != want {
			t.Errorf("ask rounds %d to %d but the matcher rounds the credit to %d", kobo, asked.Int64(), want)
		}
		if asked.Int64()%100 != 0 {
			t.Errorf("ask for %d is %d, which is not whole naira", kobo, asked.Int64())
		}
	}
}

// ─── OpenClaims ──────────────────────────────────────────────────────────────

func TestITStableOpenClaims_OffersTheOpenPurchase(t *testing.T) {
	s, userID := newCentralStableBuyService(t)
	intent, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	src := NewITStableClaimSource(s)

	claims, err := src.OpenClaims(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want 1", len(claims))
	}
	c := claims[0]
	if c.Type != InboundClaimTypeITStable {
		t.Fatalf("type = %q, want %q", c.Type, InboundClaimTypeITStable)
	}
	if c.ID != intent.ID || c.UserID != userID {
		t.Fatalf("claim identifies %s/%s, want %s/%s", c.ID, c.UserID, intent.ID, userID)
	}
	if c.AmountKobo%100 != 0 {
		t.Fatalf("amountKobo = %d is not whole naira", c.AmountKobo)
	}
	// The claim must be the figure the payer was actually told to send.
	askKobo, _ := ngnToKobo(pay.AmountNGN)
	if c.AmountKobo != askKobo {
		t.Fatalf("claim asks %d kobo but the payer was told %d kobo", c.AmountKobo, askKobo)
	}
}

func TestITStableOpenClaims_IgnoresFundedAndCancelledPurchases(t *testing.T) {
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	src := NewITStableClaimSource(s)

	// Fund it. A purchase that has been paid for cannot absorb more money.
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).
		Update("status", models.ITStatusPaymentReceived).Error; err != nil {
		t.Fatalf("advance: %v", err)
	}
	claims, err := src.OpenClaims(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("got %d claims, want 0 — a funded purchase is not awaiting payment", len(claims))
	}
}

func TestITStableOpenClaims_RoundsAPreCutoverIntentRatherThanDroppingIt(t *testing.T) {
	// An intent created before the cutover was minted at whatever the buyer
	// asked for, down to the kobo. It still has to be offered — somebody may
	// have paid for it — and it has to be rounded with the matcher's own helper
	// so both sides land on the same figure.
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).
		Update("ngn_kobo", "150050").Error; err != nil {
		t.Fatalf("backdate amount: %v", err)
	}
	src := NewITStableClaimSource(s)

	claims, err := src.OpenClaims(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want 1 — a pre-cutover purchase must stay visible", len(claims))
	}
	if got := claims[0].AmountKobo; got != RoundKoboToNaira(150050) {
		t.Fatalf("amountKobo = %d, want %d (the matcher's own rounding)", got, RoundKoboToNaira(150050))
	}
	if claims[0].AmountKobo%100 != 0 {
		t.Fatalf("amountKobo = %d violates the InboundClaim contract's whole-naira invariant", claims[0].AmountKobo)
	}
}

// ─── Accept ──────────────────────────────────────────────────────────────────

func TestITStableAccept_FundsThePurchaseKeyedOnTheSessionID(t *testing.T) {
	// The whole point of the change: the attribution key is the NIBSS session
	// id, not a Flutterwave charge id, and everything downstream reads it from
	// payment_ref without caring which it is.
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	src := NewITStableClaimSource(s)

	var open models.ITIntent
	s.db.First(&open, "id = ?", intent.ID)

	if err := src.Accept(context.Background(), intent.ID,
		stableInboundCredit("100004250250000000000000000001", mustInt64(t, open.NgnKobo))); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %q, want payment_received", got.Status)
	}
	if got.PaymentRef != "100004250250000000000000000001" {
		t.Fatalf("payment_ref = %q, want the NIBSS session id", got.PaymentRef)
	}
	if !got.Repriced {
		t.Fatal("intent was claimed but never priced at the live rate")
	}
}

func TestITStableAccept_IsIdempotentOnProviderRef(t *testing.T) {
	// The poller re-reads the same statement row every 30 seconds, so a replay
	// is the ordinary case. It must never fund a second time.
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	src := NewITStableClaimSource(s)

	var open models.ITIntent
	s.db.First(&open, "id = ?", intent.ID)
	credit := stableInboundCredit("100004250250000000000000000002", mustInt64(t, open.NgnKobo))

	if err := src.Accept(context.Background(), intent.ID, credit); err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	var first models.ITIntent
	s.db.First(&first, "id = ?", intent.ID)

	if err := src.Accept(context.Background(), intent.ID, credit); err != nil {
		t.Fatalf("replayed Accept: %v", err)
	}
	var second models.ITIntent
	s.db.First(&second, "id = ?", intent.ID)

	if second.AssetAmount != first.AssetAmount {
		t.Fatalf("replay re-priced the intent: %q → %q", first.AssetAmount, second.AssetAmount)
	}
	if second.ReceivedAmount != first.ReceivedAmount {
		t.Fatalf("replay changed what was received: %q → %q", first.ReceivedAmount, second.ReceivedAmount)
	}
	var count int64
	s.db.Model(&models.ITIntent{}).Where("payment_ref = ?", credit.ProviderRef).Count(&count)
	if count != 1 {
		t.Fatalf("%d intents carry the ref, want 1", count)
	}
}

func TestITStableAccept_SettlesOnGrossNotNet(t *testing.T) {
	// When the provider nets its funding charge out of the credit, AmountKobo
	// is short of what the buyer actually sent. Settling on the net would
	// quietly deliver every buyer less stablecoin than they paid for.
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	src := NewITStableClaimSource(s)

	var open models.ITIntent
	s.db.First(&open, "id = ?", intent.ID)
	gross := mustInt64(t, open.NgnKobo)

	credit := stableInboundCredit("100004250250000000000000000003", gross)
	credit.FeeKobo = 2310            // the 1.40% funding charge, netted out
	credit.AmountKobo = gross - 2310 // what the statement row actually shows

	if err := src.Accept(context.Background(), intent.ID, credit); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.ReceivedAmount != open.NgnKobo {
		t.Fatalf("received = %q, want the GROSS %q — the buyer was shorted the provider fee", got.ReceivedAmount, open.NgnKobo)
	}
}

func TestITStableAccept_SettlesOnTheFigureTheMatcherMatched(t *testing.T) {
	// The bind was justified by RoundKoboToNaira(GrossKobo), never by the raw
	// gross — that is the only figure the matcher ever compared with the ask.
	// Settling on the raw one delivers stablecoin for an amount nobody checked,
	// up to 99 kobo away from the one on the receipt, and the MCGP sibling has
	// rounded here for exactly that reason since it was written.
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	src := NewITStableClaimSource(s)

	var open models.ITIntent
	s.db.First(&open, "id = ?", intent.ID)
	ask := mustInt64(t, open.NgnKobo)

	// A gross the provider's fee reconstruction landed 49 kobo over. The matcher
	// still binds it, because it rounds before it compares.
	credit := stableInboundCredit("100004250250000000000000000009", ask+49)
	if RoundKoboToNaira(credit.GrossKobo) != ask {
		t.Fatalf("precondition: %d does not round onto the ask %d", credit.GrossKobo, ask)
	}

	if err := src.Accept(context.Background(), intent.ID, credit); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.ReceivedAmount != open.NgnKobo {
		t.Fatalf("received = %q, want %q — settled on a figure the matcher never compared against the ask",
			got.ReceivedAmount, open.NgnKobo)
	}
}

func TestITStableAccept_DoesNotParkOnAnUnusableRate(t *testing.T) {
	// The webhook path parks a purchase it cannot price, because a charge event
	// is not redelivered forever and nothing else would come back for it. An
	// inbound credit is the opposite: it has a durable inbound_credits row and
	// the poller re-reads it every 30 seconds. Parking here would convert a
	// transient rate outage into a manual operator recovery for no gain, and
	// leave the buyer looking at an intent only a human can unstick.
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.SetRateQuoter(&buyRateQuoter{err: errors.New("provider down")})
	src := NewITStableClaimSource(s)

	var open models.ITIntent
	s.db.First(&open, "id = ?", intent.ID)
	credit := stableInboundCredit("100004250250000000000000000005", mustInt64(t, open.NgnKobo))

	if err := src.Accept(context.Background(), intent.ID, credit); err == nil {
		t.Fatal("Accept reported success on a purchase it could not price")
	}

	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status == models.ITStatusBuyAwaitingRate {
		t.Fatal("intent was parked for an operator; the poller would have retried in 30s")
	}
	if got.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %q, want pending_payment — nothing should have been written", got.Status)
	}
	if got.PaymentRef != "" {
		t.Fatalf("payment_ref = %q, want empty — an unpriced attempt must claim nothing", got.PaymentRef)
	}

	// And the retry settles it once the rate source recovers, with no operator.
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	if err := src.Accept(context.Background(), intent.ID, credit); err != nil {
		t.Fatalf("retry Accept: %v", err)
	}
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPaymentReceived || !got.Repriced {
		t.Fatalf("retry did not settle: status=%q repriced=%v", got.Status, got.Repriced)
	}
}

func TestITStableAccept_RefusesAPurchaseThatIsNoLongerOpen(t *testing.T) {
	// Advanced between the matcher's read and ours, and not by this credit.
	// Refusing lets the matcher hold the money for an operator rather than
	// attributing it to an already-funded purchase.
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).
		Updates(map[string]any{"status": models.ITStatusPaymentReceived, "payment_ref": "someone_elses_ref"}).Error; err != nil {
		t.Fatalf("advance: %v", err)
	}
	src := NewITStableClaimSource(s)

	err := src.Accept(context.Background(), intent.ID, stableInboundCredit("100004250250000000000000000004", 165025))
	if !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState", err)
	}
}

func TestITStableAccept_RefusesACreditWithNoRef(t *testing.T) {
	// Without the session id there is no idempotency key, so accepting would
	// mean a purchase that can be funded again on the next poll tick.
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	src := NewITStableClaimSource(s)

	credit := stableInboundCredit("", 165025)
	if err := src.Accept(context.Background(), intent.ID, credit); err == nil {
		t.Fatal("accepted a credit with no provider ref")
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %q, want the purchase untouched", got.Status)
	}
}

// mustInt64 parses a stored kobo string, failing the test rather than the
// assertion when it is unusable.
func mustInt64(t *testing.T, s string) int64 {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("unusable kobo value %q", s)
	}
	return v.Int64()
}

// itoa renders a kobo count the way the intent stores it.
func itoa(v int64) string { return big.NewInt(v).String() }

// ─── currency ────────────────────────────────────────────────────────────────

// The matcher asserts a candidate's OWN currency against the credit's and
// treats an empty one as a source bug, holding the credit rather than letting
// it match. A source that forgets the field therefore strands every payment it
// could have settled.
func TestITStableOpenClaims_StampsTheClaimsOwnCurrency(t *testing.T) {
	s, userID := newCentralStableBuyService(t)
	if _, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM); err != nil {
		t.Fatalf("create: %v", err)
	}
	src := NewITStableClaimSource(s)

	// Lower-case on purpose: the argument is normalised on the way in, and the
	// claim must carry the normalised code rather than whatever was passed.
	claims, err := src.OpenClaims(context.Background(), "ngn")
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
		t.Fatalf("claim currency = %q, want NGN", claims[0].Currency)
	}
}

// Accept is reached from the manual-assign path with no currency filter in
// front of it, so the filtering inside OpenClaims is not the guard it looks
// like.
func TestITStableAccept_RefusesACreditInAnotherCurrency(t *testing.T) {
	s, userID := newCentralStableBuyService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	src := NewITStableClaimSource(s)

	var open models.ITIntent
	s.db.First(&open, "id = ?", intent.ID)
	foreign := stableInboundCredit("100004250250000000000000000099", mustInt64(t, open.NgnKobo))
	foreign.Currency = "GHS"

	if err := src.Accept(context.Background(), intent.ID, foreign); err == nil {
		t.Fatal("a GHS credit funded an NGN purchase")
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %q, want pending_payment — foreign money funded the purchase", got.Status)
	}
	if got.PaymentRef == "100004250250000000000000000099" {
		t.Fatal("the foreign credit's session id was attributed to an NGN purchase")
	}
}

func TestITStableLateClaims_OffersOnlyPurchasesPastTheWindow(t *testing.T) {
	s, userID := newCentralStableBuyService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	src := NewITStableClaimSource(s)

	late, err := src.LateClaims(context.Background(), "NGN")
	if err != nil || len(late) != 0 {
		t.Fatalf("fresh purchase: late = %d, err = %v; want none", len(late), err)
	}

	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).
		Update("created_at", time.Now().Add(-stableBuyClaimWindow-time.Hour)).Error; err != nil {
		t.Fatalf("age: %v", err)
	}
	open, _ := src.OpenClaims(context.Background(), "NGN")
	late, err = src.LateClaims(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("LateClaims: %v", err)
	}
	if len(open) != 0 || len(late) != 1 || late[0].ID != intent.ID || !late[0].PastWindow {
		t.Fatalf("open = %d, late = %+v; want the matcher blind to it and the admin view seeing it", len(open), late)
	}

	// A hand assign still funds it: Accept checks status, not age.
	var row models.ITIntent
	s.db.First(&row, "id = ?", intent.ID)
	if err := src.Accept(context.Background(), intent.ID,
		stableInboundCredit("100004250250000000000000000002", mustInt64(t, row.NgnKobo))); err != nil {
		t.Fatalf("Accept on a late purchase: %v", err)
	}
	s.db.First(&row, "id = ?", intent.ID)
	if row.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %q, want payment_received", row.Status)
	}
}
