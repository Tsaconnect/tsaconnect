package handlers

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestAdminGetTradeAndMessages(t *testing.T) {
	r := newRig(t)
	r.handler.messages = services.NewP2PMessageService(r.db, nil)
	g := r.router.Group("/api/admin/p2p")
	g.GET("/trades/:id", r.handler.AdminGetTrade)
	g.GET("/trades/:id/messages", r.handler.AdminListTradeMessages)
	tradeID, _ := seedSellAdTrade(t, r, models.P2PTradeStatusDisputed, "0xopen")
	var tr models.P2PTrade
	r.db.First(&tr, "id = ?", tradeID)
	paid := time.Now().Add(-time.Hour)
	r.db.Model(&tr).Updates(map[string]any{"proof_image_url": "https://p/proof.png", "marked_paid_at": paid})
	r.db.Exec(`INSERT INTO users (id, name, username, verification_status, verified_legal_name) VALUES (?,?,?,?,?)`, tr.BuyerID, "Ada Buyer", "ada", "verified", "ADA BUYER")
	r.db.Exec(`INSERT INTO users (id, name, username, verification_status) VALUES (?,?,?,?)`, tr.SellerID, "Sam Seller", "sam", "verified")
	r.db.Create(&models.P2PDispute{ID: uuid.New(), TradeID: tradeID, RaisedBy: tr.BuyerID, RaisedRole: "buyer", Reason: "not released",
		EvidenceURLs: `["https://cdn.test/e1.jpg"]`, Status: "open"})
	r.db.Create(&models.P2PMessage{ID: uuid.New(), TradeID: tradeID, Type: "system", Body: "Trade opened.", CreatedAt: time.Now().Add(-2 * time.Minute)})
	r.db.Create(&models.P2PMessage{ID: uuid.New(), TradeID: tradeID, SenderID: &tr.BuyerID, Type: "text", Body: "sent", CreatedAt: time.Now().Add(-time.Minute)})

	rec := doJSON(t, r, http.MethodGet, "/api/admin/p2p/trades/"+tradeID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	d := decode(t, rec)["data"].(map[string]any)
	trade := d["trade"].(map[string]any)
	buyer := d["buyer"].(map[string]any)
	payee := d["payee"].(map[string]any)
	disp := d["dispute"].(map[string]any)
	if d["side"] != "sell" || trade["proofImageUrl"] != "https://p/proof.png" || trade["markedPaidAt"] == nil ||
		buyer["name"] != "Ada Buyer" || buyer["verificationStatus"] != "verified" || buyer["legalName"] != "ADA BUYER" ||
		d["seller"].(map[string]any)["name"] != "Sam Seller" || payee["accountNumber"] != "1234567890" ||
		disp["reason"] != "not released" || disp["evidenceUrls"].([]any)[0] != "https://cdn.test/e1.jpg" {
		t.Fatalf("data = %v", d)
	}
	for _, k := range []string{"email", "phone", "bvn"} {
		if _, ok := buyer[k]; ok {
			t.Fatalf("party leaks %s", k)
		}
	}
	rec = doJSON(t, r, http.MethodGet, "/api/admin/p2p/trades/"+tradeID.String()+"/messages", nil)
	msgs := decode(t, rec)["data"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["body"] != "Trade opened." {
		t.Fatalf("messages = %v", msgs)
	}
	if rec := doJSON(t, r, http.MethodGet, "/api/admin/p2p/trades/"+uuid.NewString()+"/messages", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown trade = %d", rec.Code)
	}
	if rec := doJSON(t, r, http.MethodGet, "/api/admin/p2p/trades/"+uuid.NewString(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown trade detail = %d", rec.Code)
	}
}
