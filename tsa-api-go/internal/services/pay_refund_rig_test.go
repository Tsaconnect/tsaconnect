package services

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	refundBSCUSDT       = "0x55d398326f99059fF775485246999027B3197955" // 18 decimals
	refundBaseUSDC      = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" // 6 decimals
	refundPayerWallet   = "0x000000000000000000000000000000000000dEaD" // seedUserAt's wallet
	refundFundingWallet = "0x1111111111111111111111111111111111111111"
)

var (
	fakeRefundChains   = map[int64]string{56: "bsc", 8453: "base"}
	fakeRefundTokens   = map[string]string{"bsc:USDT": refundBSCUSDT, "base:USDC": refundBaseUSDC}
	fakeRefundDecimals = map[string]int{strings.ToLower(refundBSCUSDT): 18, strings.ToLower(refundBaseUSDC): 6}
)

// fakeRefundNetwork is two mainnet chains: BSC listing 18-decimal USDT, Base
// listing 6-decimal USDC. It records broadcasts and answers one nonce.
type fakeRefundNetwork struct {
	mu           sync.Mutex
	sent         []*types.Transaction
	broadcastErr error
	// sentCtxErrs is ctx.Err() as each broadcast saw it.
	sentCtxErrs []error
	// nonceDepths is the depth each ConfirmedNonce read asked for.
	nonceDepths []uint64
	nonce       uint64
	nonceErr    error
	decimalsErr error
	// onBroadcast, onTokenDecimals and onConfirmedNonce, when set, run inside
	// those calls.
	onBroadcast      func()
	onTokenDecimals  func()
	onConfirmedNonce func()
	// generation is the RPC endpoint generation every chain reports.
	generation uint64
}

func (f *fakeRefundNetwork) MainnetChainNameForID(id int64) (string, bool) {
	n, ok := fakeRefundChains[id]
	return n, ok
}
func (f *fakeRefundNetwork) MainnetChainIDs() []int64 {
	ids := []int64{}
	for id := range fakeRefundChains {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
func (f *fakeRefundNetwork) TokenAddress(chain, symbol string) string {
	return fakeRefundTokens[chain+":"+symbol]
}
func (f *fakeRefundNetwork) ChainName(chain string) string {
	return map[string]string{"bsc": "BNB Smart Chain", "base": "Base"}[chain]
}
func (f *fakeRefundNetwork) TokenDecimals(_ string, token string) (int, error) {
	if f.onTokenDecimals != nil {
		f.onTokenDecimals()
	}
	if f.decimalsErr != nil {
		return 0, f.decimalsErr
	}
	d, ok := fakeRefundDecimals[strings.ToLower(token)]
	if !ok {
		return 0, errors.New("no decimals for " + token)
	}
	return d, nil
}
func (f *fakeRefundNetwork) Broadcast(ctx context.Context, _ string, tx *types.Transaction) error {
	if f.onBroadcast != nil {
		f.onBroadcast()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, tx)
	f.sentCtxErrs = append(f.sentCtxErrs, ctx.Err())
	return f.broadcastErr
}
func (f *fakeRefundNetwork) ConfirmedNonce(_ context.Context, _ string, _ common.Address, depth uint64) (uint64, error) {
	if f.onConfirmedNonce != nil {
		f.onConfirmedNonce()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonceDepths = append(f.nonceDepths, depth)
	return f.nonce, f.nonceErr
}
func (f *fakeRefundNetwork) Endpoint(string) RPCEndpoint { return fakeRefundEndpoint{f} }

// fakeRefundEndpoint reports the fake network's endpoint generation.
type fakeRefundEndpoint struct{ f *fakeRefundNetwork }

func (e fakeRefundEndpoint) EndpointGeneration() uint64 {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	return e.f.generation
}

func (f *fakeRefundNetwork) broadcasts() []*types.Transaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*types.Transaction(nil), f.sent...)
}

// fakeRefundVerifier answers every proof with one reading. knownAnswers, when
// set, answers TransactionKnown in turn before falling back to known.
type fakeRefundVerifier struct {
	mu sync.Mutex
	// verified is each Verify call's (chain, txHash, token, from, to).
	verified     [][5]string
	payment      *StablePayment
	err          error
	known        bool
	knownAnswers []bool
	knownErr     error
}

func (f *fakeRefundVerifier) set(p *StablePayment, err error, known bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payment, f.err, f.known = p, err, known
}
func (f *fakeRefundVerifier) Verify(_ context.Context, chain, txHash, token, from, to string) (*StablePayment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verified = append(f.verified, [5]string{chain, txHash, token, from, to})
	return f.payment, f.err
}
func (f *fakeRefundVerifier) TransactionKnown(context.Context, string, string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.knownAnswers) > 0 {
		k := f.knownAnswers[0]
		f.knownAnswers = f.knownAnswers[1:]
		return k, "", f.knownErr
	}
	return f.known, "", f.knownErr
}

