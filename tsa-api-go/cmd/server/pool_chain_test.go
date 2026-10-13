package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBuildPoolChain_DormantWithoutAPoolRow(t *testing.T) {
	if g := buildPoolChain(testConfig(t), nil, nil, nil, nil); g != nil {
		t.Fatal("no pool row, no gates")
	}
}

func TestRegisterPoolJobs_NilRegistersNothing(t *testing.T) {
	s := jobs.NewScheduler()
	registerPoolJobs(s, nil)
	if s.Count() != 0 {
		t.Fatalf("registered %d", s.Count())
	}
}

func TestRegisterPoolJobs_ProbeAndReleaseFloat(t *testing.T) {
	s := jobs.NewScheduler()
	registerPoolJobs(s, services.NewPoolChainGates(services.PoolChainGatesConfig{ChainKey: "bsc"}))
	if s.Count() != 2 {
		t.Fatalf("registered %d, want the gate re-check and the release-gas monitor", s.Count())
	}
}

// Review I8 / S8 / S39: the per-chain lock covers every use of the release key
// only if no other signer (with its own lock) holds the same key. The
// controller confirmed prod's PM release, IT operator and relayer keys differ.
func TestBuildReleaseSigner_RefusesAKeySharedWithAnotherSigner(t *testing.T) {
	for _, other := range []string{"IT_OPERATOR_PRIVATE_KEY", "RELAYER_PRIVATE_KEY", "RAMP_SPONSOR_PRIVATE_KEY",
		"RAMP_TREASURY_WALLET_PRIVATE_KEY", "RAMP_TREASURY_PRIVATE_KEY"} {
		cfg := testConfig(t)
		cfg.PMReleasePrivateKey = testKeyA
		switch other {
		case "IT_OPERATOR_PRIVATE_KEY":
			cfg.ITOperatorPrivateKey = testKeyA
		case "RELAYER_PRIVATE_KEY":
			cfg.RelayerPrivateKey = testKeyA
		case "RAMP_SPONSOR_PRIVATE_KEY":
			cfg.RampSponsorPrivateKey = testKeyA
		case "RAMP_TREASURY_WALLET_PRIVATE_KEY":
			cfg.RampTreasuryWalletPrivateKey = testKeyA
		case "RAMP_TREASURY_PRIVATE_KEY":
			cfg.RampTreasuryPrivateKey = testKeyA
		}
		if !refusesToStart(t, func() { buildReleaseSigner(cfg, nil) }) {
			t.Fatalf("a release key shared with %s must refuse to start", other)
		}
	}
}

func TestBuildReleaseSigner_DistinctKeysDoNotRefuse(t *testing.T) {
	cfg := testConfig(t)
	cfg.PMReleasePrivateKey = testKeyA
	cfg.ITOperatorPrivateKey = testKeyB
	if refusesToStart(t, func() { buildReleaseSigner(cfg, nil) }) {
		t.Fatal("distinct keys must not refuse")
	}
}

func TestCompanyPMCheck_RefusesUnsetID(t *testing.T) {
	cfg := testConfig(t)
	if err := companyPMCheck(cfg)(context.Background()); err == nil {
		t.Fatal("IT_PM_ID unset must fail gate 7")
	}
}

// Review I2: no pool row hands out a nil interface, never a typed nil.
func TestBuildPoolChain_DormantIsANilInterface(t *testing.T) {
	var g services.PoolGate = buildPoolChain(testConfig(t), nil, nil, nil, nil)
	if g != nil {
		t.Fatalf("dormant gates = %#v, want a nil interface", g)
	}
}

// Review I2: a pool row with no RPC client at boot gets closed gates whose
// re-check job still runs (and re-alerts), rather than none at all.
func TestBuildPoolChain_NoClientIsClosedAndStillReChecked(t *testing.T) {
	g := buildPoolChain(poolTestConfig(t, "testnet"), nil, nil, nil, nil)
	if g == nil {
		t.Fatal("a pool row must get gates even without a client")
	}
	if st := g.Status(); g.Open() || st.Open || len(st.Failures) == 0 || !strings.Contains(st.Failures[0], "no chain client") {
		t.Fatalf("status = %+v, want closed on no chain client", st)
	}
	if g.ReleaseGasOK(context.Background()) {
		t.Fatal("no client: release gas must not read as OK")
	}
	s := jobs.NewScheduler()
	registerPoolJobs(s, g)
	if s.Count() != 2 {
		t.Fatalf("registered %d, want the gate re-check and the release-gas monitor", s.Count())
	}
}

func TestCompanyPMCheck_RefusesAnLPRow(t *testing.T) {
	cfg := testConfig(t)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PaymentMerchant{}); err != nil {
		t.Fatal(err)
	}
	lp := models.PaymentMerchant{ID: uuid.New(), UserID: uuid.New(), Name: "ADA OBI", Kind: models.PaymentMerchantKindLP, Status: models.PaymentMerchantStatusActive}
	db.Create(&lp)
	cfg.ITPMID = lp.ID.String()
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	if err := companyPMCheck(cfg)(context.Background()); err == nil {
		t.Fatal("IT_PM_ID naming an LP row must fail gate 7")
	}
}

// Review M4: the slice buyer is never an LP's user.
func TestCompanyPMUser_RefusesAnLPRow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PaymentMerchant{}); err != nil {
		t.Fatal(err)
	}
	lp := models.PaymentMerchant{ID: uuid.New(), UserID: uuid.New(), Name: "ADA OBI", Kind: models.PaymentMerchantKindLP, Status: models.PaymentMerchantStatusActive}
	db.Create(&lp)
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	if user, err := companyPMUser(&config.Config{ITPMID: lp.ID.String()}); err == nil {
		t.Fatalf("slice buyer = %s, want IT_PM_ID naming an LP row refused", user)
	}
}

// The release key's lane on the pool chain is the registry's: a sponsor lane
// on the same key and chain shares its lock and nonce floor.
func TestBuildReleaseSigner_TakesItsLanesFromTheRegistry(t *testing.T) {
	cfg := poolTestConfig(t, "mainnet")
	cfg.PMReleasePrivateKey = testKeyA
	bs := services.NewBlockchainService(cfg)
	crs, ok := buildReleaseSigner(cfg, bs).(*services.ChainReleaseSigner)
	if !ok || crs.Lane(56) == nil {
		t.Fatalf("release signer = %#v, want a lane on chain 56", crs)
	}
	key, _ := crypto.HexToECDSA(testKeyA)
	if l := buildSponsorLaneOn(bs, "mainnet", "bsc", "gas_sponsor", key, "ramp_sponsor_bsc", services.SponsorLimits{}, nil); l.KeyLane != crs.Lane(56) {
		t.Fatal("the release key and a sponsor lane on the same key and chain must share one KeyLane")
	}
}
