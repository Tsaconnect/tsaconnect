//go:build bsctestnet

package services

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Plan C1 Task 20: pool orders rehearsed on BSC testnet (chain 97) only, with
// throwaway keys, through the real PoolDesk, OpenPoolBuy and ExpiryCanceller.
// The gates are fakes (Plan A rehearsed the real ones); everything else is
// real: the treasury lane, its nonce watch, receipts 2 deep, and the release
// key's cancelByTimeout (permissionless on the escrow).

// rehearsalSchema defaults to C1's; LIQ_REH_SCHEMA keeps another rehearsal
// (the C2 fork and real-chain runs) in its own schema.
func rehearsalSchema() string {
	if s := os.Getenv("LIQ_REH_SCHEMA"); s != "" && strings.HasPrefix(s, "liq_") && !strings.ContainsAny(s, " ;\"'") {
		return s
	}
	return "liq_c1_rehearsal"
}

type c1Rehearsal struct {
	db     *gorm.DB
	trades *P2PTradeService
	it     *ITService
	desk   *PoolDesk
	client *blockchain.EVMClient
	lps    []c1LP
	signer func(pk string) *LocalKeyReleaseSigner
}

type c1LP struct {
	pm   *models.PaymentMerchant
	addr string
	pk   string
}

// rehearsalDB is the throwaway local Postgres only, in a schema that survives
// between the two phases.
func rehearsalDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if !strings.Contains(dsn, "@127.0.0.1:55473/") {
		t.Fatal("refusing: DATABASE_URL must be the throwaway Postgres on 127.0.0.1:55473")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	if err := admin.Exec("CREATE SCHEMA IF NOT EXISTS " + rehearsalSchema()).Error; err != nil {
		t.Fatalf("schema: %v", err)
	}
	scoped, err := testutil.DSNWithSchema(dsn, rehearsalSchema())
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(scoped), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	config.DB = db
	if err := config.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newC1Rehearsal(t *testing.T) *c1Rehearsal {
	t.Helper()
	for _, k := range []string{"LIQ_C1_LP1_PK", "LIQ_C1_LP2_PK", "LIQ_C1_LP3_PK", "LIQ_C1_TREASURY_PK", "LIQ_C1_RELEASE_PK"} {
		if os.Getenv(k) == "" {
			t.Skipf("needs %s", k)
		}
	}
	if os.Getenv("BSC_NETWORK") != "testnet" {
		t.Skip("needs BSC_NETWORK=testnet")
	}
	// A laptop on a slow link pays 1–10 s for each new TLS connection to a
	// public testnet RPC; Go keeps only 2 idle per host by default, so a burst
	// would keep re-dialling. Production sits next to its RPC.
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.MaxIdleConnsPerHost = 32
	}
	db := rehearsalDB(t)
	cfg := config.Load()
	bs := NewBlockchainService(cfg)
	reg := NewP2PEscrowRegistry(bs, cfg)
	pool, ok := reg.PoolChain()
	if !ok || pool.ChainID != 97 {
		t.Fatalf("pool chain = %+v %v: refusing anything but BSC testnet", pool, ok)
	}
	client, _ := bs.ClientForChainID(97)
	escrow, err := reg.For(97)
	if client == nil || err != nil {
		t.Fatalf("client/escrow: %v", err)
	}
	if id, err := client.Client.ChainID(context.Background()); err != nil || id.Int64() != 97 {
		t.Fatalf("RPC chain id = %v %v: refusing anything but BSC testnet", id, err)
	}
	if err := CheckPoolPaymentWindow(escrow); err != nil { // re-review N7, as boot checks it
		t.Fatalf("testnet escrow window: %v", err)
	}
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil // every pool setting at its default (P5)

	trades := NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146)
	trades.SetTokenResolver(bs.TokenAddressForChainID)
	pm := NewPMService(db)
	trades.SetPMService(pm)
	trades.SetPoolGate(fakePoolOpen(true))

	stock := NewPoolStock(reg, bs.TokenAddressForChainID)
	tok, ok := stock.Token()
	if !ok {
		t.Fatal("no pool token")
	}
	tKey, err := crypto.HexToECDSA(trim0x(os.Getenv("LIQ_C1_TREASURY_PK")))
	if err != nil {
		t.Fatal("bad treasury key")
	}
	lane := &SponsorLane{
		ChainKey: pool.ChainKey, Chain: EVMRelayChain{Client: client}, Builder: EVMRelayTxBuilder{Client: client}, Native: client,
		Signer: NewLocalKeyReleaseSigner(tKey, client), Guard: NewSponsorGuard("liq_c1_rehearsal", SponsorLimits{}, nil),
		Tokens: map[string]string{tok.Symbol: tok.Address},
	}
	tr := NewTreasury(map[string]*TreasuryLane{pool.ChainKey: {
		SponsorLane: lane, ChainID: 97, EscrowCalls: []common.Address{common.HexToAddress(escrow.ContractAddress())},
	}}, TreasuryCaps{}, nil)
	nonces := NewTreasuryNonceWatch(pool.ChainKey, client.Probe(), tr.Address(), 30*time.Second, nil)

	// The company PM's user is the slices' DB buyer (S10); a stable id across phases.
	buyer := uuid.NewSHA1(uuid.NameSpaceOID, []byte("liq-c1-rehearsal-buyer"))
	desk, err := NewPoolDesk(PoolDeskConfig{DB: db, PM: pm, Trades: trades, Stock: stock, Token: tok, Escrow: escrow,
		EscrowAddr: escrow.ContractAddress(), Treasury: tr, Gates: &fakePoolGate{open: true, gasOK: true}, Probe: client.Probe(),
		Generation: client.EndpointGeneration, Nonces: nonces, BuyerUserID: buyer, ChainName: "BNB Smart Chain Testnet"})
	if err != nil {
		t.Fatalf("desk: %v", err)
	}
	it := NewITService(db, nil, nil)
	it.SetInboundCentral(true, testCentralAccount())
	it.SetPoolDesk(desk)
	trades.SetPoolSlices(it, nil) // F21 is not rehearsed here

	r := &c1Rehearsal{db: db, trades: trades, it: it, desk: desk, client: client}
	r.signer = func(pk string) *LocalKeyReleaseSigner {
		k, err := crypto.HexToECDSA(trim0x(pk))
		if err != nil {
			t.Fatal("bad LP key")
		}
		return NewLocalKeyReleaseSigner(k, client)
	}
	for _, env := range []string{"LIQ_C1_LP1_PK", "LIQ_C1_LP2_PK", "LIQ_C1_LP3_PK"} {
		pk := os.Getenv(env)
		k, err := crypto.HexToECDSA(trim0x(pk))
		if err != nil {
			t.Fatalf("bad key %s", env)
		}
		addr := crypto.PubkeyToAddress(k.PublicKey).Hex()
		var lp models.PaymentMerchant
		if db.Where("lower(operator_wallet_address) = ?", strings.ToLower(addr)).First(&lp).Error != nil {
			lp = *seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, addr)
		}
		r.lps = append(r.lps, c1LP{pm: &lp, addr: addr, pk: pk})
	}
	return r
}

