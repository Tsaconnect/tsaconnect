package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestNairaKoboHelpers(t *testing.T) {
	for _, c := range []struct{ in, wantKobo string }{
		{"500", "50000"},
		{"500.00", "50000"},
		{"500.5", "50050"},
		{"0.01", "1"},
		{"10053.02", "1005302"},
	} {
		got, err := nairaToKobo(c.in)
		if err != nil {
			t.Fatalf("nairaToKobo(%q): %v", c.in, err)
		}
		if got.String() != c.wantKobo {
			t.Fatalf("nairaToKobo(%q) = %s, want %s", c.in, got, c.wantKobo)
		}
	}
	if _, err := nairaToKobo("1.234"); err == nil {
		t.Fatal("3dp accepted")
	}
	if s, err := koboToNairaString("50050"); err != nil || s != "500.50" {
		t.Fatalf("koboToNairaString: %q %v", s, err)
	}
	if s, err := koboToNairaString("7"); err != nil || s != "0.07" {
		t.Fatalf("koboToNairaString small: %q %v", s, err)
	}
	if f, err := koboToNairaFloat("50000"); err != nil || f != 500 {
		t.Fatalf("koboToNairaFloat: %v %v", f, err)
	}
}

// fakeITHooks records routing decisions made by DispatchFlutterwave.
type fakeITHooks struct {
	credited    []string
	vaCredited  []string
	resolved    []string
	creditErr   error
	vaCreditErr error
	vaOK        bool // when true, AutoCreditFromVA succeeds (Spec 1 path)
}

func (f *fakeITHooks) AutoCreditFromInbound(_ context.Context, ref, amount string) error {
	f.credited = append(f.credited, ref+"|"+amount)
	return f.creditErr
}

func (f *fakeITHooks) AutoCreditFromVA(_ context.Context, fwCustomerID, reference, chargeID, amountNGN string) error {
	f.vaCredited = append(f.vaCredited, chargeID+"|"+amountNGN+"|"+fwCustomerID+"|"+reference)
	if f.vaCreditErr != nil {
		return f.vaCreditErr
	}
	if f.vaOK {
		return nil
	}
	// Default: unknown customer → dispatcher falls through to PM inbound.
	return ErrITVANotFound
}

func (f *fakeITHooks) ResolvePayoutFromWebhook(_ context.Context, ref, status string) error {
	f.resolved = append(f.resolved, ref+"|"+status)
	return nil
}

// The dispatcher must route IT-prefixed references to the IT hooks and NEVER
// into the PM path (the zero-value service would nil-panic there — which is
// exactly what this test would surface on a routing bug).
func TestDispatchFlutterwaveRoutesITRefs(t *testing.T) {
	s := NewPMAutomationService(nil, nil, nil, nil, nil, nil, "")
	hooks := &fakeITHooks{}
	s.SetITHooks(hooks)
	// charge.completed now verifies against the /charges API before crediting;
	// wire a verifier that confirms the inbound charge.
	s.SetFlutterwaveChargeVerifier(&fakeChargeGetter{
		ch: FWCharge{ID: "chg_w1", Reference: "IT-ABCDEF12345678", Amount: "500", Currency: "NGN", Status: "succeeded"},
	})

	inbound := []byte(`{"webhook_id":"w1","type":"charge.completed","data":{"id":"chg_w1","reference":"IT-ABCDEF12345678","amount":500,"currency":"NGN","status":"succeeded"}}`)
	if err := s.DispatchFlutterwave(context.Background(), inbound); err != nil {
		t.Fatalf("inbound dispatch: %v", err)
	}
	if len(hooks.credited) != 1 || hooks.credited[0] != "IT-ABCDEF12345678|500.00" {
		t.Fatalf("credit routing wrong: %+v", hooks.credited)
	}

	payout := []byte(`{"webhook_id":"w2","type":"transfer.disburse","data":{"id":"trf_test1","reference":"IT-SELL-3f2b8c9e-1111-2222-3333-444444444444","status":"SUCCESSFUL"}}`)
	if err := s.DispatchFlutterwave(context.Background(), payout); err != nil {
		t.Fatalf("payout dispatch: %v", err)
	}
	if len(hooks.resolved) != 1 || hooks.resolved[0] != "IT-SELL-3f2b8c9e-1111-2222-3333-444444444444|success" {
		t.Fatalf("payout routing wrong: %+v", hooks.resolved)
	}

	// A failed-charge event is non-settling and must not reach either path.
	failed := []byte(`{"webhook_id":"w3","type":"charge.completed","data":{"reference":"IT-XYZ","amount":1,"currency":"NGN","status":"failed"}}`)
	if err := s.DispatchFlutterwave(context.Background(), failed); !errors.Is(err, ErrNonSettlingEvent) {
		t.Fatalf("failed charge: want ErrNonSettlingEvent, got %v", err)
	}
	if len(hooks.credited) != 1 {
		t.Fatalf("failed charge reached credit hook: %+v", hooks.credited)
	}
}

