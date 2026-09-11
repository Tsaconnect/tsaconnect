package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The payer instruction is the mitigation the partial-name-match risk was
// accepted against: a credit can bind to a stranger whose verified name is a
// token-subset of the sender's, and the only reason that is tolerable is that
// payers were told to send from their own account. A pay-into screen that omits
// it is that decision quietly unmade.
func TestBuy_CentralMode_TellsThePayerToUseTheirOwnAccount(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}

	data := itBuyData(t, doITBuy(t, h, user, "500000"))

	payTo, _ := data["payTo"].(map[string]any)
	if payTo == nil {
		t.Fatalf("payTo missing; body=%v", data)
	}
	want := testCentralAccount().PayerInstruction()
	if payTo["payerInstruction"] != want {
		t.Fatalf("payTo.payerInstruction = %v, want %q", payTo["payerInstruction"], want)
	}
}

// A virtual account is already in the payer's own name, so the instruction is
// wrong there in every clause — it would have them looking for a problem that
// does not exist and doubting an account that is theirs.
func TestBuy_VAMode_CarriesNoPayerInstruction(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(false, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	seedNGNVA(t, db, user.ID)

	data := itBuyData(t, doITBuy(t, h, user, "500000"))

	payTo, _ := data["payTo"].(map[string]any)
	if payTo == nil {
		t.Fatalf("payTo missing; body=%v", data)
	}
	if _, present := payTo["payerInstruction"]; present {
		t.Fatalf("payTo carries payerInstruction in VA mode: %v", payTo)
	}
}

func TestGetTrade_PMPayTo_CentralMode_CarriesThePayerInstruction(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, true))
	tradeID := seedPMTrade(t, r, "JOHN DOE")

	payTo := pmPayToOf(t, r, "/api/p2p/trades/"+tradeID.String())

	want := testCentralAccount().PayerInstruction()
	if payTo["payerInstruction"] != want {
		t.Fatalf("pmPayTo.payerInstruction = %v, want %q", payTo["payerInstruction"], want)
	}
}

// Pointed at a merchant's own account there is no name matcher in the loop —
// the merchant confirms the transfer by hand — so the instruction would be
// describing a mechanism that is not running.
func TestGetTrade_PMPayTo_MerchantAccount_CarriesNoPayerInstruction(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, false))
	tradeID := seedPMTrade(t, r, "JOHN DOE")

	payTo := pmPayToOf(t, r, "/api/p2p/trades/"+tradeID.String())

	if _, present := payTo["payerInstruction"]; present {
		t.Fatalf("pmPayTo carries payerInstruction for a merchant's own account: %v", payTo)
	}
}

// Three products, one wording. Told different things about the same company
// account, a payer decides one of the screens is lying and stops.
//
// The two surfaces reachable from this package are compared to each other
// directly; the third (stablecoin buys) is pinned to the same source in
// services/payer_instruction_test.go, which also holds the wording itself
// spelled out in full.
func TestPayerInstruction_IdenticalAcrossProducts(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	mcgp, _ := itBuyData(t, doITBuy(t, h, user, "500000"))["payTo"].(map[string]any)

	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, true))
	tradeID := seedPMTrade(t, r, "JOHN DOE")
	p2p := pmPayToOf(t, r, "/api/p2p/trades/"+tradeID.String())

	if mcgp["payerInstruction"] == nil || mcgp["payerInstruction"] == "" {
		t.Fatalf("MCGP buy carries no payerInstruction: %v", mcgp)
	}
	if mcgp["payerInstruction"] != p2p["payerInstruction"] {
		t.Fatalf("MCGP buy says %q but the P2P buy screen says %q",
			mcgp["payerInstruction"], p2p["payerInstruction"])
	}
}
