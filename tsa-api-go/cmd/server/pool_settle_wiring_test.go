package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// P15: with the settle path wired, a mainnet pool row builds the desk — the
// C1 guard is gone, and nothing logs "plan C2".
func TestWirePoolOrders_MainnetRowBuildsTheSettleDesk(t *testing.T) {
	cfg, bs, reg, trades, it := poolDeskWorldOn(t, "mainnet", 56, 60*time.Minute)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	w := wirePoolOrders(cfg, bs, reg, trades, services.NewPMService(config.DB), fakeSender{}, it, nil)
	if w.desk == nil || strings.Contains(buf.String(), "plan C2") {
		t.Fatalf("mainnet wiring = %+v\n%s", w, buf.String())
	}
}

// A desk with no release key cannot settle: boot leaves orders dormant.
func TestWirePoolOrders_NoReleaseKeyBuildsNoDesk(t *testing.T) {
	cfg, bs, reg, trades, it := poolDeskWorld(t, 60*time.Minute)
	w := wirePoolOrders(cfg, bs, reg, trades, services.NewPMService(config.DB), nil, it, nil)
	if w.desk != nil || it.HasPool() {
		t.Fatal("no release key: no desk, no pool orders")
	}
}

// Review I-4: no LP forwarder, no desk — the pool never takes naira it
// cannot pay its LPs from.
func TestWirePoolOrders_NoForwarderBuildsNoDesk(t *testing.T) {
	cfg, bs, reg, trades, it := poolDeskWorld(t, 60*time.Minute)
	it.SetPoolForwards(nil)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	w := wirePoolOrders(cfg, bs, reg, trades, services.NewPMService(config.DB), fakeSender{}, it, nil)
	if w.desk != nil || it.HasPool() || !strings.Contains(buf.String(), "liquidity_pool_orders_dormant") {
		t.Fatalf("desk=%v\n%s", w.desk, buf.String())
	}
}

// The console routes and the dedicated settle job (alert-watched) come with the desk.
func TestAttachPoolSettle_WiresAdminAndTheSettleJob(t *testing.T) {
	cfg, bs, reg, trades, it := poolDeskWorld(t, 60*time.Minute)
	w := wirePoolOrders(cfg, bs, reg, trades, services.NewPMService(config.DB), fakeSender{}, it, nil)
	h := handlers.NewITHandler(nil, nil, nil)
	attachPoolSettle(w, it, h)
	if !h.PoolOrdersEnabled() {
		t.Fatal("the pool admin routes must register with the desk")
	}
	s := jobs.NewScheduler()
	registerPoolSettleJob(s, w, it, nil)
	if s.Count() != 1 {
		t.Fatalf("registered %d, want the liquidity-pool-settle job", s.Count())
	}
	assertAlertWatched(t, s, "liquidity-pool-settle")
	s2 := jobs.NewScheduler()
	registerPoolSettleJob(s2, &poolWiring{}, it, nil)
	if s2.Count() != 0 {
		t.Fatal("no desk, no settle job")
	}
}

// A config with no pool row (any chain whose preset is emptied) stays dormant
// even with every settle part available (release key, LP forwarder, admin
// handler): no pool orders, no settle job, no pool console routes. The shipped
// config carries the BSC mainnet pool since the 2026-10-13 rollout, so the
// pool rows are dropped here to keep exercising the dormant path.
func TestBoot_NoPoolRowStaysDormant(t *testing.T) {
	t.Setenv("BSC_NETWORK", "")
	_, _, _, trades, it := poolDeskWorld(t, 60*time.Minute) // a forwarder-wired ITService
	cfg := config.Load()
	if pool, ok := cfg.PoolChain(); !ok || pool.ChainID != 56 {
		t.Fatalf("shipped pool chain = %+v ok=%t, want BSC mainnet (56)", pool, ok)
	}
	for id, cc := range cfg.P2PChains {
		if cc.Purpose == config.P2PPurposePool {
			delete(cfg.P2PChains, id)
		}
	}
	if _, ok := cfg.PoolChain(); ok {
		t.Fatal("pool rows not dropped")
	}
	reg := services.NewP2PEscrowRegistryFromMap(nil, "2")
	w := wirePoolOrders(cfg, services.NewBlockchainService(cfg), reg, trades, services.NewPMService(config.DB), fakeSender{}, it, nil)
	if w.desk != nil || w.gates != nil || it.HasPool() {
		t.Fatalf("prod wiring = %+v hasPool=%t", w, it.HasPool())
	}
	h := handlers.NewITHandler(nil, nil, nil)
	attachPoolSettle(w, it, h)
	s := jobs.NewScheduler()
	registerPoolSettleJob(s, w, it, nil)
	if h.PoolOrdersEnabled() || s.Count() != 0 {
		t.Fatalf("dormant pool: admin routes=%t settle jobs=%d", h.PoolOrdersEnabled(), s.Count())
	}
}
