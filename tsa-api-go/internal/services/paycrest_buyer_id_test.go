package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// testBuyerID is a well-formed buyer ID for tests that are not about it.
func testBuyerID() *BuyerID {
	return &BuyerID{Type: "bvn", Number: "22212345678", BirthDate: "1990-04-12"}
}

func TestBuyerID_Normalized(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	ok := []BuyerID{
		{Type: "BVN", Number: " 222 1234 5678 ", BirthDate: "1990-04-12"},
		{Type: "nin", Number: "12345678901", BirthDate: "2008-11-04"}, // 18 today
	}
	for _, in := range ok {
		got, err := in.Normalized(now)
		if err != nil {
			t.Fatalf("%+v refused: %v", in.Type, err)
		}
		if got.Type != strings.ToLower(strings.TrimSpace(in.Type)) || len(got.Number) != 11 {
			t.Fatalf("not normalized: type=%q number length %d", got.Type, len(got.Number))
		}
	}

	bad := map[string]BuyerID{
		"Choose BVN or NIN.":             {Type: "passport", Number: "12345678901", BirthDate: "1990-01-01"},
		"Your BVN is 11 digits.":         {Type: "bvn", Number: "1234567890", BirthDate: "1990-01-01"},
		"Your NIN is 11 digits.":         {Type: "nin", Number: "1234567890a", BirthDate: "1990-01-01"},
		"Enter your date of birth.":      {Type: "bvn", Number: "12345678901", BirthDate: "12/04/1990"},
		"You must be 18 or over to buy.": {Type: "bvn", Number: "12345678901", BirthDate: "2008-11-05"},
		"Check your date of birth.":      {Type: "bvn", Number: "12345678901", BirthDate: "1890-01-01"},
	}
	for reason, in := range bad {
		_, err := in.Normalized(now)
		var e *BuyerIDInvalidError
		if !errors.As(err, &e) || e.Reason != reason || !errors.Is(err, ErrBuyerIDInvalid) {
			t.Fatalf("want %q, got %v", reason, err)
		}
	}
}

// A BVN must never reach a log line through a stray %v.
func TestBuyerID_NeverPrints(t *testing.T) {
	id := testBuyerID()
	k := PaycrestPersonKYC{FullName: "ADA OKAFOR", ID: *id}
	req := PaycrestBuyRequest{BuyerID: id}
	for _, s := range []string{
		fmt.Sprintf("%v %+v %s %#v", *id, *id, id, *id),
		fmt.Sprintf("%v %+v", k, k),
		fmt.Sprintf("%+v", req),
	} {
		if strings.Contains(s, id.Number) || strings.Contains(s, id.BirthDate) {
			t.Fatalf("buyer id printed: %s", s)
		}
	}
}

func TestPaycrestError_RedactsIDNumbers(t *testing.T) {
	err := paycrestError("onramp", 400, []byte(`{"status":"error","message":"BVN 22212345678 could not be verified"}`))
	if strings.Contains(err.Error(), "22212345678") {
		t.Fatalf("id number echoed: %v", err)
	}
}

// Asked before any provider call, so the app can collect it and retry.
func TestPaycrestCreateBuy_RequiresTheBuyerID(t *testing.T) {
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newFeeTestService(t, "0.5", pc, "buy", "1390.64")
	req := paycrestGateBuyRequest(seedVerifiedUser(t, svc.db))
	req.BuyerID = nil

	if _, err := svc.CreateBuy(context.Background(), req); !errors.Is(err, ErrBuyerIDRequired) {
		t.Fatalf("CreateBuy err = %v, want ErrBuyerIDRequired", err)
	}
	if pc.onramp.Reference != "" {
		t.Fatal("an order reached Paycrest without the buyer's ID")
	}

	req.BuyerID = &BuyerID{Type: "bvn", Number: "123", BirthDate: "1990-04-12"}
	if _, err := svc.CreateBuy(context.Background(), req); !errors.Is(err, ErrBuyerIDInvalid) {
		t.Fatalf("CreateBuy err = %v, want ErrBuyerIDInvalid", err)
	}
	if pc.onramp.Reference != "" {
		t.Fatal("an order reached Paycrest with a malformed ID")
	}
}

type verifyingOrderer struct {
	*capturingOrderer
	name string
	err  error
}

func (v *verifyingOrderer) VerifyAccount(context.Context, string, string) (string, error) {
	return v.name, v.err
}