type refundRig struct {
	s           *PayRefundService
	pay         *PayPaymentService
	db          *gorm.DB
	m           *models.PayMerchant
	net         *fakeRefundNetwork
	ver         *fakeRefundVerifier
	ownerKey    *ecdsa.PrivateKey
	ownerWallet string
	payer       uuid.UUID
	alerts      func() []events.Event
}

// newRefundRig is a live, naira-settled merchant whose owner has a proven
// wallet, a verified payer (Ada Okafor, wallet 0x…dEaD, not proven), and a
// refund service over the two-chain fake network. The verifier starts at "not
// mined, but the node has it" — a submitted refund just waits.
func newRefundRig(t *testing.T) *refundRig {
	t.Helper()
	db := newPayTestDB(t)
	return newRefundRigOn(t, db)
}

// newRefundRigOn is newRefundRig over db, which already has the pay tables and
// a users table with account_status (SQLite or Postgres).
func newRefundRigOn(t *testing.T, db *gorm.DB) *refundRig {
	t.Helper()
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}, &models.RampSellIntent{}, &models.WalletLinkChallenge{}); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return payTestNow }
	net := &fakeRefundNetwork{}
	ver := &fakeRefundVerifier{err: errors.New("verify: not found"), known: true}
	merchants := NewPayMerchantService(db, nil, net, []byte(strings.Repeat("k", 32)), now)
	pay := NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now)
	s := NewPayRefundService(db, pay, merchants, net, ver, 5, now)
	bus := events.NewBus()
	alerts := collectPayAlerts(bus)
	s.SetEvents(bus)
	pay.SetRefundAggregator(s)

	m := seedPayMerchant(t, db)
	if err := db.Model(m).Updates(map[string]any{"webhook_url": "https://skyair.ng/hook", "test_webhook_url": "https://skyair.ng/test-hook"}).Error; err != nil {
		t.Fatal(err)
	}
	m.WebhookURL, m.TestWebhookURL = "https://skyair.ng/hook", "https://skyair.ng/test-hook"
	key, _ := crypto.GenerateKey()
	wallet := crypto.PubkeyToAddress(key.PublicKey).Hex()
	if err := db.Exec(`INSERT INTO users (id, name, email, wallet_address) VALUES (?, 'Sky Owner', 'owner@skyair.ng', ?)`, m.UserID, wallet).Error; err != nil {
		t.Fatal(err)
	}
	proveWallet(t, db, m.UserID, wallet)
	return &refundRig{s: s, pay: pay, db: db, m: m, net: net, ver: ver, ownerKey: key, ownerWallet: wallet,
		payer: seedVerifiedUser(t, db), alerts: alerts}
}

// proveWallet records that userID proved it owns wallet (a used challenge).
func proveWallet(t *testing.T, db *gorm.DB, userID uuid.UUID, wallet string) {
	t.Helper()
	used := payTestNow
	if err := db.Create(&models.WalletLinkChallenge{UserID: userID, Address: strings.ToLower(wallet), Nonce: uuid.NewString(),
		Message: "proof", ExpiresAt: payTestNow.Add(time.Hour), UsedAt: &used}).Error; err != nil {
		t.Fatal(err)
	}
}

