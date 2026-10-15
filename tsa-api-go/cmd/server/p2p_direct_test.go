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

// Every hot key the API holds, the PM admin EOA and the known production
// operator and treasury are forbidden direct-escrow admins; unset and
// malformed keys are skipped.
func TestDirectEscrowForbiddenAdmins_EveryKnownHotKey(t *testing.T) {
	cfg := &config.Config{PMReleasePrivateKey: testKeyA, RelayerPrivateKey: "0x" + testKeyB,
		RampTreasuryPrivateKey: "not-a-key", PMAdminEOAAddress: "0x00000000000000000000000000000000000038cE"}
	got := directEscrowForbiddenAdmins(cfg)
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
	if n := len(directEscrowForbiddenAdmins(&config.Config{})); n != len(knownHotKeys) {
		t.Fatalf("no keys configured: %d addresses, want only the %d known hot keys", n, len(knownHotKeys))
	}
}

func directGuardDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE p2p_trades (id TEXT PRIMARY KEY, chain_id INTEGER, status TEXT, it_intent_id TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE p2p_ads (id TEXT PRIMARY KEY, chain_id INTEGER, status TEXT, pm_id TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE payment_merchants (id TEXT PRIMARY KEY, kind TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

// Plan p2p-bsc §6 rollback: emptying the direct row while direct trades or
// ads are live refuses boot; terminal rows and retired slices do not; a
// configured row whose escrow is not running only warns.
func TestCheckDirectUnconfigure(t *testing.T) {
	db := directGuardDB(t)
	emptied := &config.Config{DormantP2PChains: []config.P2PChainConfig{{ChainID: 56, ChainKey: "bsc", Audience: config.P2PAudienceDirect}}}
	none := services.NewP2PEscrowRegistryFromMap(nil, "2")
	trade := func(status string, slice bool) {
		var intent any
		if slice {
			intent = uuid.NewString()
		}
		db.Exec(`INSERT INTO p2p_trades (id, chain_id, status, it_intent_id) VALUES (?, 56, ?, ?)`, uuid.NewString(), status, intent)
	}
	trade(models.P2PTradeStatusReleased, false)
	trade(models.P2PTradeStatusCancelled, false)
	trade(models.P2PTradeStatusPendingPayment, true) // a retired slice: checkSliceLeftovers' business
	db.Exec(`INSERT INTO p2p_ads (id, chain_id, status) VALUES (?, 56, ?)`, uuid.NewString(), models.P2PAdStatusCancelled)
	lp := uuid.NewString()
	db.Exec(`INSERT INTO payment_merchants (id, kind) VALUES (?, ?)`, lp, models.PaymentMerchantKindLP)
	db.Exec(`INSERT INTO p2p_ads (id, chain_id, status, pm_id) VALUES (?, 56, ?, ?)`, uuid.NewString(), models.P2PAdStatusActive, lp)
	if err := checkDirectUnconfigure(db, emptied, none); err != nil {
		t.Fatalf("only terminal rows and a retired LP stock ad: %v", err)
	}
	db.Exec(`INSERT INTO p2p_ads (id, chain_id, status) VALUES (?, 56, ?)`, uuid.NewString(), models.P2PAdStatusPaused)
	if err := checkDirectUnconfigure(db, emptied, none); err == nil || !strings.Contains(err.Error(), "p2p_direct_unconfigure_refused") {
		t.Fatalf("a live direct ad on an emptied row: %v", err)
	}
	trade(models.P2PTradeStatusPaid, false)
	if err := checkDirectUnconfigure(db, emptied, none); err == nil || !strings.Contains(err.Error(), "trades and ads") {
		t.Fatalf("live trades and ads: %v", err)
	}

	buf := directLog(t)
	configured := &config.Config{P2PChains: map[int64]config.P2PChainConfig{56: {ChainID: 56, Audience: config.P2PAudienceDirect}}}
	if err := checkDirectUnconfigure(db, configured, none); err != nil {
		t.Fatalf("a configured row whose escrow is not running must boot: %v", err)
	}
	if !strings.Contains(buf.String(), "WARN p2p_direct_chain_not_running chain=56") {
		t.Fatalf("no warning:\n%s", buf.String())
	}
	// Sonic rows (audience all/pm) are not this guard's.
	if err := checkDirectUnconfigure(db, &config.Config{}, none); err != nil {
		t.Fatalf("no direct row anywhere: %v", err)
	}
}

// A read error is an ALERT, never a refusal.
func TestCheckDirectUnconfigure_ReadErrorAlertsAndBoots(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{}) // no tables
	if err != nil {
		t.Fatal(err)
	}
	buf := directLog(t)
	cfg := &config.Config{DormantP2PChains: []config.P2PChainConfig{{ChainID: 56, Audience: config.P2PAudienceDirect}}}
	if err := checkDirectUnconfigure(db, cfg, services.NewP2PEscrowRegistryFromMap(nil, "2")); err != nil {
		t.Fatalf("a read error refused boot: %v", err)
	}
	if !strings.Contains(buf.String(), "ALERT p2p_direct_unconfigure_unchecked") {
		t.Fatalf("no alert:\n%s", buf.String())
	}
}

// A direct row's FeeManager is dropped when its escrow is not running; a
// running one, and Sonic's, are kept.
func TestDropUngatedDirectFeeManagers(t *testing.T) {
	const fm = "0x00000000000000000000000000000000000FEE01"
	cfg := &config.Config{
		P2PChains: map[int64]config.P2PChainConfig{56: {ChainID: 56, Audience: config.P2PAudienceDirect, FeeManager: fm}},
		FeeManagerChains: map[int64]config.FeeManagerChainConfig{
			56: {ChainID: 56, ChainKey: "bsc", Address: fm}, 146: {ChainID: 146, ChainKey: "sonic", Address: "0x00000000000000000000000000000000000FEE02"}},
	}
	dropUngatedDirectFeeManagers(cfg, services.NewP2PEscrowRegistryFromMap(map[int64]services.P2PEscrowAdapter{56: nil}, "2"))
	if len(cfg.FeeManagerChains) != 2 {
		t.Fatalf("a running direct escrow lost its FeeManager: %v", cfg.FeeManagerChains)
	}
	dropUngatedDirectFeeManagers(cfg, services.NewP2PEscrowRegistryFromMap(nil, "2"))
	if _, ok := cfg.FeeManagerChains[56]; ok {
		t.Fatal("the gate-failed direct escrow's FeeManager must be unregistered")
	}
	if _, ok := cfg.FeeManagerChains[146]; !ok {
		t.Fatal("Sonic's FeeManager must be kept")
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