// Paycrest refuses a KYC name that differs from the bank's name on the refund
// account. When the bank's name is exactly hers (any order), its spelling is
// used for both; a partial match keeps her verified name on the KYC.
func TestPaycrestCreateBuy_FilesKYCUnderTheRefundAccountsBankName(t *testing.T) {
	for _, tc := range []struct{ bank, wantKYC string }{
		{"OKAFOR ADA", "OKAFOR ADA"},        // exact: the bank's spelling
		{"OKAFOR ADA CHIOMA", "Ada Okafor"}, // partial: her verified name
	} {
		pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
		svc := newFeeTestService(t, "0.5", pc, "buy", "1390.64")
		svc.pc = &verifyingOrderer{capturingOrderer: pc, name: tc.bank}

		if _, err := svc.CreateBuy(context.Background(), paycrestGateBuyRequest(seedVerifiedUser(t, svc.db))); err != nil {
			t.Fatalf("%s: CreateBuy: %v", tc.bank, err)
		}
		got := pc.onramp
		if got.KYC == nil || got.KYC.FullName != tc.wantKYC || got.RefundAccount.AccountName != tc.bank {
			t.Fatalf("%s: kyc set %v, refund %q; want kyc %q, refund %q", tc.bank, got.KYC != nil, got.RefundAccount.AccountName, tc.wantKYC, tc.bank)
		}
		if got.KYC.ID.Number != "22212345678" || got.KYC.ID.Type != "bvn" || got.KYC.ID.BirthDate != "1990-04-12" {
			t.Fatal("the buyer's ID did not reach the order as given")
		}
	}
}

// No name from Paycrest (lookup down, or "OK" for a bank with no lookup):
// the KYC is filed under her verified legal name.
func TestPaycrestCreateBuy_FallsBackToTheVerifiedLegalName(t *testing.T) {
	for _, v := range []verifyingOrderer{{name: ""}, {err: errors.New("503")}} {
		pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
		svc := newFeeTestService(t, "0.5", pc, "buy", "1390.64")
		v.capturingOrderer = pc
		svc.pc = &v

		if _, err := svc.CreateBuy(context.Background(), paycrestGateBuyRequest(seedVerifiedUser(t, svc.db))); err != nil {
			t.Fatalf("CreateBuy: %v", err)
		}
		if pc.onramp.KYC == nil || pc.onramp.KYC.FullName != "Ada Okafor" {
			t.Fatal("kyc not filed under the verified legal name")
		}
		// The app's own name for the account ("Test User") is never trusted.
		if pc.onramp.RefundAccount.AccountName != "Ada Okafor" {
			t.Fatalf("refund name %q, want the verified legal name", pc.onramp.RefundAccount.AccountName)
		}
	}
}

type kycRefusingOrderer struct{ *capturingOrderer }

func (k *kycRefusingOrderer) CreateOnrampOrder(context.Context, PaycrestOnrampRequest) (*PaycrestOrder, error) {
	return nil, paycrestError("onramp", 400,
		[]byte(`{"status":"error","message":"Failed to validate payload","data":{"field":"Source","message":"source.kyc.personDetails.fullName does not match refund account name"}}`))
}

// Paycrest refusing the identity is hers to fix: a distinct error the app
// reopens the form on, never the generic provider failure.
func TestPaycrestCreateBuy_PaycrestRefusingTheIDIsTheBuyers(t *testing.T) {
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newFeeTestService(t, "0.5", pc, "buy", "1390.64")
	svc.pc = &kycRefusingOrderer{pc}

	_, err := svc.CreateBuy(context.Background(), paycrestGateBuyRequest(seedVerifiedUser(t, svc.db)))
	if !errors.Is(err, ErrBuyerIDRejected) {
		t.Fatalf("CreateBuy err = %v, want ErrBuyerIDRejected", err)
	}
	if errors.Is(err, ErrOrderRequestSent) {
		t.Fatal("a 400 was marked as possibly sent")
	}
}

// A refund account in someone else's name would file the buy under that
// person's identity: refused before anything reaches Paycrest.
func TestPaycrestCreateBuy_RefusesARefundAccountInAnotherName(t *testing.T) {
	pc := &capturingOrderer{rate: "1390.64", feePercent: "0.5"}
	svc := newFeeTestService(t, "0.5", pc, "buy", "1390.64")
	svc.pc = &verifyingOrderer{capturingOrderer: pc, name: "MUSA IBRAHIM BELLO"}

	_, err := svc.CreateBuy(context.Background(), paycrestGateBuyRequest(seedVerifiedUser(t, svc.db)))
	if !errors.Is(err, ErrRefundAccountNotBuyers) {
		t.Fatalf("CreateBuy err = %v, want ErrRefundAccountNotBuyers", err)
	}
	if pc.onramp.Reference != "" {
		t.Fatal("an order reached Paycrest under another person's name")
	}
}