// paid is a succeeded payment of mode under reference ref, paid by the rig's
// payer at payTestNow, with updates applied on top.
func (r *refundRig) paid(t *testing.T, mode, ref string, updates map[string]any) *models.PayPayment {
	t.Helper()
	in := payIn()
	in.Reference = ref
	p, _, err := r.pay.Create(context.Background(), r.m, mode, in)
	if err != nil {
		t.Fatal(err)
	}
	at := payTestNow
	all := map[string]any{"status": models.PayStatusSucceeded, "payer_user_id": r.payer, "succeeded_at": &at, "attempted_at": &at, "locked_at": &at}
	for k, v := range updates {
		all[k] = v
	}
	if err := r.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Updates(all).Error; err != nil {
		t.Fatal(err)
	}
	got, err := r.pay.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// paidDirect is a live naira payment whose Paycrest order took tokenAmount
// USDT on BSC from the funding wallet, with returned coming back.
func (r *refundRig) paidDirect(t *testing.T, ref, tokenAmount, returned string) *models.PayPayment {
	t.Helper()
	return r.paidByOrder(t, ref, "bsc", "USDT", tokenAmount, returned)
}

// paidByOrder is a live naira payment whose settled Paycrest order took
// tokenAmount of token on chainKey from the funding wallet: USDT on BSC for a
// direct payment, the swap's Base-USDC deposit for a converted one.
func (r *refundRig) paidByOrder(t *testing.T, ref, chainKey, token, tokenAmount, returned string) *models.PayPayment {
	t.Helper()
	order := models.PaycrestOrderRecord{ID: uuid.New(), UserID: r.payer, Direction: "sell", Status: models.PaycrestStatusSettled,
		Reference: "order-" + ref, FiatCode: "NGN", FiatAmount: "50500", Token: token, ChainKey: chainKey, Network: chainKey,
		TokenAmount: tokenAmount, AmountReturned: returned, WalletAddress: refundFundingWallet}
	if err := r.db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	p := r.paid(t, models.PayModeLive, ref, map[string]any{"paycrest_order_id": order.ID})
	if err := r.db.Model(&order).Update("pay_payment_id", p.ID).Error; err != nil {
		t.Fatal(err)
	}
	return p
}

// paidCrypto is a live payment settled in token (symbol) on chainID, whose
// intent owed the merchant destAmount base units: ₦50,500 paid for ₦50,000.
func (r *refundRig) paidCrypto(t *testing.T, ref string, chainID int64, token, symbol, destAmount string) *models.PayPayment {
	t.Helper()
	in := models.RampSellIntent{ID: uuid.New(), UserID: r.payer, SourceAddress: refundFundingWallet, WalletAddress: refundFundingWallet,
		SourceChainID: chainID, SourceTokenAddress: token, SourceAmount: "1", FiatCode: "NGN", Status: models.SellIntentDelivered,
		DestChainID: chainID, DestTokenAddress: token, DestAddress: "0x2222222222222222222222222222222222222222", DestAmount: destAmount}
	if err := r.db.Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	return r.paid(t, models.PayModeLive, ref, map[string]any{"settlement_mode": models.PaySettlementCrypto,
		"settle_chain_id": chainID, "settle_token_address": token, "settle_token_symbol": symbol,
		"settle_wallet_address": "0x2222222222222222222222222222222222222222", "sell_intent_id": in.ID,
		"customer_total_kobo": int64(5_050_000), "merchant_net_kobo": int64(5_000_000)})
}

// insertRefund writes a refund row as-is (cap $33), bypassing Request.
func (r *refundRig) insertRefund(t *testing.T, p *models.PayPayment, status string, amount int64) *models.PayRefund {
	t.Helper()
	ref := &models.PayRefund{PaymentID: p.ID, MerchantID: p.MerchantID, Mode: p.Mode, AmountMicroUSD: amount, CapMicroUSD: 33_000_000,
		ToAddress: refundPayerWallet, Status: status, RequestedVia: models.PayRefundViaWeb, CreatedAt: payTestNow}
	if status == models.PayRefundConfirmed {
		at := payTestNow
		ref.ConfirmedAt, ref.CreditedMicroUSD = &at, amount
	}
	if err := r.db.Create(ref).Error; err != nil {
		t.Fatal(err)
	}
	return ref
}

// webhooks is every queued delivery of event, as the decoded payload.
func (r *refundRig) webhooks(t *testing.T, event string) []map[string]any {
	t.Helper()
	var ds []models.PayWebhookDelivery
	if err := r.db.Where("event = ?", event).Order("created_at").Find(&ds).Error; err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]any, len(ds))
	for i, d := range ds {
		if err := json.Unmarshal([]byte(d.Payload), &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// alertsOf is the published events of one type.
func (r *refundRig) alertsOf(kind string) []events.Event {
	var out []events.Event
	for _, e := range r.alerts() {
		if e.Type == kind {
			out = append(out, e)
		}
	}
	return out
}
