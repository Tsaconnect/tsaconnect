package handlers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

const (
	refundTestUSDT    = "0x55d398326f99059fF775485246999027B3197955"
	refundTestFunding = "0x1111111111111111111111111111111111111111"
)

// refundHandlerNet is BSC with 18-decimal USDT; it counts broadcasts.
type refundHandlerNet struct {
	mu   sync.Mutex
	sent int
}

func (*refundHandlerNet) MainnetChainNameForID(id int64) (string, bool) { return "bsc", id == 56 }
func (*refundHandlerNet) MainnetChainIDs() []int64                      { return []int64{56} }
func (*refundHandlerNet) TokenAddress(chain, symbol string) string {
	if chain == "bsc" && symbol == "USDT" {
		return refundTestUSDT
	}
	return ""
}
func (*refundHandlerNet) ChainName(string) string                   { return "BNB Smart Chain" }
func (*refundHandlerNet) TokenDecimals(string, string) (int, error) { return 18, nil }
func (n *refundHandlerNet) Broadcast(context.Context, string, *types.Transaction) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent++
	return nil
}
func (*refundHandlerNet) ConfirmedNonce(context.Context, string, common.Address, uint64) (uint64, error) {
	return 0, nil
}

func (*refundHandlerNet) Endpoint(string) services.RPCEndpoint { return refundHandlerEndpoint{} }

// refundHandlerEndpoint never fails over.
type refundHandlerEndpoint struct{}

func (refundHandlerEndpoint) EndpointGeneration() uint64 { return 0 }

// refundHandlerVerifier: nothing is mined yet, but the node has it.
type refundHandlerVerifier struct{}

func (refundHandlerVerifier) Verify(context.Context, string, string, string, string, string) (*services.StablePayment, error) {
	return nil, errors.New("verify: not found")
}
func (refundHandlerVerifier) TransactionKnown(context.Context, string, string) (bool, string, error) {
	return true, "", nil
}

type refundHandlerRig struct {
	r         *gin.Engine
	db        *gorm.DB
	m         *models.PayMerchant
	owner     *models.User
	ownerKey  *ecdsa.PrivateKey
	merchants *services.PayMerchantService
	payments  *services.PayPaymentService
	refunds   *services.PayRefundService
	h         *PayHandler
	net       *refundHandlerNet
	payer     uuid.UUID
}