func TestDispatchFlutterwaveRoutesITVARefs(t *testing.T) {
	s := NewPMAutomationService(nil, nil, nil, nil, nil, nil, "")
	hooks := &fakeITHooks{vaOK: true}
	s.SetITHooks(hooks)
	s.SetFlutterwaveChargeVerifier(&fakeChargeGetter{
		ch: FWCharge{ID: "chg_va1", Reference: "ITVA-deadbeef", Amount: "2500", Currency: "NGN", Status: "succeeded"},
	})

	body := []byte(`{"webhook_id":"wva1","type":"charge.completed","data":{"id":"chg_va1","reference":"ITVA-deadbeef","amount":2500,"currency":"NGN","status":"succeeded","customer":{"id":"cus_user1"}}}`)
	if err := s.DispatchFlutterwave(context.Background(), body); err != nil {
		t.Fatalf("ITVA dispatch: %v", err)
	}
	if len(hooks.credited) != 0 {
		t.Fatalf("ITVA must not hit legacy IT- credit: %+v", hooks.credited)
	}
	if len(hooks.vaCredited) != 1 || hooks.vaCredited[0] != "chg_va1|2500.00|cus_user1|ITVA-deadbeef" {
		t.Fatalf("VA credit routing wrong: %+v", hooks.vaCredited)
	}
}

// ─── AutoPayout: rail-aware destination + PayoutRequest construction ──────
//
// AutoPayout operates on an already-debited intent and only calls the payout
// gateway + DB — no live chain client needed — so it's the seam that fully
// exercises the sell payout path end-to-end for this task.