func TestPaycrestKYCRefusal_OnlyIdentityProblems(t *testing.T) {
	for msg, want := range map[string]bool{
		"source.kyc is required for on-ramp orders":   true,
		"personDetails.birthDate: must be 18 or over": true,
		"refund account name does not match fullName": true,
		"account name mismatch":                       true,
		"date of birth does not match BVN record":     true,
		"BVN mismatch":                           true,
		"rate does not match the current market": false,
		"token/network mismatch":                 false,
		"Refund institution not supported":       false,
	} {
		if got := paycrestKYCRefusal(&PaycrestRequestError{Op: "onramp", Status: 400, Msg: msg}); got != want {
			t.Errorf("%q: %v, want %v", msg, got, want)
		}
	}
	if paycrestKYCRefusal(&PaycrestRequestError{Op: "onramp", Status: 503, Msg: "kyc service down"}) {
		t.Error("a 503 is not her identity")
	}
}

func TestRedaction_CatchesGluedNumbersAndTheBirthDate(t *testing.T) {
	if got := redactIDNumbers("BVN22212345678 invalid"); strings.Contains(got, "2221234567") {
		t.Fatalf("glued number survived: %s", got)
	}
	id := BuyerID{Number: "22212345678", BirthDate: "1990-04-12"}
	err := redactBuyerID(errors.New("dob 1990-04-12 for 22212345678 rejected"), id)
	if s := err.Error(); strings.Contains(s, "1990-04-12") || strings.Contains(s, "22212345678") {
		t.Fatalf("buyer id survived: %s", s)
	}
	cause := ErrPaycrestOrderRejected
	if !errors.Is(redactBuyerID(fmt.Errorf("%w: 22212345678", cause), id), cause) {
		t.Fatal("redaction lost the cause")
	}
	// A body cut at 300 bytes still has its number blanked.
	long := strings.Repeat("x", 295) + "22212345678"
	if strings.Contains(paycrestError("onramp", 400, []byte(long)).Error(), "22212") {
		t.Fatal("number split by the truncation survived")
	}
}

func TestScrubPaycrestKYC_DropsEveryKYCObject(t *testing.T) {
	in := `{"event":"payment_order.settled","data":{"id":"o1","source":{"currency":"NGN","kyc":{"personDetails":{"idDocument":{"number":"22212345678"}}}},"items":[{"kyc":{"x":1}}]}}`
	out := string(ScrubPaycrestKYC([]byte(in)))
	if strings.Contains(out, "kyc") || strings.Contains(out, "22212345678") {
		t.Fatalf("kyc stored: %s", out)
	}
	if !strings.Contains(out, `"id":"o1"`) || !strings.Contains(out, `"currency":"NGN"`) {
		t.Fatalf("scrub dropped the event: %s", out)
	}
	if got := string(ScrubPaycrestKYC([]byte("not json 22212345678"))); strings.Contains(got, "22212345678") {
		t.Fatalf("non-JSON body kept the number: %s", got)
	}
	// No kyc: stored byte for byte, so it still matches its signature.
	plain := `{"event":"payment_order.settled", "data":{"amountInBaseUnits":123456789012345678901}}`
	if got := string(ScrubPaycrestKYC([]byte(plain))); got != plain {
		t.Fatalf("an untouched body was rewritten: %s", got)
	}
	// With kyc: big numbers survive the re-encode exactly.
	withKYC := `{"data":{"amount":123456789012345678901,"source":{"kyc":{}}}}`
	if got := string(ScrubPaycrestKYC([]byte(withKYC))); !strings.Contains(got, "123456789012345678901") {
		t.Fatalf("number rounded: %s", got)
	}
}

// A NUBAN (10 digits) stays readable in an error; an 11-digit ID does not,
// even glued to other digits.
func TestRedactIDNumbers_ElevenOrMoreDigits(t *testing.T) {
	if got := redactIDNumbers("account 0123456789 invalid, bvn 22212345678, ref 2221234567890"); got != "account 0123456789 invalid, bvn [redacted], ref [redacted]" {
		t.Fatalf("got %q", got)
	}
}
