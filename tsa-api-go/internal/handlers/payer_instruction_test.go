package handlers

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The payer instruction is the mitigation the partial-name-match risk was
// accepted against: a credit can bind to a stranger whose verified name is a
// token-subset of the sender's, and the only reason that is tolerable is that
// payers are told which account to send from. A pay-into screen that omits it
// is that decision quietly unmade.
func TestBuy_CentralMode_TellsThePayerToUseTheirOwnAccount(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	// doITBuy seeds the users row verified as user.Name.
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}

	data := itBuyData(t, doITBuy(t, h, user, "500000"))

	payTo, _ := data["payTo"].(map[string]any)
	if payTo == nil {
		t.Fatalf("payTo missing; body=%v", data)
	}
	want := testCentralAccount().PayerInstruction("JOHN DOE")
	if payTo["payerInstruction"] != want {
		t.Fatalf("payTo.payerInstruction = %v, want %q", payTo["payerInstruction"], want)
	}
}

// The name shown must be the stored verified name the matcher uses, not the
// free-text profile name on the authenticated user.
func TestBuy_CentralMode_NamesTheVerifiedNameNotTheProfileName(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "Johnny", WalletAddress: "0xWALLET"}
	if err := db.Exec(
		`INSERT INTO users (id, name, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?)`,
		user.ID, user.Name, user.WalletAddress, models.VerificationStatusVerified, "JOHN ADEMOLA DOE",
	).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	payTo, _ := itBuyData(t, doITBuy(t, h, user, "500000"))["payTo"].(map[string]any)

	got, _ := payTo["payerInstruction"].(string)
	if !strings.HasPrefix(got, "Pay from a bank account named JOHN ADEMOLA DOE. ") {
		t.Fatalf("payerInstruction does not name the verified legal name: %q", got)
	}
}

// A virtual account is already in the payer's own name, so the instruction is
// wrong there in every clause.
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

// setVerifiedLegalName gives an already-seeded users row its Didit name.
func setVerifiedLegalName(t *testing.T, r *rig, userID uuid.UUID, name string) {
	t.Helper()
	if err := r.db.Exec(`UPDATE users SET verified_legal_name = ? WHERE id = ?`, name, userID.String()).Error; err != nil {
		t.Fatalf("set legal name: %v", err)
	}
}

func TestGetTrade_PMPayTo_CentralMode_NamesTheBuyer(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, true))
	tradeID := seedPMTrade(t, r, "Johnny")
	setVerifiedLegalName(t, r, r.sellerID, "JOHN DOE")

	payTo := pmPayToOf(t, r, "/api/p2p/trades/"+tradeID.String())

	want := testCentralAccount().PayerInstruction("JOHN DOE")
	if payTo["payerInstruction"] != want {
		t.Fatalf("pmPayTo.payerInstruction = %v, want %q", payTo["payerInstruction"], want)
	}
}

// The seller fetches the same trade. The buyer's legal name is theirs alone.
func TestGetTrade_PMPayTo_CentralMode_SellerNeverSeesTheBuyersLegalName(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, true))
	tradeID := seedPMTrade(t, r, "Johnny")
	setVerifiedLegalName(t, r, r.sellerID, "JOHN DOE")
	// Swap sides: the rig user becomes the seller, a stranger the buyer.
	buyer := uuid.New()
	if err := r.db.Exec(`INSERT INTO users (id, name, verified_legal_name) VALUES (?,?,?)`,
		buyer.String(), "Stranger", "STRANGER PERSON").Error; err != nil {
		t.Fatalf("seed buyer: %v", err)
	}
	if err := r.db.Exec(`UPDATE p2p_trades SET buyer_id = ?, seller_id = ? WHERE id = ?`,
		buyer.String(), r.sellerID.String(), tradeID.String()).Error; err != nil {
		t.Fatalf("swap sides: %v", err)
	}

	payTo := pmPayToOf(t, r, "/api/p2p/trades/"+tradeID.String())

	got, _ := payTo["payerInstruction"].(string)
	if got != testCentralAccount().PayerInstruction("") || strings.Contains(got, "STRANGER") {
		t.Fatalf("seller's view carries a named instruction: %q", got)
	}
}

func TestGetAd_PMPayTo_CentralMode_NamesTheViewer(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, true))
	adID := seedPMAd(t, r)
	if err := r.db.Exec(`INSERT INTO users (id, name, verified_legal_name) VALUES (?,?,?)`,
		r.sellerID.String(), "Johnny", "JOHN DOE").Error; err != nil {
		t.Fatalf("seed viewer: %v", err)
	}

	payTo := pmPayToOf(t, r, "/api/p2p/ads/"+adID.String())

	want := testCentralAccount().PayerInstruction("JOHN DOE")
	if payTo["payerInstruction"] != want {
		t.Fatalf("ad pmPayTo.payerInstruction = %v, want %q", payTo["payerInstruction"], want)
	}
}

// Pointed at a merchant's own account there is no name matcher in the loop —
// the merchant confirms the transfer by hand.
func TestGetTrade_PMPayTo_MerchantAccount_CarriesNoPayerInstruction(t *testing.T) {
	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, false))
	tradeID := seedPMTrade(t, r, "JOHN DOE")

	payTo := pmPayToOf(t, r, "/api/p2p/trades/"+tradeID.String())

	if _, present := payTo["payerInstruction"]; present {
		t.Fatalf("pmPayTo carries payerInstruction for a merchant's own account: %v", payTo)
	}
}

// Products share one wording. Told different things about the same company
// account, a payer decides one of the screens is lying and stops. The
// stablecoin buy is pinned to the same text in services/payer_instruction_test.go.
func TestPayerInstruction_IdenticalAcrossProducts(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	mcgp, _ := itBuyData(t, doITBuy(t, h, user, "500000"))["payTo"].(map[string]any)

	r := newRig(t)
	r.handler.SetInboundCentralSource(centralSourceFor(t, true))
	tradeID := seedPMTrade(t, r, "JOHN DOE")
	setVerifiedLegalName(t, r, r.sellerID, "JOHN DOE")
	p2p := pmPayToOf(t, r, "/api/p2p/trades/"+tradeID.String())

	if mcgp["payerInstruction"] == nil || mcgp["payerInstruction"] == "" {
		t.Fatalf("MCGP buy carries no payerInstruction: %v", mcgp)
	}
	if mcgp["payerInstruction"] != p2p["payerInstruction"] {
		t.Fatalf("MCGP buy says %q but the P2P buy screen says %q",
			mcgp["payerInstruction"], p2p["payerInstruction"])
	}
}