func newRefundHandlerRig(t *testing.T) *refundHandlerRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{}, &models.PayAuditEvent{},
		&models.PayRefund{}, &models.PaycrestOrderRecord{}, &models.RampSellIntent{}, &models.WalletLinkChallenge{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT, email TEXT, wallet_address TEXT, verification_status TEXT,
		verified_legal_name TEXT, account_status TEXT DEFAULT 'active')`).Error; err != nil {
		t.Fatal(err)
	}
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })

	key, _ := crypto.GenerateKey()
	wallet := crypto.PubkeyToAddress(key.PublicKey).Hex()
	m := seedHandlerMerchant(t, db)
	owner := &models.User{ID: m.UserID, WalletAddress: wallet}
	payer := uuid.New()
	db.Exec(`INSERT INTO users (id, name, email, wallet_address) VALUES (?, 'Sky Owner', 'owner@skyair.ng', ?)`, owner.ID, wallet)
	db.Exec(`INSERT INTO users (id, name, email, verification_status, verified_legal_name) VALUES (?, 'Ada Okafor', 'ada@x.ng', 'verified', 'Ada Okafor')`, payer)
	used := payHandlerNow
	if err := db.Create(&models.WalletLinkChallenge{UserID: owner.ID, Address: strings.ToLower(wallet), Nonce: uuid.NewString(),
		Message: "proof", ExpiresAt: payHandlerNow.Add(time.Hour), UsedAt: &used}).Error; err != nil {
		t.Fatal(err)
	}

	now := func() time.Time { return payHandlerNow }
	net := &refundHandlerNet{}
	merchants := services.NewPayMerchantService(db, nil, net, []byte(strings.Repeat("k", 32)), now)
	payments := services.NewPayPaymentService(db, merchants, 100, "https://x", now)
	refunds := services.NewPayRefundService(db, payments, merchants, net, refundHandlerVerifier{}, 5, now)
	payments.SetRefundAggregator(refunds)
	h := NewPayHandler(merchants, payments, nil, false).WithRefunds(refunds)

	return &refundHandlerRig{r: refundOwnerRouter(h, owner), db: db, m: m, owner: owner, ownerKey: key, merchants: merchants, payments: payments,
		refunds: refunds, h: h, net: net, payer: payer}
}

// refundOwnerRouter mounts the owner refund routes as routes.go does, signed
// in as user (nil: nobody).
func refundOwnerRouter(h *PayHandler, user *models.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user != nil {
			u := *user
			c.Set("user", &u)
		}
	})
	r.GET("/api/pay/merchant/payments/:id", h.OwnerPayment)
	r.POST("/api/pay/merchant/payments/:id/refunds", h.OwnerCreateRefund)
	r.GET("/api/pay/merchant/payments/:id/refunds", h.OwnerPaymentRefunds)
	r.GET("/api/pay/merchant/refunds", h.OwnerRefunds)
	r.GET("/api/pay/merchant/refunds/:id", h.OwnerRefund)
	r.POST("/api/pay/merchant/refunds/:id/cancel", h.OwnerCancelRefund)
	r.POST("/api/pay/merchant/refunds/:id/submit", h.OwnerSubmitRefund)
	return r
}

// paid is a succeeded payment in mode whose Paycrest order took $34 of BSC
// USDT from the funding wallet (live) — or a ₦15,000 test payment ($10 cap).
func (o *refundHandlerRig) paid(t *testing.T, mode, ref string) *models.PayPayment {
	t.Helper()
	p, _, err := o.payments.Create(context.Background(), o.m, mode, services.CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: ref, Description: "LOS-ABV"})
	if err != nil {
		t.Fatal(err)
	}
	at := payHandlerNow
	updates := map[string]any{"status": models.PayStatusSucceeded, "payer_user_id": o.payer, "succeeded_at": &at}
	if mode == models.PayModeLive {
		order := models.PaycrestOrderRecord{ID: uuid.New(), UserID: o.payer, Direction: "sell", Status: models.PaycrestStatusSettled, Reference: "order-" + ref,
			FiatCode: "NGN", FiatAmount: "50500", Token: "USDT", ChainKey: "bsc", Network: "bnb-smart-chain", TokenAmount: "34", WalletAddress: refundTestFunding, PayPaymentID: &p.ID}
		if err := o.db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		updates["paycrest_order_id"] = order.ID
	} else {
		updates["customer_total_kobo"] = int64(1_500_000)
	}
	if err := o.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Updates(updates).Error; err != nil {
		t.Fatal(err)
	}
	got, _ := o.payments.Get(context.Background(), p.ID)
	return got
}

type refundResp struct {
	status int
	raw    string
	body   map[string]any
}

func (o *refundHandlerRig) do(method, path string, body any) refundResp {
	w := payDo(o.r, method, path, body, nil)
	out := refundResp{status: w.Code, raw: w.Body.String()}
	json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

func (r refundResp) data() map[string]any { d, _ := r.body["data"].(map[string]any); return d }
func (r refundResp) code() string         { c, _ := r.body["code"].(string); return c }
func (r refundResp) refund() map[string]any {
	ref, _ := r.data()["refund"].(map[string]any)
	return ref
}

// signedRefund signs the BSC USDT transfer that pays the refund exactly.
func (o *refundHandlerRig) signedRefund(t *testing.T, key *ecdsa.PrivateKey, to string, micro int64) string {
	t.Helper()
	amount := new(big.Int).Mul(big.NewInt(micro), big.NewInt(1_000_000_000_000))
	data := append(append(common.FromHex("0xa9059cbb"), common.LeftPadBytes(common.HexToAddress(to).Bytes(), 32)...), common.LeftPadBytes(amount.Bytes(), 32)...)
	token := common.HexToAddress(refundTestUSDT)
	tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(56), To: &token, Value: new(big.Int), Gas: 80_000,
		GasFeeCap: big.NewInt(2), GasTipCap: big.NewInt(1), Data: data}), types.LatestSignerForChainID(big.NewInt(56)), key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := tx.MarshalBinary()
	return hexutil.Encode(raw)
}

func TestPayRefundOwner_CreateListDetailCancelOverHTTP(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "H-1")
	created := o.do(http.MethodPost, "/api/pay/merchant/payments/"+p.ID.String()+"/refunds", map[string]any{"amount": "12.50", "reason": "Cancelled"})
	if created.status != http.StatusCreated || created.refund()["status"] != "requested" || created.refund()["amount"] != "12.50" ||
		created.refund()["requestedVia"] != "app" || created.refund()["toAddress"] != refundTestFunding {
		t.Fatalf("create %d %s", created.status, created.raw)
	}
	id := created.refund()["id"].(string)

	forPayment := o.do(http.MethodGet, "/api/pay/merchant/payments/"+p.ID.String()+"/refunds", nil)
	if forPayment.status != http.StatusOK || forPayment.data()["cap"] != "34.00" || forPayment.data()["remaining"] != "21.50" ||
		forPayment.data()["refundable"] != false || len(forPayment.data()["refunds"].([]any)) != 1 {
		t.Fatalf("for payment %d %s", forPayment.status, forPayment.raw)
	}
	list := o.do(http.MethodGet, "/api/pay/merchant/refunds?mode=live&from=2026-09-23&to=2026-09-23", nil)
	if list.status != http.StatusOK || list.data()["total"] != float64(1) {
		t.Fatalf("list %d %s", list.status, list.raw)
	}
	detail := o.do(http.MethodGet, "/api/pay/merchant/refunds/"+id, nil)
	opts, _ := detail.data()["options"].([]any)
	if detail.status != http.StatusOK || detail.data()["fromAddress"] != o.owner.WalletAddress || len(opts) != 1 ||
		opts[0].(map[string]any)["amountBaseUnits"] != "12500000000000000000" || detail.data()["payerFirstName"] != "Ada" {
		t.Fatalf("detail %d %s", detail.status, detail.raw)
	}
	cancelled := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+id+"/cancel", nil)
	if cancelled.status != http.StatusOK || cancelled.refund()["status"] != "cancelled" {
		t.Fatalf("cancel %d %s", cancelled.status, cancelled.raw)
	}
	if again := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+id+"/cancel", nil); again.status != http.StatusConflict || again.code() != "refund_state" {
		t.Fatalf("cancel twice %d %s", again.status, again.raw)
	}
}

func TestPayRefundOwner_OverCapAnswers422WithRemaining(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "H-2")
	res := o.do(http.MethodPost, "/api/pay/merchant/payments/"+p.ID.String()+"/refunds", map[string]any{"amount": "34.01"})
	if res.status != http.StatusUnprocessableEntity || res.code() != "refund_over_cap" || res.data()["remaining"] != "34.00" {
		t.Fatalf("%d %s", res.status, res.raw)
	}
	if bad := o.do(http.MethodPost, "/api/pay/merchant/payments/"+p.ID.String()+"/refunds", map[string]any{"amount": "1.2345678"}); bad.status != http.StatusUnprocessableEntity || bad.code() != "refund_invalid" {
		t.Fatalf("bad amount %d %s", bad.status, bad.raw)
	}
}

// A test payment is refunded through the same route, confirmed at once.
func TestPayRefundOwner_ATestPaymentIsRefundedAtOnce(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeTest, "H-3")
	res := o.do(http.MethodPost, "/api/pay/merchant/payments/"+p.ID.String()+"/refunds?mode=test", map[string]any{"amount": "10"})
	if res.status != http.StatusCreated || res.refund()["status"] != "confirmed" || res.refund()["mode"] != "test" {
		t.Fatalf("%d %s", res.status, res.raw)
	}
	if o.net.sent != 0 {
		t.Fatal("a test refund touched the chain")
	}
}

func TestPayRefundOwner_AnotherMerchantsRefundIsNotFound(t *testing.T) {
	o := newRefundHandlerRig(t)
	foreign := &models.PayRefund{PaymentID: uuid.New(), MerchantID: uuid.New(), Mode: models.PayModeLive, AmountMicroUSD: 1, CapMicroUSD: 1,
		ToAddress: refundTestFunding, Status: models.PayRefundRequested, RequestedVia: models.PayRefundViaWeb}
	o.db.Create(foreign)
	for _, path := range []string{"/api/pay/merchant/refunds/" + foreign.ID.String(), "/api/pay/merchant/refunds/not-a-uuid"} {
		if res := o.do(http.MethodGet, path, nil); res.status != http.StatusNotFound {
			t.Errorf("%s: %d %s", path, res.status, res.raw)
		}
	}
	if res := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+foreign.ID.String()+"/cancel", nil); res.status != http.StatusNotFound {
		t.Fatalf("cancel %d", res.status)
	}
	submit := map[string]any{"chainId": 56, "tokenSymbol": "USDT", "signedTx": o.signedRefund(t, o.ownerKey, refundTestFunding, 1)}
	if res := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+foreign.ID.String()+"/submit", submit); res.status != http.StatusNotFound || o.net.sent != 0 {
		t.Fatalf("submit %d %s", res.status, res.raw)
	}
	var still models.PayRefund
	if o.db.First(&still, "id = ?", foreign.ID); still.Status != models.PayRefundRequested {
		t.Fatalf("another merchant's refund moved: %+v", still)
	}
}

func TestPayRefundOwner_SubmitNeedsTheProvenWallet(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "H-4")
	ref, _ := o.refunds.Request(context.Background(), o.m, models.PayModeLive, p.ID, services.PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})
	o.db.Where("user_id = ?", o.owner.ID).Delete(&models.WalletLinkChallenge{})
	res := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+ref.ID.String()+"/submit",
		map[string]any{"chainId": 56, "tokenSymbol": "USDT", "signedTx": o.signedRefund(t, o.ownerKey, ref.ToAddress, ref.AmountMicroUSD)})
	if res.status != http.StatusForbidden || res.code() != WalletProofRequiredCode || o.net.sent != 0 {
		t.Fatalf("%d %s", res.status, res.raw)
	}
}

func TestPayRefundOwner_SubmitRecordsAndBroadcasts(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "H-5")
	ref, _ := o.refunds.Request(context.Background(), o.m, models.PayModeLive, p.ID, services.PayRefundRequest{Amount: "12.50", Via: models.PayRefundViaApp})
	res := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+ref.ID.String()+"/submit",
		map[string]any{"chainId": 56, "tokenSymbol": "USDT", "signedTx": o.signedRefund(t, o.ownerKey, ref.ToAddress, ref.AmountMicroUSD)})
	if res.status != http.StatusOK || res.refund()["status"] != "submitted" || res.refund()["txHash"] == "" || o.net.sent != 1 {
		t.Fatalf("%d %s", res.status, res.raw)
	}
}

func TestPayRefundOwner_SubmitRefusesAForeignTransaction(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "H-6")
	ref, _ := o.refunds.Request(context.Background(), o.m, models.PayModeLive, p.ID, services.PayRefundRequest{Amount: "12.50", Via: models.PayRefundViaApp})
	stranger, _ := crypto.GenerateKey()
	res := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+ref.ID.String()+"/submit",
		map[string]any{"chainId": 56, "tokenSymbol": "USDT", "signedTx": o.signedRefund(t, stranger, ref.ToAddress, ref.AmountMicroUSD)})
	if res.status != http.StatusUnprocessableEntity || res.code() != "refund_tx_invalid" || !strings.Contains(res.raw, o.owner.WalletAddress) {
		t.Fatalf("%d %s", res.status, res.raw)
	}
	if missing := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+ref.ID.String()+"/submit", map[string]any{"chainId": 56}); missing.status != http.StatusBadRequest {
		t.Fatalf("no signedTx %d", missing.status)
	}
}

func TestPayRefundOwner_ListRejectsABadStatusOrDateRange(t *testing.T) {
	o := newRefundHandlerRig(t)
	if res := o.do(http.MethodGet, "/api/pay/merchant/refunds?status=refunded", nil); res.status != http.StatusBadRequest {
		t.Fatalf("status %d", res.status)
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/refunds?from=2026-10-09&to=2026-10-01", nil); res.status != http.StatusBadRequest || res.code() != "date_range" {
		t.Fatalf("range %d %s", res.status, res.raw)
	}
}

func TestPayRefundOwner_PaymentDetailCarriesRefundTotals(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "H-7")
	at := payHandlerNow
	o.db.Create(&models.PayRefund{PaymentID: p.ID, MerchantID: o.m.ID, Mode: models.PayModeLive, AmountMicroUSD: 34_000_000, CapMicroUSD: 34_000_000,
		ToAddress: refundTestFunding, Status: models.PayRefundConfirmed, RequestedVia: models.PayRefundViaApp, ConfirmedAt: &at})
	res := o.do(http.MethodGet, "/api/pay/merchant/payments/"+p.ID.String(), nil)
	payment, _ := res.data()["payment"].(map[string]any)
	if res.status != http.StatusOK || payment["refundedAmount"] != "34.00" || payment["refundStatus"] != "full" || payment["status"] != "succeeded" {
		t.Fatalf("%d %s", res.status, res.raw)
	}
}

// Another merchant's payment, or this merchant's in the other mode, is not
// found; a mode that is neither live nor test is refused.
func TestPayRefundOwner_PaymentRoutesAreScopedToTheOwnersMerchantAndMode(t *testing.T) {
	o := newRefundHandlerRig(t)
	live := o.paid(t, models.PayModeLive, "H-8")
	other := seedHandlerMerchant(t, o.db)
	foreign, _, err := o.payments.Create(context.Background(), other, models.PayModeLive, services.CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "H-9"})
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"another merchant's": "/api/pay/merchant/payments/" + foreign.ID.String() + "/refunds",
		"the other mode":     "/api/pay/merchant/payments/" + live.ID.String() + "/refunds?mode=test",
		"not an id":          "/api/pay/merchant/payments/not-a-uuid/refunds",
	} {
		if res := o.do(http.MethodGet, path, nil); res.status != http.StatusNotFound {
			t.Errorf("list %s: %d %s", name, res.status, res.raw)
		}
		if res := o.do(http.MethodPost, path, map[string]any{"amount": "1"}); res.status != http.StatusNotFound {
			t.Errorf("create %s: %d %s", name, res.status, res.raw)
		}
	}
	for _, path := range []string{"/api/pay/merchant/payments/" + live.ID.String() + "/refunds?mode=sandbox", "/api/pay/merchant/refunds?mode=sandbox"} {
		if res := o.do(http.MethodGet, path, nil); res.status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", path, res.status, res.raw)
		}
	}
	var n int64
	if o.db.Model(&models.PayRefund{}).Count(&n); n != 0 {
		t.Fatalf("%d refunds made", n)
	}
}

// A refund-id route takes no mode: the row has its own, so ?mode never hides
// it. The list does take one, and shows only that mode's refunds.
func TestPayRefundOwner_RefundIDRoutesTakeTheModeFromTheRow(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "H-10")
	ref, err := o.refunds.Request(context.Background(), o.m, models.PayModeLive, p.ID, services.PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})
	if err != nil {
		t.Fatal(err)
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/refunds/"+ref.ID.String()+"?mode=test", nil); res.status != http.StatusOK || res.refund()["mode"] != "live" {
		t.Fatalf("detail %d %s", res.status, res.raw)
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/refunds?mode=test", nil); res.status != http.StatusOK || res.data()["total"] != float64(0) {
		t.Fatalf("test list %d %s", res.status, res.raw)
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/refunds", nil); res.status != http.StatusOK || res.data()["total"] != float64(1) ||
		res.data()["page"] != float64(1) || res.data()["limit"] != float64(20) {
		t.Fatalf("default list %d %s", res.status, res.raw)
	}
	if res := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+ref.ID.String()+"/cancel?mode=test", nil); res.status != http.StatusOK || res.refund()["status"] != "cancelled" {
		t.Fatalf("cancel %d %s", res.status, res.raw)
	}
}

// A second refund while one is open is refused with refund_open.
func TestPayRefundOwner_OneOpenRefundAtATime(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "H-11")
	path := "/api/pay/merchant/payments/" + p.ID.String() + "/refunds"
	if first := o.do(http.MethodPost, path, map[string]any{"amount": "1"}); first.status != http.StatusCreated {
		t.Fatalf("first %d %s", first.status, first.raw)
	}
	if second := o.do(http.MethodPost, path, map[string]any{"amount": "1"}); second.status != http.StatusConflict || second.code() != "refund_open" {
		t.Fatalf("second %d %s", second.status, second.raw)
	}
}

func TestPayRefundOwner_SignedOutOrMerchantlessIsRefused(t *testing.T) {
	o := newRefundHandlerRig(t)
	stranger := &models.User{ID: uuid.New()}
	paths := []struct{ method, path string }{
		{http.MethodPost, "/api/pay/merchant/payments/" + uuid.NewString() + "/refunds"},
		{http.MethodGet, "/api/pay/merchant/payments/" + uuid.NewString() + "/refunds"},
		{http.MethodGet, "/api/pay/merchant/refunds"},
		{http.MethodGet, "/api/pay/merchant/refunds/" + uuid.NewString()},
		{http.MethodPost, "/api/pay/merchant/refunds/" + uuid.NewString() + "/cancel"},
		{http.MethodPost, "/api/pay/merchant/refunds/" + uuid.NewString() + "/submit"},
	}
	for _, p := range paths {
		if w := payDo(refundOwnerRouter(o.h, nil), p.method, p.path, map[string]any{}, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("signed out %s %s: %d", p.method, p.path, w.Code)
		}
		w := payDo(refundOwnerRouter(o.h, stranger), p.method, p.path, map[string]any{}, nil)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"no_merchant"`) {
			t.Errorf("no merchant %s %s: %d %s", p.method, p.path, w.Code, w.Body.String())
		}
	}
}

