package services

import (
	"context"
	"log"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// fmReconcileLog emits a structured, greppable reconciler line (the keyed
// `*_total=1` tokens mirror the p2p_reconciler_drift_total / reward_reconciler
// log-metric convention — there is no Prometheus in this codebase yet).
func fmReconcileLog(format string, args ...interface{}) {
	log.Printf("[fee-manager-reconciler] "+format, args...)
}

// FeeManagerReconcileResult is one drift-reconcile pass's outcome (logging/tests).
type FeeManagerReconcileResult struct {
	// ChainsChecked is how many configured FeeManager chains were reconciled.
	ChainsChecked int
	// KeysChecked is how many (feature, token, chain) replica keys were compared.
	KeysChecked int
	// DriftCount is how many keys had on-chain rateFor != the fee_config replica
	// (the replica was refreshed from chain for each — chain wins).
	DriftCount int
	// Refreshed is how many replica rows were rewritten from chain truth.
	Refreshed int
	// ReadErrors is how many on-chain reads failed (last-known-good kept; escalated).
	ReadErrors int
}

// fmReconcileFeatures is the canonical set of (feature, token) replica keys the
// drift reconciler compares against on-chain rateFor for each chain. It mirrors
// the seeded DefaultFeeConfigs wildcard rows — these are the rates the FeeManager
// is the source of truth for. (Per-token override rows are reconciled too via the
// replica scan below; this list guarantees the canonical wildcard rows are always
// checked even if a chain-specific replica row hasn't been written yet.)
var fmReconcileFeatures = []string{
	models.FeatureProductMerchant,
	models.FeatureProduct,
	models.FeatureServiceContact,
	models.FeatureWithdrawal,
	models.FeatureSwap,
	models.FeatureServiceOther,
	models.FeatureOTC,
	models.FeatureP2P,
	models.FeatureP2PPlatform,
	models.FeatureSpot,
	models.FeatureInstantPay,
	models.FeatureInstantTransfer,
}

// ReconcileChains is the hourly drift reconciler (design §10). For each configured
// FeeManager chain it compares the on-chain rateFor against the fee_config read
// replica; on mismatch it REFRESHES the replica from chain (chain is the source
// of truth — CQRS, write→chain/read→replica) and emits fee_rate_drift_total. If an
// on-chain read FAILS it KEEPS the last-known-good replica and escalates (a
// transient RPC blip must never erase a good rate). There is NO escrow-getter to
// reconcile (A1) — only FeeManager (truth) vs replica (cache).
//
// No-op (clean) when the feature is dormant: no configured FeeManager chains, no
// chain reader, or nil service. Idempotent + read-only on-chain — safe on a tick.
func (s *FeeConfigService) ReconcileChains(ctx context.Context, cfg *config.Config, reader FeeManagerChainReader) (*FeeManagerReconcileResult, error) {
	res := &FeeManagerReconcileResult{}
	if s == nil || s.db == nil || cfg == nil || len(cfg.FeeManagerChains) == 0 || reader == nil {
		return res, nil // dormant / not wired — nothing to reconcile
	}

	for chainID, fmChain := range cfg.FeeManagerChains {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res.ChainsChecked++
		cid := chainID

		// The keys to compare for this chain: the canonical wildcard features PLUS
		// any chain-specific (feature, token) replica rows already written.
		type key struct {
			feature string
			token   string
		}
		seen := map[key]bool{}
		var keys []key
		for _, f := range fmReconcileFeatures {
			k := key{feature: f, token: models.FeeTokenAny}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		// Add chain-scoped replica rows so admin-set per-token overrides are checked.
		var chainRows []models.FeeConfig
		if err := s.db.WithContext(ctx).
			Where("chain_id = ?", chainID).
			Find(&chainRows).Error; err != nil {
			return res, err
		}
		for _, r := range chainRows {
			k := key{feature: r.Feature, token: r.TokenSymbol}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}

		for _, k := range keys {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			res.KeysChecked++

			onchain, err := reader.RateFor(ctx, chainID, fmChain.Address, k.feature, k.token)
			if err != nil {
				// A single read failing must not abort the pass or erase a good
				// replica — keep last-known-good + escalate. Next tick retries.
				res.ReadErrors++
				fmReconcileLog("fee_rate_read_error_total=1 chain=%d feature=%s token=%s err=%v (keeping last-known-good)",
					chainID, k.feature, k.token, err)
				continue
			}

			// Compare against the replica's chain-specific resolved value. We read
			// the chain-specific row directly (not the global fallback) so a missing
			// chain row vs a present on-chain rate is detected as drift to refresh.
			replicaBPS, replicaFlatUnits, replicaExempt, replicaActive, hasRow := s.replicaRowFor(k.feature, k.token, &cid)

			if hasRow && replicaBPS == int(onchain.BPS) && replicaFlatUnits == onchain.FlatUSD &&
				replicaExempt == onchain.MCGPExempt && replicaActive == onchain.Active {
				continue // in sync
			}

			res.DriftCount++
			fmReconcileLog("fee_rate_drift_total=1 ALERT chain=%d feature=%s token=%s replica=(bps=%d,flat=%d,exempt=%t,active=%t,present=%t) onchain=(bps=%d,flat=%d,exempt=%t,active=%t) — refreshing replica from chain (chain wins)",
				chainID, k.feature, k.token,
				replicaBPS, replicaFlatUnits, replicaExempt, replicaActive, hasRow,
				onchain.BPS, onchain.FlatUSD, onchain.MCGPExempt, onchain.Active)

			if err := s.RefreshFromConfirmedRate(k.feature, k.token, &cid, onchain.BPS, flatUnitsToUSD(onchain.FlatUSD), onchain.MCGPExempt, onchain.Active); err != nil {
				res.ReadErrors++
				fmReconcileLog("fee_rate_refresh_error_total=1 chain=%d feature=%s token=%s err=%v (replica NOT updated; keeping last-known-good)",
					chainID, k.feature, k.token, err)
				continue
			}
			res.Refreshed++
		}
	}
	return res, nil
}

// replicaRowFor reads the EXACT chain-specific replica row for (feature, token,
// chain) from the DB (not the global fallback) so the reconciler can tell a
// missing chain row apart from a present-but-equal one. Returns hasRow=false when
// no chain-specific row exists.
func (s *FeeConfigService) replicaRowFor(feature, token string, chainID *int64) (bps int, flatUnits uint64, exempt, active, hasRow bool) {
	var row models.FeeConfig
	q := s.db.Where("feature = ? AND token_symbol = ?", feature, token)
	if chainID == nil {
		q = q.Where("chain_id IS NULL")
	} else {
		q = q.Where("chain_id = ?", *chainID)
	}
	if err := q.First(&row).Error; err != nil {
		return 0, 0, false, false, false
	}
	return row.BPS, flatUSDToUnits(row.FlatAmountUSD), row.MCGPExempt, row.Active, true
}