func TestSellPayoutRequest_MobileMoneyRail(t *testing.T) {
	s := newITTestService(t)
	gw := &fakeNombaGateway{payoutID: "pay_mobile_1"}
	s.SetPayoutGateway(gw)
	// A cross-currency payout must be converted before it is sent, so the quote
	// source has to be wired. KES is not a catalog currency, so the allowlist
	// needs it too — this test predates the catalog and exercises rail-field
	// mapping rather than product availability.
	allowQuoteCurrency(t, "KES")
	s.SetRateQuoter(stubRateQuoter{ngnPerDestUnit: 15})
	intent := &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionSell,
		Status: models.ITStatusDebited, McgpAmount: "1000000000000000000",
		NgnKobo: "150000", RateKobo: "1500",
		DestinationCurrency: "KES", RailType: "mobile_money",
		MobileNetwork: "mpesa", MobileCountry: "KE", MobileMsisdn: "254712345678",
	}
	if err := s.db.Create(intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if err := s.AutoPayout(context.Background(), intent); err != nil {
		t.Fatalf("AutoPayout: %v", err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("expected exactly one InitiatePayout call, got %d", len(gw.calls))
	}
	req := gw.calls[0]
	if req.DestinationCurrency != "KES" || req.RailType != "mobile_money" ||
		req.MobileNetwork != "mpesa" || req.MobileCountry != "KE" || req.MobileMsisdn != "254712345678" {
		t.Fatalf("PayoutRequest rail fields = %+v", req)
	}
	if req.BankCode != "" || req.AccountNumber != "" {
		t.Fatalf("mobile-money payout must not carry legacy bank fields: %+v", req)
	}
	// AmountNGN keeps carrying our locked source amount for unmigrated readers.
	if req.AmountNGN != "1500.00" {
		t.Fatalf("AmountNGN = %q, want 1500.00", req.AmountNGN)
	}
	// Amount is what the RECIPIENT gets, in the destination currency: ₦1500 at
	// the stubbed 15 NGN/KES probe = 100 KES. Sending the naira figure here
	// would pay out 1500 KES — the exact ~100x error the conversion exists to
	// prevent, and irrecoverable once the transfer lands.
	if req.Amount != "100.00" {
		t.Fatalf("Amount = %q, want 100.00 (₦1500 converted to KES)", req.Amount)
	}
	if req.SourceCurrency != "NGN" {
		t.Fatalf("SourceCurrency = %q, want NGN (the balance drawn from)", req.SourceCurrency)
	}
	if intent.PayoutID != "pay_mobile_1" {
		t.Fatalf("intent.PayoutID = %q, want pay_mobile_1", intent.PayoutID)
	}
}

// REGRESSION: Flutterwave rejects a transfer reference longer than 42 chars
// ("size must be between 6 and 42"). "IT-SELL-" + a canonical 36-char UUID is
// 44, so live sell payouts 400'd. The reference must stay within the limit and
// still map back to the intent on the transfer webhook.
func TestSellPayoutRef_WithinFlutterwaveLimitAndRoundTrips(t *testing.T) {
	s := newITTestService(t)
	gw := &fakeNombaGateway{payoutID: "pay_ref_1"}
	s.SetPayoutGateway(gw)
	intent := &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionSell,
		Status: models.ITStatusDebited, McgpAmount: "1000000000000000000",
		NgnKobo: "150000", RateKobo: "1500",
		BankCode: "044", AccountNumber: "0123456789", AccountName: "JOHN DOE",
	}
	if err := s.db.Create(intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if err := s.AutoPayout(context.Background(), intent); err != nil {
		t.Fatalf("AutoPayout: %v", err)
	}
	ref := gw.calls[0].Reference
	if len(ref) < 6 || len(ref) > 42 {
		t.Fatalf("reference %q len=%d, Flutterwave requires 6..42", ref, len(ref))
	}
	if !strings.HasPrefix(ref, ITPayoutRefPrefix) {
		t.Fatalf("reference %q lost its routing prefix", ref)
	}
	// The transfer.disburse webhook must resolve this exact ref back to the
	// intent — otherwise the sell never settles.
	got, err := uuid.Parse(strings.TrimPrefix(ref, ITPayoutRefPrefix))
	if err != nil {
		t.Fatalf("reference does not parse back to a UUID: %v", err)
	}
	if got != intent.ID {
		t.Fatalf("reference round-trip = %s, want %s", got, intent.ID)
	}
}

func TestSellPayoutRequest_LegacyBankUnchanged(t *testing.T) {
	s := newITTestService(t)
	gw := &fakeNombaGateway{payoutID: "pay_bank_1"}
	s.SetPayoutGateway(gw)
	intent := &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionSell,
		Status: models.ITStatusDebited, McgpAmount: "1000000000000000000",
		NgnKobo: "150000", RateKobo: "1500",
		BankCode: "044", AccountNumber: "0123456789", AccountName: "JOHN DOE",
	}
	if err := s.db.Create(intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if err := s.AutoPayout(context.Background(), intent); err != nil {
		t.Fatalf("AutoPayout: %v", err)
	}
	req := gw.calls[0]
	if req.BankCode != "044" || req.AccountNumber != "0123456789" || req.AccountName != "JOHN DOE" {
		t.Fatalf("legacy bank PayoutRequest fields = %+v", req)
	}
	if req.DestinationCurrency != "" || req.RailType != "" || req.MobileNetwork != "" ||
		req.MobileCountry != "" || req.MobileMsisdn != "" {
		t.Fatalf("NGN-bank sell must build a byte-for-byte legacy PayoutRequest, got rail fields set: %+v", req)
	}
	if req.AmountNGN != "1500.00" {
		t.Fatalf("AmountNGN = %q, want 1500.00", req.AmountNGN)
	}
}

func TestSellPayoutGuard_MobileMoneyAcceptedWithoutBankFields(t *testing.T) {
	s := newITTestService(t)
	s.SetPayoutGateway(&fakeNombaGateway{payoutID: "pay_ok"})
	// The pre-fix guard checked BankCode/AccountNumber unconditionally, which
	// would wrongly reject this mobile-money-only intent.
	intent := &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionSell,
		Status: models.ITStatusDebited, McgpAmount: "1000000000000000000", NgnKobo: "1000",
		RailType: "mobile_money", MobileNetwork: "mpesa", MobileCountry: "KE", MobileMsisdn: "254700000000",
	}
	if err := s.db.Create(intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if err := s.AutoPayout(context.Background(), intent); err != nil {
		t.Fatalf("mobile-money sell wrongly rejected by the payout-destination guard: %v", err)
	}
}

func TestSellPayoutGuard_MobileMoneyMissingFieldsRejected(t *testing.T) {
	s := newITTestService(t)
	s.SetPayoutGateway(&fakeNombaGateway{payoutID: "pay_bad"})
	intent := &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionSell,
		Status: models.ITStatusDebited, McgpAmount: "1000000000000000000", NgnKobo: "1000",
		RailType: "mobile_money", MobileNetwork: "mpesa", MobileCountry: "KE", // MobileMsisdn missing
	}
	if err := s.db.Create(intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if err := s.AutoPayout(context.Background(), intent); err == nil {
		t.Fatal("expected error for incomplete mobile-money destination")
	}
}

func TestSellPayoutGuard_LegacyBankMissingFieldsRejected(t *testing.T) {
	s := newITTestService(t)
	s.SetPayoutGateway(&fakeNombaGateway{payoutID: "pay_bad"})
	// Empty RailType (legacy) with no bank details — unchanged pre-existing
	// rejection.
	intent := &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionSell,
		Status: models.ITStatusDebited, McgpAmount: "1000000000000000000", NgnKobo: "1000",
	}
	if err := s.db.Create(intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if err := s.AutoPayout(context.Background(), intent); err == nil {
		t.Fatal("expected error for legacy sell with no bank destination")
	}
}

// stubRateQuoter returns a fixed NGN-per-destination-unit rate, shaped like
// Flutterwave's destination-amount-probed /transfers/rates response so
// QuoteWithdrawal's derivation runs unchanged.
type stubRateQuoter struct{ ngnPerDestUnit float64 }

func (q stubRateQuoter) QuoteRate(_ context.Context, sourceCurrency, destCurrency string, destAmount float64) (FWRate, error) {
	return FWRate{
		Rate:        q.ngnPerDestUnit,
		Source:      FWMoney{Currency: sourceCurrency, Amount: q.ngnPerDestUnit * destAmount},
		Destination: FWMoney{Currency: destCurrency, Amount: destAmount},
	}, nil
}

// TestSellPayout_CrossCurrencyRefusesWithoutQuote pins that an unavailable FX
// quote blocks the payout rather than sending the unconverted figure.
//
// The failure mode this guards is silent and irrecoverable: PayoutRequest.Amount
// is what the recipient RECEIVES, so passing the source-currency number through
// would pay ₦1500 as ₵1500 — roughly 100x — and a completed transfer cannot be
// clawed back. Refusing strands the premium until an operator retries, which is
// recoverable. Between an unrecoverable overpayment and a recoverable stall, the
// stall is correct.
func TestSellPayout_CrossCurrencyRefusesWithoutQuote(t *testing.T) {
	s := newITTestService(t)
	gw := &fakeNombaGateway{payoutID: "pay_should_not_happen"}
	s.SetPayoutGateway(gw)
	allowQuoteCurrency(t, "KES")
	// No rate quoter wired — QuoteWithdrawal cannot price the conversion.

	intent := &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionSell,
		Status: models.ITStatusDebited, McgpAmount: "1000000000000000000",
		Currency: "NGN", NgnKobo: "150000", RateKobo: "1500",
		DestinationCurrency: "KES", RailType: "bank",
		BankCode: "044", AccountNumber: "0690000031", AccountName: "Ada Obi",
	}
	if err := s.db.Create(intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	if err := s.AutoPayout(context.Background(), intent); err == nil {
		t.Fatal("AutoPayout succeeded without an FX quote — it must refuse rather than " +
			"send the unconverted source amount as the destination amount")
	}
	if len(gw.calls) != 0 {
		t.Fatalf("gateway was called %d time(s) despite an unpriceable conversion: %+v", len(gw.calls), gw.calls)
	}
}

// TestSellPayout_SameCurrencyNeedsNoQuote confirms the conversion is skipped
// when source and destination match, so the naira path keeps working with no FX
// source wired at all.
func TestSellPayout_SameCurrencyNeedsNoQuote(t *testing.T) {
	s := newITTestService(t)
	gw := &fakeNombaGateway{payoutID: "pay_ngn_1"}
	s.SetPayoutGateway(gw)
	// Deliberately no rate quoter.

	intent := &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: models.ITDirectionSell,
		Status: models.ITStatusDebited, McgpAmount: "1000000000000000000",
		Currency: "NGN", NgnKobo: "150000", RateKobo: "1500",
		RailType: "bank", BankCode: "044", AccountNumber: "0690000031", AccountName: "Ada Obi",
	}
	if err := s.db.Create(intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	if err := s.AutoPayout(context.Background(), intent); err != nil {
		t.Fatalf("AutoPayout: %v", err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("expected 1 payout call, got %d", len(gw.calls))
	}
	if got := gw.calls[0].Amount; got != "1500.00" {
		t.Fatalf("Amount = %q, want 1500.00 unconverted", got)
	}
}