// failingRefunds answers every refund call with err.
type failingRefunds struct{ err error }

func (f failingRefunds) Request(context.Context, *models.PayMerchant, string, uuid.UUID, services.PayRefundRequest) (*models.PayRefund, error) {
	return nil, f.err
}
func (f failingRefunds) Cancel(context.Context, uuid.UUID, uuid.UUID) (*models.PayRefund, error) {
	return nil, f.err
}
func (f failingRefunds) Submit(context.Context, *models.PayMerchant, uuid.UUID, string, services.PayRefundSubmit) (*models.PayRefund, error) {
	return nil, f.err
}
func (f failingRefunds) Detail(context.Context, *models.PayMerchant, uuid.UUID) (*services.PayRefundDetail, error) {
	return nil, f.err
}
func (f failingRefunds) List(context.Context, services.PayRefundFilter) ([]services.PayRefundListItem, int64, error) {
	return nil, 0, f.err
}
func (f failingRefunds) ListForPayment(context.Context, *models.PayPayment) (*services.PayPaymentRefunds, error) {
	return nil, f.err
}
func (f failingRefunds) AdminList(context.Context, services.PayRefundAdminFilter) ([]services.PayRefundAdminItem, int64, error) {
	return nil, 0, f.err
}
func (f failingRefunds) Reevaluate(context.Context, uuid.UUID) (*models.PayRefund, error) {
	return nil, f.err
}
func (f failingRefunds) View(*models.PayRefund) services.PayRefundView {
	return services.PayRefundView{}
}

