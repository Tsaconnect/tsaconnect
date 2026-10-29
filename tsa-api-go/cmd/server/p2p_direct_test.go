package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func directLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// Every hot key the API holds and the known production operator and treasury may hold no role on
// a core contract; unset and malformed keys are skipped.
func TestAPIHotKeys_EveryKnownHotKey(t *testing.T) {
	cfg := &config.Config{PMReleasePrivateKey: testKeyA, RelayerPrivateKey: "0x" + testKeyB, RampTreasuryPrivateKey: "not-a-key",
		PMAdminEOAAddress: "0x00000000000000000000000000000000000038cE"}
	got := apiHotKeys(cfg)
	want := map[string]string{
		"PM_RELEASE_PRIVATE_KEY": signerAddress(testKeyA),
		// IT operator and gas sponsor fall back to the operator key.
		"IT_OPERATOR_PRIVATE_KEY":  signerAddress(testKeyA),
		"RAMP_SPONSOR_PRIVATE_KEY": signerAddress(testKeyA),
		"RELAYER_PRIVATE_KEY":      signerAddress(testKeyB),
		"PM_ADMIN_EOA_ADDRESS":     "0x00000000000000000000000000000000000038cE",
	}
	for label, addr := range knownHotKeys {
		want[label] = addr
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if !strings.EqualFold(got[k], v) {
			t.Fatalf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if n := len(apiHotKeys(&config.Config{})); n != len(knownHotKeys) {
		t.Fatalf("no keys configured: %d addresses, want only the %d known hot keys", n, len(knownHotKeys))
	}
}

func escrowMoveDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE p2p_trades (id TEXT PRIMARY KEY, chain_id INTEGER, status TEXT, it_intent_id TEXT, escrow_address TEXT)`,
		`CREATE TABLE p2p_ads (id TEXT PRIMARY KEY, chain_id INTEGER, status TEXT, side TEXT, pm_id TEXT, escrow_address TEXT)`,
		`CREATE TABLE payment_merchants (id TEXT PRIMARY KEY, kind TEXT)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// The hard switch: boot refuses while a live trade or an openable sell-ad sits on any escrow but
// its chain's P2PTradeEscrow. Terminal rows, retired slices, LP stock ads, buy-ads and unsigned
// ads do not count; nor does case.
func TestCheckP2PEscrowMove(t *testing.T) {
	const core = "0x00000000000000000000000000000000000E5C03"
	const v3 = "0xD16c1597EA3be8D2017698CC5d6Bf53b595EEb03"
	db := escrowMoveDB(t)
	cfg := &config.Config{CoreSet: config.CoreSetDev, P2PChains: map[int64]config.P2PChainConfig{56: {ChainID: 56, EscrowAddress: core}}}
	trade := func(chain int, status, escrow string, slice bool) {
		var intent any
		if slice {
			intent = uuid.NewString()
		}
		db.Exec(`INSERT INTO p2p_trades (id, chain_id, status, it_intent_id, escrow_address) VALUES (?, ?, ?, ?, ?)`, uuid.NewString(), chain, status, intent, escrow)
	}
	ad := func(chain int, status, side, escrow string, pm any) {
		db.Exec(`INSERT INTO p2p_ads (id, chain_id, status, side, pm_id, escrow_address) VALUES (?, ?, ?, ?, ?, ?)`, uuid.NewString(), chain, status, side, pm, escrow)
	}
	trade(56, models.P2PTradeStatusReleased, "", false)
	trade(56, models.P2PTradeStatusPendingPayment, "", true)
	trade(56, models.P2PTradeStatusPaid, strings.ToLower(core), false)
	ad(56, models.P2PAdStatusActive, models.P2PAdSideBuy, "", nil)
	ad(56, models.P2PAdStatusPending, models.P2PAdSideSell, "", nil)
	ad(56, models.P2PAdStatusActive, models.P2PAdSideSell, core, nil)
	lp := uuid.NewString()
	db.Exec(`INSERT INTO payment_merchants (id, kind) VALUES (?, ?)`, lp, models.PaymentMerchantKindLP)
	ad(56, models.P2PAdStatusActive, models.P2PAdSideSell, "", lp)
	if err := checkP2PEscrowMove(db, cfg); err != nil {
		t.Fatalf("nothing live on another escrow: %v", err)
	}

	ad(56, models.P2PAdStatusPaused, models.P2PAdSideSell, v3, nil)
	if err := checkP2PEscrowMove(db, cfg); err == nil || !strings.Contains(err.Error(), "p2p_escrow_move_refused") || !strings.Contains(err.Error(), "sell-ads on chain 56") {
		t.Fatalf("a paused V3 sell-ad: %v", err)
	}
	trade(146, models.P2PTradeStatusPendingPayment, "", false) // a chain with no P2P after the switch
	err := checkP2PEscrowMove(db, cfg)
	if err == nil || !strings.Contains(err.Error(), "trades on chain 146 at the pre-switch escrow") {
		t.Fatalf("a live trade on a chain without P2P: %v", err)
	}

	// No core set (prod before cutover): P2P is dormant, so the same rows only alert.
	buf := directLog(t)
	if err := checkP2PEscrowMove(db, &config.Config{}); err != nil {
		t.Fatalf("dormant P2P must boot: %v", err)
	}
	if !strings.Contains(buf.String(), "ALERT p2p_escrow_move_pending") {
		t.Fatalf("no alert:\n%s", buf.String())
	}
}

// A read error is an ALERT, never a refusal.
func TestCheckP2PEscrowMove_ReadErrorAlertsAndBoots(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{}) // no tables
	if err != nil {
		t.Fatal(err)
	}
	buf := directLog(t)
	if err := checkP2PEscrowMove(db, &config.Config{}); err != nil {
		t.Fatalf("a read error refused boot: %v", err)
	}
	if !strings.Contains(buf.String(), "ALERT p2p_escrow_move_unchecked") {
		t.Fatalf("no alert:\n%s", buf.String())
	}
}

// Plan API-7: no lane without a registered non-Sonic P2P chain, nor without a
// sponsor key — never a new key or env var.
func TestBuildP2PChainGasSponsorships_Dormant(t *testing.T) {
	cfg := &config.Config{P2PChains: map[int64]config.P2PChainConfig{
		146: {ChainID: 146, ChainKey: "sonic"}, 56: {ChainID: 56, ChainKey: "bsc", Network: "mainnet"}}}
	if got := buildP2PChainGasSponsorships(cfg, nil, []int64{56}); len(got) != 0 {
		t.Fatalf("no chain registry: %v", got)
	}
	buf := directLog(t)
	if got := buildP2PChainGasSponsorships(cfg, services.NewBlockchainService(cfg), []int64{146}); len(got) != 0 || buf.Len() != 0 {
		t.Fatalf("Sonic only: %v, log %q", got, buf.String())
	}
	if got := buildP2PChainGasSponsorships(cfg, services.NewBlockchainService(cfg), []int64{56}); len(got) != 0 ||
		!strings.Contains(buf.String(), "[p2p-gas] dormant") {
		t.Fatalf("no key: %v, log %q", got, buf.String())
	}
}
