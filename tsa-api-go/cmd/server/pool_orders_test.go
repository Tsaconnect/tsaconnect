package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Production today: no pool row → no desk, no ledger, nothing to wire. The
// pool_order_max ceiling still follows the treasury's per-order cap: the
// admin console can edit pool settings while the pool is dormant.
func TestWirePoolOrders_DormantWithoutAVault(t *testing.T) {
	t.Cleanup(func() { services.SetPoolOrderMaxCeilingUSD(2000) })
	cfg := &config.Config{RampTreasuryMaxInflightUSDPerOrder: 500}
	w := wirePoolOrders(cfg, nil, nil, nil, nil)
	if w.desk != nil || w.gates != nil || w.nonces != nil || w.ledger != nil {
		t.Fatalf("dormant wiring = %+v", w)
	}
	err := services.ValidatePoolSetting(nil, models.FeaturePoolOrderMax, 0, 600)
	if err == nil || !strings.Contains(err.Error(), "at most 500 USD") {
		t.Fatalf("pool_order_max 600 over a 500 USD treasury cap: %v", err)
	}
	s := jobs.NewScheduler()
	registerPoolOrderJobs(s, w)
	if s.Count() != 0 {
		t.Fatalf("dormant registered %d job(s)", s.Count())
	}
}

// fakeRPC answers what boot asks the pool chain: its id, a block, code at any
// address, and eth_call — every call returns uint256(18) (the token's
// decimals; the gates read the rest as wrong and close, which is fine here).
func fakeRPC(t *testing.T, chainID int64) string {
	t.Helper()
	word := "0x" + strings.Repeat("0", 62) + "12"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		var result any
		switch req.Method {
		case "eth_chainId":
			result = hexutil.EncodeBig(big.NewInt(chainID))
		case "eth_blockNumber":
			result = "0x10"
		case "eth_getCode":
			result = "0x60"
		case "eth_call":
			result = word
		case "eth_gasPrice":
			result = "0x3b9aca00"
		case "eth_getBalance":
			result = "0x0"
		default:
			result = nil
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// poolDeskWorld is a BSC testnet pool row with every part the desk needs: an
// RPC client (a fake node), a router-less treasury lane and a database.
func poolDeskWorld(t *testing.T) (*config.Config, *services.BlockchainService, *services.ITService) {
	t.Helper()
	cfg := poolTestConfig(t, "testnet")
	nc := cfg.Networks["testnet"]
	bsc := nc.Chains["bsc"]
	bsc.RPCURL = fakeRPC(t, 97)
	nc.Chains["bsc"] = bsc
	cfg.Networks["testnet"] = nc
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	return cfg, services.NewBlockchainService(cfg), services.NewITService(db, nil, nil)
}

// A vault row with every part: the desk (handed to ITService), its nonce
// watch and the ledger indexer (both alert-watched), and the pinned card.
func TestWirePoolOrders_VaultRowBuildsTheDeskAndLedger(t *testing.T) {
	cfg, bs, it := poolDeskWorld(t)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	w := wirePoolOrders(cfg, bs, fakeSender{}, it, nil)
	if w.treasury == nil || w.gates == nil || w.desk == nil || w.nonces == nil || w.ledger == nil {
		t.Fatalf("wiring = %+v, want every part\n%s", w, buf.String())
	}
	if !it.HasPool() {
		t.Fatal("the desk must be handed to ITService (the pool provider and the card read it)")
	}
	if !strings.Contains(strings.ToLower(buf.String()), strings.ToLower("pool orders ready chain=bsc(97) vault="+testPoolVault)) {
		t.Fatalf("no ready line:\n%s", buf.String())
	}
	s := jobs.NewScheduler()
	registerPoolOrderJobs(s, w)
	if s.Count() != 2 {
		t.Fatalf("registered %d, want the ledger indexer and the treasury nonce watch", s.Count())
	}
	assertAlertWatched(t, s, "liquidity-vault-indexer")
	if !poolCardEnabled(t, w, it) {
		t.Fatal("a wired desk shows the pinned card")
	}
}

// No operator key: the desk is never built (it could take naira it cannot
// fill), but the ledger still keeps LP history up.
func TestWirePoolOrders_NoOperatorBuildsNoDesk(t *testing.T) {
	cfg, bs, it := poolDeskWorld(t)
	w := wirePoolOrders(cfg, bs, nil, it, nil)
	if w.desk != nil || it.HasPool() || w.ledger == nil {
		t.Fatalf("wiring = %+v hasPool=%t, want a ledger and no desk", w, it.HasPool())
	}
}

// poolCardEnabled boots the P2P handler as main does and reads the pinned card.
func poolCardEnabled(t *testing.T, w *poolWiring, it *services.ITService) bool {
	t.Helper()
	h := handlers.NewP2PHandler(nil, nil, nil, nil, nil)
	attachPoolCard(w, h, it)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/p2p/pool-card?currency=NGN", nil)
	c.Set("user", &models.User{ID: uuid.New(), VerificationStatus: "verified"})
	h.GetPoolCard(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("pool card = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data services.PoolCard `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data.Enabled
}

// assertAlertWatched starts the scheduler on a cancelled context: it reports
// its alert watch list and runs no tick.
func assertAlertWatched(t *testing.T, s *jobs.Scheduler, name string) {
	t.Helper()
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Start(ctx)
	s.Stop()
	if !strings.Contains(logs.String(), name+"(") {
		t.Fatalf("%s is not alert-watched:\n%s", name, logs.String())
	}
}

// The canceller runs with a release key, pool or not, and a silent death of
// it alerts.
func TestRegisterExpiryCanceller_AlwaysWithAReleaseKey(t *testing.T) {
	s := jobs.NewScheduler()
	trades := services.NewP2PTradeService(nil, services.NewP2PEscrowRegistryFromMap(nil, "2"), nil, nil, 146)
	registerExpiryCanceller(s, trades, &fakeSender{}, nil, nil)
	if s.Count() != 1 {
		t.Fatalf("registered %d, want the canceller", s.Count())
	}
	assertAlertWatched(t, s, "p2p-expiry-canceller")
	s2 := jobs.NewScheduler()
	registerExpiryCanceller(s2, trades, nil, nil, nil)
	if s2.Count() != 0 {
		t.Fatal("a canceller with no release key cannot send; it must not be registered")
	}
}

// The canceller's statement-freshness source is the inbound poller, and a
// missing poller is a true nil interface (the typed-nil lesson).
func TestStatementFreshness_InboundPollerOrTrueNil(t *testing.T) {
	if f := statementFreshness(nil); f != nil {
		t.Fatalf("no poller = %#v, want a nil interface", f)
	}
	p := jobs.NewInboundCreditPoller(nil, time.Minute)
	if f := statementFreshness(p); f == nil || f.(*jobs.InboundCreditPoller) != p {
		t.Fatalf("the poller must be the freshness source, got %#v", f)
	}
}

type fakeSender struct{}

func (fakeSender) Address() string                                          { return "0x00000000000000000000000000000000000000a1" }
func (fakeSender) SignAndBroadcast(context.Context, []byte) (string, error) { return "", nil }
func (fakeSender) Send(context.Context, int64, func() ([]byte, error)) (string, error) {
	return "", nil
}