// Every refusal the refund service can give reaches the owner as its status,
// code and owner-safe words, on every owner route — and anything else is a
// plain 500 whose cause is logged redacted, never echoed.
func TestPayRefundOwner_EachRefusalAnswersItsStatusAndCode(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "H-12")
	refundID := uuid.NewString()
	submit := map[string]any{"chainId": 56, "tokenSymbol": "USDT", "signedTx": "0x01"}
	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/pay/merchant/payments/" + p.ID.String() + "/refunds", map[string]any{"amount": "1"}},
		{http.MethodGet, "/api/pay/merchant/payments/" + p.ID.String() + "/refunds", nil},
		{http.MethodGet, "/api/pay/merchant/refunds", nil},
		{http.MethodGet, "/api/pay/merchant/refunds/" + refundID, nil},
		{http.MethodPost, "/api/pay/merchant/refunds/" + refundID + "/cancel", nil},
		{http.MethodPost, "/api/pay/merchant/refunds/" + refundID + "/submit", submit},
	}
	const secret = "SECRETKEY"
	for _, c := range []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{services.ErrPayRefundOpen, http.StatusConflict, "refund_open", "already has a refund in progress"},
		{fmt.Errorf("lock: %w", services.ErrPayRefundOpen), http.StatusConflict, "refund_open", "already has a refund in progress"},
		{services.ErrPayRefundInvalid, http.StatusUnprocessableEntity, "refund_invalid", "at most 6 decimal places"},
		{&services.PayRefundOverCapError{RemainingMicroUSD: 2_500_000}, http.StatusUnprocessableEntity, "refund_over_cap", "more than can still be refunded"},
		{services.ErrPayRefundNotSucceeded, http.StatusConflict, "payment_not_succeeded", "Only paid payments can be refunded"},
		{services.ErrPayRefundNotRefundable, http.StatusUnprocessableEntity, "not_refundable", "Refunds aren't available for this payment"},
		{services.ErrPayRefundPayerDeleted, http.StatusUnprocessableEntity, "payer_deleted", "account was deleted"},
		{services.ErrPayRefundState, http.StatusConflict, "refund_state", "can't be changed now"},
		{services.ErrPayRefundNotFound, http.StatusNotFound, "", "Refund not found"},
		{services.ErrPayPaymentNotFound, http.StatusNotFound, "", "Payment not found"},
		{services.ErrPayRefundNonceUsed, http.StatusConflict, "refund_nonce_used", "sign the refund again"},
		{services.ErrPayRefundNonceFar, http.StatusConflict, "refund_nonce_far", "until earlier ones go through"},
		{services.ErrPayRefundTxUsed, http.StatusConflict, "refund_tx_used", "already attached to another refund"},
		{services.ErrPayRefundUnavailable, http.StatusServiceUnavailable, "refunds_unavailable", "Refunds aren't available right now"},
		{&services.PayRefundTxError{Why: "it isn't a signed transaction"}, http.StatusUnprocessableEntity, "refund_tx_invalid", "it isn't a signed transaction"},
		{errors.New(`ERROR: relation "pay_refunds" does not exist; Post "https://bsc.example-rpc.io/v2/` + secret + `"`), http.StatusInternalServerError, "", "Something went wrong"},
	} {
		h := NewPayHandler(o.merchants, o.payments, nil, false).WithRefunds(failingRefunds{c.err})
		r := refundOwnerRouter(h, o.owner)
		for _, rt := range routes {
			var logged bytes.Buffer
			log.SetOutput(&logged)
			w := payDo(r, rt.method, rt.path, rt.body, nil)
			log.SetOutput(os.Stderr)
			res := refundResp{status: w.Code, raw: w.Body.String()}
			json.Unmarshal(w.Body.Bytes(), &res.body)
			msg, _ := res.body["message"].(string)
			if res.status != c.status || res.code() != c.code || !strings.Contains(msg, c.message) {
				t.Errorf("%v on %s %s: %d %s", c.err, rt.method, rt.path, res.status, res.raw)
			}
			if strings.Contains(res.raw, "pay refund:") || strings.Contains(res.raw, "relation") || strings.Contains(res.raw, secret) {
				t.Errorf("%v on %s %s: the service's own words reached the owner: %s", c.err, rt.method, rt.path, res.raw)
			}
			if strings.Contains(logged.String(), secret) {
				t.Errorf("%v on %s %s: logged unredacted: %q", c.err, rt.method, rt.path, logged.String())
			}
			if c.code == "refund_over_cap" && res.data()["remaining"] != "2.50" {
				t.Errorf("over cap on %s %s: %s", rt.method, rt.path, res.raw)
			}
		}
	}
}

