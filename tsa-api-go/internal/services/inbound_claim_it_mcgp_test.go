package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Claim-source tests for the MCGP buy leg.
//
// Same harness seam as the VA settlement tests: there is no EVM client, so the
// on-chain broadcast inside approveBuyCreditWithRef always fails and the intent
// lands in `failed`. Everything before the broadcast — which order was picked,
// what it was repriced to, and whether a replay can price a second one — is
// observable in the DB, and that is the logic that decides whether a user's
// money is accounted for exactly once.

const testInboundRef = "100004260910120000000000012345" // NIBSS session id shape

func newMCGPClaimSource(t *testing.T) (*ITMCGPClaimSource, *ITService, *models.User) {
	t.Helper()
	s, user, _ := newVATestService(t)
	return NewITMCGPClaimSource(s.db, s), s, user
}

func inboundCreditFor(ref string, grossKobo int64) *models.InboundCredit {
	return &models.InboundCredit{
		ID:          uuid.New(),
		Provider:    models.InboundCreditProviderFlutterwave,
		ProviderRef: ref,
		Currency:    "NGN",
		AmountKobo:  grossKobo,
		GrossKobo:   grossKobo,
		Status:      models.InboundCreditUnmatched,
	}
}

// ─── OpenClaims ────────────────────────────────────────────────────────────

func TestITMCGPClaimSource_OffersAnOpenBuyOrder(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000") // ₦2,000
	if err != nil {
		t.Fatalf("OpenBuyOrder: %v", err)
	}

	claims, err := src.OpenClaims(ctx, "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want 1", len(claims))
	}
	if claims[0].ID != order.ID {
		t.Errorf("claim id = %s, want %s", claims[0].ID, order.ID)
	}
	if claims[0].UserID != user.ID {
		t.Errorf("claim user = %s, want %s", claims[0].UserID, user.ID)
	}
	if claims[0].AmountKobo != 200_000 {
		t.Errorf("claim amount = %d kobo, want 200000", claims[0].AmountKobo)
	}
	if claims[0].Type != InboundClaimTypeITMCGP {
		t.Errorf("claim type = %q", claims[0].Type)
	}
	if claims[0].Currency != "NGN" {
		t.Errorf("claim currency = %q, want NGN — an unset currency holds real credits", claims[0].Currency)
	}
}

// The whole-naira invariant can only be tested on an order that is NOT whole
// naira to begin with. OpenBuyOrder mints at whole naira, so asserting the
// invariant over its output passes whether or not this source rounds at all —
// the fractional row has to be planted directly.
//
// A pre-cutover order for ₦1,234.56 offered as 123456 can never equal the
// 123500 the matcher rounds the credit to, so it holds forever: the buyer paid
// and nothing in the system will ever attribute it.
func TestITMCGPClaimSource_RoundsAPreCutoverOrderRatherThanOfferingFractions(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	fractional := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, WalletAddress: user.WalletAddress,
		Direction: models.ITDirectionBuy, Status: models.ITStatusPendingPayment,
		Currency: "NGN", Asset: models.ITAssetMCGP, NgnKobo: "123456",
		RateKobo: "1", McgpAmount: "1", PaymentRef: mintedRefPrefix + "FRACTION00001",
	}
	if err := s.db.Create(fractional).Error; err != nil {
		t.Fatal(err)
	}

	claims, err := src.OpenClaims(ctx, "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want 1 — a pre-cutover order must stay visible", len(claims))
	}
	if got := claims[0].AmountKobo; got != roundKoboToNaira(123456) {
		t.Fatalf("amountKobo = %d, want %d (the matcher's own rounding); as offered it can never equal the rounded credit",
			got, roundKoboToNaira(123456))
	}
	if claims[0].AmountKobo%100 != 0 {
		t.Fatalf("amountKobo = %d violates the InboundClaim contract's whole-naira invariant", claims[0].AmountKobo)
	}
}

// A claim is only claimable in the currency it was priced in. Offering an
// NGN order against a credit in another currency compares two different units
// of money.
func TestITMCGPClaimSource_OffersNothingInAnotherCurrency(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()
	if _, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000"); err != nil {
		t.Fatal(err)
	}

	claims, err := src.OpenClaims(ctx, "GHS")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("offered %d NGN claims against a GHS credit", len(claims))
	}
}