// warmRPC opens a few connections to the pool chain's RPC right before a
// timed step, so the 8 s open cap measures the open, not TLS handshakes.
func (r *c1Rehearsal) warmRPC(t *testing.T) {
	t.Helper()
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = r.client.Client.BlockNumber(ctx)
		}()
	}
	wg.Wait()
	t.Logf("RPC warmed in %s", time.Since(start))
}

// reconcilePass is one tick of the chain reconciler (jobs.ChainReconciler.Run,
// every 30 s in prod): the canceller only sends cancelByTimeout, and the
// reconciler moves a slice the chain shows Cancelled to cancelled. The job is
// mirrored rather than imported: jobs imports services, so this package's
// tests cannot import it.
func (r *c1Rehearsal) reconcilePass(t *testing.T, ctx context.Context) {
	t.Helper()
	trades, err := r.trades.ListNonTerminalTrades(ctx, 200)
	if err != nil {
		t.Fatalf("list non-terminal: %v", err)
	}
	for _, tr := range trades {
		out, err := r.trades.ReconcileTrade(ctx, tr.ID)
		switch {
		case err != nil:
			t.Logf("reconcile %s: %v", tr.ID, err)
		case out.Changed:
			t.Logf("reconcile %s: %s -> %s (%s)", tr.ID, out.FromStatus, out.ToStatus, out.Reason)
		}
	}
}