func TestPayRefundOwner_ListFiltersByStatus(t *testing.T) {
	o := newRefundHandlerRig(t)
	open := o.paid(t, models.PayModeLive, "H-FILT-1")
	if res := o.do(http.MethodPost, "/api/pay/merchant/payments/"+open.ID.String()+"/refunds", map[string]any{"amount": "1"}); res.status != http.StatusCreated {
		t.Fatalf("create %d %s", res.status, res.raw)
	}
	done := o.paid(t, models.PayModeLive, "H-FILT-2")
	at := payHandlerNow
	if err := o.db.Create(&models.PayRefund{PaymentID: done.ID, MerchantID: o.m.ID, Mode: models.PayModeLive, AmountMicroUSD: 1_000_000, CapMicroUSD: 34_000_000,
		Status: models.PayRefundConfirmed, RequestedVia: models.PayRefundViaApp, CreatedAt: payHandlerNow, ConfirmedAt: &at}).Error; err != nil {
		t.Fatal(err)
	}
	all := o.do(http.MethodGet, "/api/pay/merchant/refunds", nil)
	if all.data()["total"] != float64(2) {
		t.Fatalf("unfiltered %s", all.raw)
	}
	requested := o.do(http.MethodGet, "/api/pay/merchant/refunds?status=requested", nil)
	items, _ := requested.data()["refunds"].([]any)
	if requested.data()["total"] != float64(1) || len(items) != 1 || items[0].(map[string]any)["status"] != "requested" {
		t.Fatalf("status=requested %s", requested.raw)
	}
}