// The three exclusions AutoCreditFromVA already applies, each of which would
// otherwise let one credit reach an order it must not.
func TestITMCGPClaimSource_ExcludesOrdersThatCannotClaim(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	// Past the 24h window: nothing else expires an MCGP order, so an abandoned
	// one would lie in wait and swallow an unrelated transfer.
	stale, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", stale.ID).
		Update("created_at", time.Now().Add(-buyOrderClaimWindow-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	// Money already landing against it: payment_ref is no longer the minted
	// one, and payment_ref carries a partial unique index.
	settling, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "300000")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", settling.ID).
		Update("payment_ref", "some-provider-charge").Error; err != nil {
		t.Fatal(err)
	}

	// A stablecoin purchase — a different product with its own claim source.
	stable := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, WalletAddress: user.WalletAddress,
		Direction: models.ITDirectionBuy, Status: models.ITStatusPendingPayment,
		Currency: "NGN", Asset: models.ITAssetUSDC, NgnKobo: "400000",
		RateKobo: "1", McgpAmount: "0", PaymentRef: mintedRefPrefix + "STABLE0000001",
	}
	if err := s.db.Create(stable).Error; err != nil {
		t.Fatal(err)
	}

	// The one that should still be offered.
	live, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "500000")
	if err != nil {
		t.Fatal(err)
	}

	claims, err := src.OpenClaims(ctx, "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 || claims[0].ID != live.ID {
		var got []string
		for _, c := range claims {
			got = append(got, c.ID.String())
		}
		t.Fatalf("claims = %v, want only the live order %s", got, live.ID)
	}
}

// ─── Accept ────────────────────────────────────────────────────────────────