// seedRehearsalBuyer is seedVerifiedUser for the real users table: Postgres
// needs a username and password, and wallet_address is unique.
func seedRehearsalBuyer(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := db.Exec(
		`INSERT INTO users (id, name, username, email, password, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "Ada Okafor", "reh-"+id.String(), id.String()+"@test.com", "x", common.BytesToAddress(id[:]).Hex(),
		models.VerificationStatusVerified, "Ada Okafor",
	).Error; err != nil {
		t.Fatalf("seed buyer: %v", err)
	}
	return id
}

func (r *c1Rehearsal) send(t *testing.T, ctx context.Context, pk, label string, raw []byte, err error) string {
	t.Helper()
	if err != nil {
		t.Fatalf("%s prepare: %v", label, err)
	}
	hash, err := r.signer(pk).SignAndBroadcast(ctx, raw)
	if err != nil {
		t.Fatalf("%s broadcast: %v", label, err)
	}
	if _, err := r.client.WaitForReceipt(hash, 90*time.Second); err != nil {
		t.Fatalf("%s receipt: %v", label, err)
	}
	t.Logf("%s %s", label, hash)
	return hash
}

// fund deposits 6 USDT for an LP and signs its stock ad (Plan B's flow). A
// re-run skips whatever an earlier run already did, so no gas is spent twice.
func (r *c1Rehearsal) fund(t *testing.T, ctx context.Context, lp c1LP) {
	t.Helper()
	six := usdt18(6)
	bal, err := r.trades.GetPoolBalance(ctx, lp.addr, "USDT", 97)
	if err != nil {
		t.Fatalf("pool balance: %v", err)
	}
	if bal.Available.Cmp(six) < 0 {
		raw, err := r.trades.PrepareApprove(ctx, lp.pm.UserID, lp.addr, "USDT", six.String(), 97)
		r.send(t, ctx, lp.pk, "approve", raw, err)
		raw, err = r.trades.PrepareDeposit(ctx, lp.pm.UserID, lp.addr, "USDT", six.String(), 97)
		h := r.send(t, ctx, lp.pk, "deposit", raw, err)
		if _, err := r.trades.ConfirmDeposit(ctx, lp.pm.UserID, lp.addr, "USDT", six.String(), h, 97); err != nil {
			t.Fatalf("confirm deposit: %v", err)
		}
	} else {
		t.Logf("LP %s already holds %s free in the pool", lp.addr, bal.Available)
	}
	if ad, err := r.trades.LatestStockAd(ctx, lp.pm, 97, "USDT"); err == nil && ad != nil && ad.Status == models.P2PAdStatusActive {
		t.Logf("LP %s stock ad %s already active", lp.addr, ad.ID)
		return
	}
	res, err := r.trades.CreateAd(ctx, lp.pm.UserID, lp.addr, CreateAdParams{Token: "USDT", TokenAmount: "1", FiatCurrency: "NGN", ChainID: 97})
	if err != nil {
		t.Fatalf("stock ad: %v", err)
	}
	digest, _ := decodeHex32(res.Ad.VoucherHash)
	k, _ := crypto.HexToECDSA(trim0x(lp.pk))
	sig, _ := crypto.Sign(digest[:], k)
	sig[64] += 27
	if ad, err := r.trades.SignAd(ctx, res.Ad.ID, lp.pm.UserID, lp.addr, sig); err != nil || ad.Status != models.P2PAdStatusActive {
		t.Fatalf("sign: %v", err)
	}
	t.Logf("LP %s stock ad %s signed", lp.addr, res.Ad.ID)
}

// Phase 1 (P21): a ₦20,000 order at the test mid is ~14.7 USDT, so three
// LPs with 6 USDT each must all be sliced (6 + 6 + ~2.7), broadcast back to
// back on the treasury lane (distinct nonces, review I2), and every slice
// proven, before the pay-in exists.
func TestPoolOrdersRehearsal_Open_BSCTestnet(t *testing.T) {
	r := newC1Rehearsal(t)
	ctx := context.Background()
	for _, lp := range r.lps {
		r.fund(t, ctx, lp)
	}
	user := seedRehearsalBuyer(t, r.db)
	p, err := r.it.QuotePoolBuy(context.Background(), "USDT", "20000", "NGN", poolMid, specPoolSettings())
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	r.warmRPC(t)
	octx, cancel := context.WithTimeout(ctx, PoolOpenCap)
	defer cancel()
	start := time.Now()
	intent, pay, err := r.it.OpenPoolBuy(octx, user, p, testRoutedEVM)
	if err != nil || pay == nil {
		t.Fatalf("open = %v (after %s)", err, time.Since(start))
	}
	t.Logf("open took %s; intent %s units=%s created_at=%s", time.Since(start), intent.ID, intent.AssetAmount, intent.CreatedAt.UTC().Format(time.RFC3339))
	if intent.Status != models.ITStatusPendingPayment {
		t.Fatalf("intent status %s", intent.Status)
	}
	var slices []models.P2PTrade
	r.db.Where("it_intent_id = ? AND dropped_at IS NULL", intent.ID).Find(&slices)
	if len(slices) != 3 {
		t.Fatalf("%d live slices, want 3", len(slices))
	}
	nonces := map[int64]bool{}
	for _, sl := range slices {
		if sl.OpenNonce != nil {
			nonces[*sl.OpenNonce] = true
		}
	}
	if len(nonces) != 3 {
		t.Fatalf("open nonces = %v, want three distinct", nonces)
	}
	escrow := r.desk.c.Escrow
	for _, sl := range slices {
		if sl.OpenedAt == nil || sl.OpenNonce == nil {
			t.Fatalf("slice %s not proven: %+v", sl.ID, sl)
		}
		id, _ := decodeHex32(sl.OnChainTradeID)
		on, err := escrow.ReadTrade(id)
		if err != nil || on.Status != OnChainP2PStatusPending {
			t.Fatalf("slice %s on-chain = %+v %v, want Pending", sl.ID, on, err)
		}
		t.Logf("slice %s trade=%s units=%s nonce=%d tx=%s opened_at=%s", sl.ID, sl.OnChainTradeID, sl.TokenAmount, *sl.OpenNonce,
			sl.OpenTxHash, sl.OpenedAt.UTC().Format(time.RFC3339))
	}
	t.Log("phase 1 PASS — run phase 2 after the 60-minute window, with no payment")
}

// Phase 2: nobody paid. The canceller frees every slice on-chain; the LPs
// withdraw their stock.
func TestPoolOrdersRehearsal_Cancel_BSCTestnet(t *testing.T) {
	r := newC1Rehearsal(t)
	ctx := context.Background()
	var intent models.ITIntent
	if err := r.db.Where("provider = ?", models.ITProviderPool).Order("created_at DESC").First(&intent).Error; err != nil {
		t.Fatalf("no phase-1 order: %v", err)
	}
	relKey, err := crypto.HexToECDSA(trim0x(os.Getenv("LIQ_C1_RELEASE_PK")))
	if err != nil {
		t.Fatal("bad release key")
	}
	release := NewChainReleaseSigner(relKey, map[int64]ReleaseBroadcaster{97: r.client})
	ec := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.trades, Signer: release, Pool: r.it, Fresh: fixedFresh{at: time.Now()}})

	deadline := time.Now().Add(5 * time.Minute)
	for {
		tick, err := ec.RunOnce(ctx)
		t.Logf("tick %+v err=%v", tick, err)
		r.reconcilePass(t, ctx)
		var live int64
		r.db.Model(&models.P2PTrade{}).Where("it_intent_id = ? AND status <> ?", intent.ID, models.P2PTradeStatusCancelled).Count(&live)
		if live == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d slices still live after 5 minutes of ticks", live)
		}
		time.Sleep(15 * time.Second)
	}
	var slices []models.P2PTrade
	r.db.Where("it_intent_id = ?", intent.ID).Find(&slices)
	for _, sl := range slices {
		id, _ := decodeHex32(sl.OnChainTradeID)
		if on, err := r.desk.c.Escrow.ReadTrade(id); err != nil || on.Status != OnChainP2PStatusCancelled {
			t.Fatalf("slice %s on-chain = %+v %v, want Cancelled", sl.ID, on, err)
		}
		t.Logf("slice %s trade=%s cancelled on-chain", sl.ID, sl.OnChainTradeID)
	}
	for _, lp := range r.lps {
		bal, err := r.trades.GetPoolBalance(ctx, lp.addr, "USDT", 97)
		if err != nil {
			t.Fatalf("pool balance: %v", err)
		}
		if bal.Locked.Sign() != 0 {
			t.Fatalf("LP %s still has %s locked", lp.addr, bal.Locked)
		}
		if bal.Available.Sign() == 0 {
			t.Logf("LP %s has nothing to withdraw", lp.addr)
			continue
		}
		amount := bal.Available.String()
		raw, err := r.trades.PrepareWithdraw(ctx, lp.pm.UserID, lp.addr, "USDT", amount, 97)
		h := r.send(t, ctx, lp.pk, "withdraw", raw, err)
		if _, err := r.trades.ConfirmWithdraw(ctx, lp.pm.UserID, lp.addr, "USDT", amount, h, 97); err != nil {
			t.Fatalf("confirm withdraw: %v", err)
		}
	}
	t.Log("phase 2 PASS")
}