func TestPayRefundOwner_SubmitBodyIsCappedAt16KiB(t *testing.T) {
	o := newRefundHandlerRig(t)
	huge := map[string]any{"chainId": 56, "tokenSymbol": "USDT", "signedTx": "0x" + strings.Repeat("ab", 10<<10)}
	res := o.do(http.MethodPost, "/api/pay/merchant/refunds/"+uuid.NewString()+"/submit", huge)
	if res.status != http.StatusBadRequest {
		t.Fatalf("a 20 KiB body: %d %s", res.status, res.raw)
	}
}

// One helper answers every unrecognised owner-route error: a plain 500 and a
// redacted log line, never the error's own words.
func TestPayInternalFail_IsRedactedAndPlain(t *testing.T) {
	const secret = "SECRETKEY"
	err := errors.New(`Post "https://bsc.example-rpc.io/v2/` + secret + `": dial tcp`)
	for name, fail := range map[string]func(*gin.Context){
		"payOwnerFail":  func(c *gin.Context) { payOwnerFail(c, err) },
		"payRefundFail": func(c *gin.Context) { payRefundFail(c, err) },
	} {
		var logged bytes.Buffer
		log.SetOutput(&logged)
		w := payDo(func() *gin.Engine {
			r := gin.New()
			r.GET("/x", fail)
			return r
		}(), http.MethodGet, "/x", nil, nil)
		log.SetOutput(os.Stderr)
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "dial") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if logged.Len() == 0 || strings.Contains(logged.String(), secret) {
			t.Errorf("%s: log %q", name, logged.String())
		}
	}
}