func TestITMCGPClaimSource_AcceptAttributesTheSessionIdAndReprices(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if err != nil {
		t.Fatal(err)
	}
	// The payer sent ₦2,500, not the ₦2,000 quoted. Settlement prices what
	// actually arrived, exactly as the VA path does.
	_ = src.Accept(ctx, order.ID, inboundCreditFor(testInboundRef, 250_000))

	var got models.ITIntent
	if err := s.db.First(&got, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.PaymentRef != testInboundRef {
		t.Errorf("payment_ref = %q, want the NIBSS session id", got.PaymentRef)
	}
	if got.NgnKobo != "250000" {
		t.Errorf("ngn_kobo = %q, want 250000 (the amount actually sent)", got.NgnKobo)
	}
	if got.WalletAddress != user.WalletAddress {
		t.Errorf("wallet = %q", got.WalletAddress)
	}
}

// The credited amount is the gross snapped to whole naira, so the user is
// credited the figure they were quoted rather than a kobo either side of it.
func TestITMCGPClaimSource_AcceptRoundsTheGrossToWholeNaira(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if err != nil {
		t.Fatal(err)
	}
	// Gross reconstructed from a netted fee, a kobo out.
	_ = src.Accept(ctx, order.ID, inboundCreditFor(testInboundRef, 199_999))

	var got models.ITIntent
	if err := s.db.First(&got, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.NgnKobo != "200000" {
		t.Errorf("ngn_kobo = %q, want 200000 — the gross rounded to whole naira", got.NgnKobo)
	}
}

// The property that must not regress: one session id can only ever pay for one
// order. A replay against a DIFFERENT order is the shape that would credit the
// same money twice.
func TestITMCGPClaimSource_AcceptRefusesToSpendOneCreditTwice(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	first, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if err != nil {
		t.Fatal(err)
	}
	credit := inboundCreditFor(testInboundRef, 200_000)
	_ = src.Accept(ctx, first.ID, credit)

	// A second order for the same user and amount — opened after the first was
	// already attributed, so it must not be settleable by the same money.
	second := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, WalletAddress: user.WalletAddress,
		Direction: models.ITDirectionBuy, Status: models.ITStatusPendingPayment,
		Currency: "NGN", Asset: models.ITAssetMCGP, NgnKobo: "200000",
		RateKobo: "1", McgpAmount: "1", PaymentRef: mintedRefPrefix + "SECOND0000001",
	}
	if err := s.db.Create(second).Error; err != nil {
		t.Fatal(err)
	}

	if err := src.Accept(ctx, second.ID, credit); err == nil {
		t.Fatal("one session id settled two different orders")
	}
	var reloaded models.ITIntent
	if err := s.db.First(&reloaded, "id = ?", second.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.PaymentRef != mintedRefPrefix+"SECOND0000001" {
		t.Errorf("second order took the session id: payment_ref = %q", reloaded.PaymentRef)
	}

	var carriers int64
	s.db.Model(&models.ITIntent{}).Where("payment_ref = ?", testInboundRef).Count(&carriers)
	if carriers != 1 {
		t.Fatalf("%d intents carry the session id, want 1", carriers)
	}
}

// A crash between Accept and the matcher's status write replays Accept. The
// replay must re-drive the SAME order, not price a second one.
func TestITMCGPClaimSource_AcceptReplayIsIdempotent(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if err != nil {
		t.Fatal(err)
	}
	credit := inboundCreditFor(testInboundRef, 200_000)
	_ = src.Accept(ctx, order.ID, credit)

	var afterFirst models.ITIntent
	if err := s.db.First(&afterFirst, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	_ = src.Accept(ctx, order.ID, credit)

	var afterReplay models.ITIntent
	if err := s.db.First(&afterReplay, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if afterReplay.NgnKobo != afterFirst.NgnKobo || afterReplay.McgpAmount != afterFirst.McgpAmount {
		t.Fatalf("replay repriced the order: ngn %q→%q mcgp %q→%q",
			afterFirst.NgnKobo, afterReplay.NgnKobo, afterFirst.McgpAmount, afterReplay.McgpAmount)
	}
	var intents int64
	s.db.Model(&models.ITIntent{}).Where("user_id = ? AND direction = ?", user.ID, models.ITDirectionBuy).Count(&intents)
	if intents != 1 {
		t.Fatalf("replay created intents: %d exist, want 1", intents)
	}
}

// A credited order is terminal. Replaying its session id is a no-op, never a
// second broadcast.
func TestITMCGPClaimSource_AcceptOnACreditedOrderIsANoOp(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	done := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, WalletAddress: user.WalletAddress,
		Direction: models.ITDirectionBuy, Status: models.ITStatusCredited,
		Currency: "NGN", Asset: models.ITAssetMCGP, NgnKobo: "200000",
		RateKobo: "1", McgpAmount: "1", PaymentRef: testInboundRef,
	}
	if err := s.db.Create(done).Error; err != nil {
		t.Fatal(err)
	}
	if err := src.Accept(ctx, done.ID, inboundCreditFor(testInboundRef, 200_000)); err != nil {
		t.Fatalf("replaying a credited order errored: %v", err)
	}
}

// An intent mid-broadcast has money against it and no credit yet. Reporting
// success would let the matcher mark the credit `matched` with nothing behind
// it, so this must stay retryable.
func TestITMCGPClaimSource_AcceptOnAProcessingOrderStaysRetryable(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	stuck := &models.ITIntent{
		ID: uuid.New(), UserID: user.ID, WalletAddress: user.WalletAddress,
		Direction: models.ITDirectionBuy, Status: models.ITStatusProcessing,
		Currency: "NGN", Asset: models.ITAssetMCGP, NgnKobo: "200000",
		RateKobo: "1", McgpAmount: "1", PaymentRef: testInboundRef,
	}
	if err := s.db.Create(stuck).Error; err != nil {
		t.Fatal(err)
	}
	if err := src.Accept(ctx, stuck.ID, inboundCreditFor(testInboundRef, 200_000)); err == nil {
		t.Fatal("reported success for money that is not credited")
	}
}

// A stale FX feed must not park the order under a ref domain the operator
// recovery cannot reproduce — it must simply retry on the next poll.
func TestITMCGPClaimSource_AcceptRetriesRatherThanParkingOnStaleFx(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if err != nil {
		t.Fatal(err)
	}
	s.fx = stubFx{ngn: 1500, stale: true}

	if err := src.Accept(ctx, order.ID, inboundCreditFor(testInboundRef, 200_000)); err == nil {
		t.Fatal("settled at a stale rate")
	}
	var got models.ITIntent
	if err := s.db.First(&got, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != models.ITStatusPendingPayment {
		t.Errorf("status = %q, want pending_payment so the next poll retries", got.Status)
	}
	if got.PaymentRef != order.PaymentRef {
		t.Errorf("payment_ref = %q — a stale rate must not burn the session id", got.PaymentRef)
	}
}

// The on-chain ref is derived from the money, not the order: it is what stops
// the contract crediting the same transfer twice.
func TestInboundCreditRefIsDerivedFromTheSessionId(t *testing.T) {
	a := itRefKey(itInboundRefDomain, testInboundRef)
	again := itRefKey(itInboundRefDomain, testInboundRef)
	if a != again {
		t.Fatal("the same session id produced two different refs — usedRef could not refuse a replay")
	}
	other := itRefKey(itInboundRefDomain, testInboundRef[:len(testInboundRef)-1]+"6")
	if a == other {
		t.Fatal("two session ids collided on one ref")
	}
	// Domain separation from the VA rail, so a charge id and a session id that
	// happened to be equal could never produce the same on-chain ref.
	if a == itRefKey(itVARefDomain, testInboundRef) {
		t.Fatal("the inbound and VA ref domains collide")
	}
}

// ─── whole-naira orders (design §5.4) ──────────────────────────────────────

// Matching compares a credit's gross, rounded to naira, against the order's
// amount. An order minted at a fraction of a naira could never match the money
// a payer's bank actually lets them send.
func TestOpenBuyOrder_MintsWholeNairaOnly(t *testing.T) {
	s, user, _ := newVATestService(t)
	ctx := context.Background()

	for _, tc := range []struct{ ask, want string }{
		{"200000", "200000"}, // already whole
		{"199937", "199900"}, // rounds down
		{"199967", "200000"}, // rounds up
		{"199950", "200000"}, // half rounds up
	} {
		order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, tc.ask)
		if err != nil {
			t.Fatalf("OpenBuyOrder(%s): %v", tc.ask, err)
		}
		if order.NgnKobo != tc.want {
			t.Errorf("OpenBuyOrder(%s) minted %s kobo, want %s", tc.ask, order.NgnKobo, tc.want)
		}
		if order.NgnKobo != "" {
			if order.NgnKobo[len(order.NgnKobo)-2:] != "00" {
				t.Errorf("OpenBuyOrder(%s) minted %s kobo — not a whole naira", tc.ask, order.NgnKobo)
			}
		}
	}

	// Unparseable input still gets CreateBuyIntent's own rejection rather than
	// a second, differently-worded one.
	if _, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "not-a-number"); err == nil ||
		!strings.Contains(err.Error(), "invalid ngnKobo") {
		t.Errorf("bad amount error = %v, want the usual invalid ngnKobo", err)
	}
}

func TestWholeNairaKobo(t *testing.T) {
	cases := []struct{ in, want string }{
		{"200000", "200000"},
		{"199949", "199900"},
		{"199950", "200000"},
		{"49", "0"},
		{"50", "100"},
		{" 100000 ", "100000"},
		{"not-a-number", "not-a-number"}, // passed through for CreateBuyIntent to reject
	}
	for _, c := range cases {
		if got := wholeNairaKobo(c.in); got != c.want {
			t.Errorf("wholeNairaKobo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ─── currency re-assertion ──────────────────────────────────────────────────

// OpenClaims filters by currency, but Accept is also reached from the
// manual-assign path, where an operator names a claim id by hand and no filter
// runs at all. Settling a naira order out of a cedi transfer compares two
// different units of money and issues MCGP against value that never arrived.
func TestITMCGPClaimSource_AcceptRefusesACreditInAnotherCurrency(t *testing.T) {
	src, s, user := newMCGPClaimSource(t)
	ctx := context.Background()

	order, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "200000")
	if err != nil {
		t.Fatal(err)
	}
	foreign := inboundCreditFor(testInboundRef, 200_000)
	foreign.Currency = "GHS"

	if err := src.Accept(ctx, order.ID, foreign); err == nil {
		t.Fatal("a GHS credit settled an NGN order")
	}
	var got models.ITIntent
	if err := s.db.First(&got, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.PaymentRef != order.PaymentRef {
		t.Errorf("payment_ref = %q — a foreign-currency credit burned the session id", got.PaymentRef)
	}
	if got.Status != models.ITStatusPendingPayment {
		t.Errorf("status = %q, want pending_payment", got.Status)
	}
}
