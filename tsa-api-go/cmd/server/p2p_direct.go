package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// knownHotKeys are production hot-key addresses that must never administer a
// direct escrow, listed even when this process does not hold their key (a
// KMS signer, a box without the treasury key): the OPERATOR (PM release, pool
// releases) and the TREASURY (signer consolidation 2026-10-13).
var knownHotKeys = map[string]string{
	"operator 0x38cE (production)": "0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9",
	"treasury 0xA412 (production)": "0xA4123022c84E66A5E955fd96C5468A78bE84e205",
}

// directEscrowForbiddenAdmins is every hot-key address the API knows at boot
// (label → address): none may be an admin (or the owner) of a direct-audience
// escrow, whose admins are dispute arbiters only (decisions 2026-10-12/13).
// The configured keys come from signerKeysExcept, the one list
// assertDistinctKey also reads, so a new hot key lands in both. Addresses are
// derived from the keys; nothing here signs.
func directEscrowForbiddenAdmins(cfg *config.Config) map[string]string {
	out := apiHotKeys(cfg)
	addHotKey(out, "PM_ADMIN_EOA_ADDRESS", cfg.PMAdminEOAAddress)
	return out
}

// apiHotKeys is every signing key the API holds or is known to run (label → address): the
// configured keys from signerKeysExcept plus knownHotKeys. None may hold a role on a core
// contract (services.CheckCoreGates).
func apiHotKeys(cfg *config.Config) map[string]string {
	out := map[string]string{}
	for name, key := range signerKeysExcept(cfg, "") {
		addHotKey(out, name, signerAddress(key))
	}
	for label, addr := range knownHotKeys {
		addHotKey(out, label, addr)
	}
	return out
}

func addHotKey(out map[string]string, label, addr string) {
	if common.IsHexAddress(addr) && common.HexToAddress(addr) != (common.Address{}) {
		out[label] = common.HexToAddress(addr).Hex()
	}
}

// checkDirectUnconfigure is the direct-escrow rollback guard (plan p2p-bsc §6):
// boot refuses while, on a chain whose preset row no longer configures a
// direct-audience escrow (the row exists, empty), there is
//
//   - a non-terminal trade (not a retired pool slice): the registry fails
//     loud, so it could never finish; or
//   - a pending, active or paused ad (not a retired LP stock ad): its voucher
//     can still be opened on chain, and its seller can no longer cancel it here.
//
// Restore the preset row until they are terminal. Sellers' deposited (not
// locked) balances do not block boot — no row says who holds one — but the
// API cannot build their withdraw without the row either: they need the row
// restored, or must call withdraw() on the escrow themselves. A configured
// escrow the registry is not running (RPC down, a failed gate) and a chain no
// preset row names are warnings; a table it cannot read is an ALERT.
func checkDirectUnconfigure(db *gorm.DB, cfg *config.Config, reg *services.P2PEscrowRegistry) error {
	if db == nil || cfg == nil {
		return nil
	}
	direct := map[int64]bool{}
	for _, cc := range cfg.DormantP2PChains {
		if cc.Audience == config.P2PAudienceDirect {
			direct[cc.ChainID] = true
		}
	}
	for id, cc := range cfg.P2PChains {
		if cc.Audience == config.P2PAudienceDirect {
			direct[id] = true
		}
	}
	if len(direct) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(direct))
	var tradeChains, adChains []int64
	err := db.Model(&models.P2PTrade{}).
		Where("chain_id IN ? AND it_intent_id IS NULL AND status NOT IN ?", ids,
			[]string{models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled, models.P2PTradeStatusResolved}).
		Distinct("chain_id").Pluck("chain_id", &tradeChains).Error
	if err == nil {
		err = db.Model(&models.P2PAd{}).
			Where("chain_id IN ? AND status IN ?", ids,
				[]string{models.P2PAdStatusPending, models.P2PAdStatusActive, models.P2PAdStatusPaused}).
			// A retired slice pool's LP stock ad is no direct ad (as excludePoolAds).
			Where("(pm_id IS NULL OR NOT EXISTS (SELECT 1 FROM payment_merchants pool_lp WHERE pool_lp.id = p2p_ads.pm_id AND pool_lp.kind = ?))",
				models.PaymentMerchantKindLP).
			Distinct("chain_id").Pluck("chain_id", &adChains).Error
	}
	if err != nil {
		log.Printf("[config] ALERT p2p_direct_unconfigure_unchecked err=%s — boot goes on; re-check live direct trades and ads by hand",
			services.RedactAlertText(err.Error()))
		return nil
	}
	live := map[int64][]string{}
	for _, id := range tradeChains {
		live[id] = append(live[id], "trades")
	}
	for _, id := range adChains {
		live[id] = append(live[id], "ads")
	}
	for _, id := range slices.Sorted(maps.Keys(live)) {
		what := strings.Join(live[id], " and ")
		if _, ok := cfg.P2PChains[id]; ok {
			if !reg.HasChain(id) {
				log.Printf("[config] WARN p2p_direct_chain_not_running chain=%d: live direct %s, the direct escrow is configured but not registered (RPC down or a failed gate at boot) — they wait until it is back", id, what)
			}
			continue
		}
		return fmt.Errorf("p2p_direct_unconfigure_refused: live direct %s on chain %d, whose direct escrow row is empty — restore the preset row until they are terminal (sellers' deposited balances also need the row, or an on-chain withdraw() of their own)", what, id)
	}
	return nil
}

// dropUngatedDirectFeeManagers unregisters a direct-audience row's FeeManager
// from cfg.FeeManagerChains when its escrow did not pass the boot gates: the
// admin fee flow and the drift reconciler must not drive a FeeManager whose
// escrow the API does not run. Runs after the registry and before anything
// reads FeeManagerChains.
func dropUngatedDirectFeeManagers(cfg *config.Config, reg *services.P2PEscrowRegistry) {
	if cfg == nil || reg == nil {
		return
	}
	for id, fm := range cfg.FeeManagerChains {
		cc, ok := cfg.P2PChains[id]
		if !ok || cc.Audience != config.P2PAudienceDirect || cc.FeeManager == "" ||
			common.HexToAddress(fm.Address) != common.HexToAddress(cc.FeeManager) || reg.HasChain(id) {
			continue
		}
		delete(cfg.FeeManagerChains, id)
		log.Printf("[config] ALERT p2p_direct_fee_manager_unregistered chain=%d fee_manager=%s — its direct escrow is not running (failed gate or no RPC); the admin fee flow and drift reconciler skip it until it is", id, fm.Address)
	}
}

// coreGatesWorker gates the core contract set every minute (services.CoreRegistry.Gate): a chain
// is ready only while its gates pass, and a set that stays shut fails the tick (failure-streak ALERT).
type coreGatesWorker struct{ reg *services.CoreRegistry }

func (w coreGatesWorker) Name() string            { return "core-contracts-gates" }
func (w coreGatesWorker) Interval() time.Duration { return services.CoreGatesInterval }
func (w coreGatesWorker) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	return w.reg.Gate(ctx)
}