// adminRefundEngine mounts the admin refund routes for a user of role, with
// the super-admin gate exactly as routes.go puts it on reevaluate.
func (o *refundHandlerRig) adminRefundEngine(role string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", &models.User{ID: uuid.New(), Role: role}) })
	r.GET("/api/admin/pay/refunds", o.h.AdminListRefunds)
	r.GET("/api/admin/pay/payments", o.h.AdminListPayments)
	r.POST("/api/admin/pay/refunds/:id/reevaluate", middleware.RequireSuperAdmin(), o.h.AdminReevaluateRefund)
	return r
}

func (o *refundHandlerRig) adminDo(role, method, path string) refundResp {
	w := payDo(o.adminRefundEngine(role), method, path, nil, nil)
	out := refundResp{status: w.Code, raw: w.Body.String()}
	json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

func (o *refundHandlerRig) submittedRefund(t *testing.T, ref string) *models.PayRefund {
	t.Helper()
	p := o.paid(t, models.PayModeLive, ref)
	at, hash := payHandlerNow, "0x"+strings.Repeat("cd", 32)
	r := &models.PayRefund{PaymentID: p.ID, MerchantID: o.m.ID, Mode: models.PayModeLive, AmountMicroUSD: 1_000_000, CapMicroUSD: 34_000_000,
		ToAddress: refundTestFunding, FromAddress: o.owner.WalletAddress, ChainID: 56, TokenSymbol: "USDT", TokenAddress: refundTestUSDT,
		TxHash: &hash, RawTx: "0x00", Status: models.PayRefundSubmitted, RequestedVia: models.PayRefundViaApp, CreatedAt: payHandlerNow, SubmittedAt: &at}
	if err := o.db.Create(r).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPayRefundAdmin_ListsAndFiltersEveryMerchantsRefunds(t *testing.T) {
	o := newRefundHandlerRig(t)
	sent := o.submittedRefund(t, "ADM-H-1")
	other := o.paid(t, models.PayModeLive, "ADM-H-2")
	o.refunds.Request(context.Background(), o.m, models.PayModeLive, other.ID, services.PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})

	all := o.adminDo(models.RoleAdmin, http.MethodGet, "/api/admin/pay/refunds")
	if all.status != http.StatusOK || all.data()["total"] != float64(2) {
		t.Fatalf("all %d %s", all.status, all.raw)
	}
	bySent := o.adminDo(models.RoleAdmin, http.MethodGet, "/api/admin/pay/refunds?status=submitted")
	items, _ := bySent.data()["refunds"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != sent.ID.String() || items[0].(map[string]any)["merchantName"] != "Sky Air" {
		t.Fatalf("by status %s", bySent.raw)
	}
	byPayment := o.adminDo(models.RoleAdmin, http.MethodGet, "/api/admin/pay/refunds?paymentId="+other.ID.String())
	if byPayment.data()["total"] != float64(1) {
		t.Fatalf("by payment %s", byPayment.raw)
	}
	if testOnly := o.adminDo(models.RoleAdmin, http.MethodGet, "/api/admin/pay/refunds?mode=test"); testOnly.data()["total"] != float64(0) {
		t.Fatalf("by mode %s", testOnly.raw)
	}
	for _, q := range []string{"?status=refunded", "?paymentId=nope", "?mode=sandbox"} {
		if bad := o.adminDo(models.RoleAdmin, http.MethodGet, "/api/admin/pay/refunds"+q); bad.status != http.StatusBadRequest {
			t.Errorf("%s: %d", q, bad.status)
		}
	}
}

func TestPayRefundAdmin_ReevaluateIsSuperAdminOnlyAndOnlyForOpenProofs(t *testing.T) {
	o := newRefundHandlerRig(t)
	sent := o.submittedRefund(t, "ADM-H-3")
	path := "/api/admin/pay/refunds/" + sent.ID.String() + "/reevaluate"
	if res := o.adminDo(models.RoleAdmin, http.MethodPost, path); res.status != http.StatusForbidden {
		t.Fatalf("plain admin: %d %s", res.status, res.raw)
	}
	res := o.adminDo(models.RoleSuperAdmin, http.MethodPost, path)
	if res.status != http.StatusOK || res.refund()["status"] != "submitted" {
		t.Fatalf("super admin: %d %s", res.status, res.raw)
	}
	p := o.paid(t, models.PayModeLive, "ADM-H-4")
	waiting, _ := o.refunds.Request(context.Background(), o.m, models.PayModeLive, p.ID, services.PayRefundRequest{Amount: "1", Via: models.PayRefundViaApp})
	if res := o.adminDo(models.RoleSuperAdmin, http.MethodPost, "/api/admin/pay/refunds/"+waiting.ID.String()+"/reevaluate"); res.status != http.StatusConflict || res.code() != "refund_state" {
		t.Fatalf("requested: %d %s", res.status, res.raw)
	}
	if res := o.adminDo(models.RoleSuperAdmin, http.MethodPost, "/api/admin/pay/refunds/"+uuid.NewString()+"/reevaluate"); res.status != http.StatusNotFound {
		t.Fatalf("unknown: %d", res.status)
	}
}

// The admin payments list is built from Views, so it carries the refund
// totals the console shows beside each payment.
func TestPayRefundAdmin_PaymentsListCarriesRefundTotals(t *testing.T) {
	o := newRefundHandlerRig(t)
	o.submittedRefund(t, "ADM-H-5")
	res := o.adminDo(models.RoleAdmin, http.MethodGet, "/api/admin/pay/payments")
	items, _ := res.data()["payments"].([]any)
	if res.status != http.StatusOK || len(items) == 0 {
		t.Fatalf("%d %s", res.status, res.raw)
	}
	first := items[0].(map[string]any)
	if _, ok := first["refundedAmount"]; !ok {
		t.Fatalf("no refundedAmount: %s", res.raw)
	}
	if _, ok := first["refundStatus"]; !ok {
		t.Fatalf("no refundStatus: %s", res.raw)
	}
}

// With refunds off (no WithRefunds — TSA_PAY_REFUNDS unset) every owner and
// admin refund handler answers 503 refunds_unavailable before reading anything;
// the payment views still carry refund totals.
func TestPayRefund_OffAnswersRefundsUnavailableOnEveryRoute(t *testing.T) {
	o := newRefundHandlerRig(t)
	p := o.paid(t, models.PayModeLive, "OFF-1")
	off := NewPayHandler(o.merchants, o.payments, nil, false)
	owner := refundOwnerRouter(off, o.owner)
	admin := (&refundHandlerRig{h: off}).adminRefundEngine(models.RoleSuperAdmin)
	id := uuid.NewString()
	for _, rt := range []struct {
		r            *gin.Engine
		method, path string
	}{
		{owner, http.MethodPost, "/api/pay/merchant/payments/" + p.ID.String() + "/refunds?mode=live"},
		{owner, http.MethodGet, "/api/pay/merchant/payments/" + p.ID.String() + "/refunds?mode=live"},
		{owner, http.MethodGet, "/api/pay/merchant/refunds?mode=live"},
		{owner, http.MethodGet, "/api/pay/merchant/refunds/" + id},
		{owner, http.MethodPost, "/api/pay/merchant/refunds/" + id + "/cancel"},
		{owner, http.MethodPost, "/api/pay/merchant/refunds/" + id + "/submit"},
		{admin, http.MethodGet, "/api/admin/pay/refunds"},
		{admin, http.MethodPost, "/api/admin/pay/refunds/" + id + "/reevaluate"},
	} {
		w := payDo(rt.r, rt.method, rt.path, map[string]any{"amount": "1"}, nil)
		var body struct{ Message, Code string }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: %v %s", rt.method, rt.path, err, w.Body.String())
		}
		if w.Code != http.StatusServiceUnavailable || body.Code != "refunds_unavailable" || body.Message != "Refunds aren't available yet." {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
	var n int64
	if o.db.Model(&models.PayRefund{}).Count(&n); n != 0 {
		t.Fatalf("refunds off made %d refunds", n)
	}
	done := o.submittedRefund(t, "OFF-2")
	o.db.Model(done).Update("status", models.PayRefundConfirmed)
	w := payDo(admin, http.MethodGet, "/api/admin/pay/payments", nil, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"refundStatus":"partial"`) {
		t.Fatalf("payments list with refunds off: %d %s", w.Code, w.Body.String())
	}
}

// reevaluateStub answers Reevaluate with err; nothing else is called.
type reevaluateStub struct {
	payRefundAPI
	err error
}

func (s reevaluateStub) Reevaluate(context.Context, uuid.UUID) (*models.PayRefund, error) {
	return nil, s.err
}

// Only a chain that couldn't be read is 502 chain_unreadable; anything else —
// a database failure — is the plain, redacted 500, never worded as the chain.
func TestPayRefundAdmin_ReevaluateTellsAChainReadFromAnInternalFailure(t *testing.T) {
	o := newRefundHandlerRig(t)
	path := "/api/admin/pay/refunds/" + uuid.NewString() + "/reevaluate"
	chain := fmt.Errorf("%w: read 0xabc on bsc: dial tcp: i/o timeout", services.ErrPayRefundChainUnreadable)
	o.h = NewPayHandler(o.merchants, o.payments, nil, false).WithRefunds(reevaluateStub{err: chain})
	if res := o.adminDo(models.RoleSuperAdmin, http.MethodPost, path); res.status != http.StatusBadGateway || res.code() != "chain_unreadable" {
		t.Fatalf("chain: %d %s", res.status, res.raw)
	}
	o.h = NewPayHandler(o.merchants, o.payments, nil, false).WithRefunds(reevaluateStub{err: errors.New("sql: database is closed")})
	res := o.adminDo(models.RoleSuperAdmin, http.MethodPost, path)
	if res.status != http.StatusInternalServerError || res.code() == "chain_unreadable" || strings.Contains(res.raw, "chain") || strings.Contains(res.raw, "sql") {
		t.Fatalf("database: %d %s", res.status, res.raw)
	}
}
