package main

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/jobs"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// knownHotKeys are production hot-key addresses that must never hold a role on a
// core contract, listed even when this process does not hold their key (a
// KMS signer, a box without the treasury key): the OPERATOR (PM release, pool
// releases) and the TREASURY (signer consolidation 2026-10-13).
var knownHotKeys = map[string]string{
	"operator 0x38cE (production)": "0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9",
	"treasury 0xA412 (production)": "0xA4123022c84E66A5E955fd96C5468A78bE84e205",
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
	// The PM admin EOA signs PM settlement: it must not resolve disputes or administer either.
	addHotKey(out, "PM_ADMIN_EOA_ADDRESS", cfg.PMAdminEOAAddress)
	return out
}

func addHotKey(out map[string]string, label, addr string) {
	if common.IsHexAddress(addr) && common.HexToAddress(addr) != (common.Address{}) {
		out[label] = common.HexToAddress(addr).Hex()
	}
}

// checkP2PEscrowMove is the P2P hard-switch guard (slice 8): every P2P chain's escrow is its core
// set's P2PTradeEscrow, so boot refuses while, on any chain, there is
//
//   - a non-terminal trade (not a retired pool slice) recorded on another escrow (or none: every
//     trade before the switch): the API could never finish it; or
//   - an active or paused sell-ad (not a retired LP stock ad) whose voucher was signed for another
//     escrow: it can still be opened there, and its seller can no longer cancel it here.
//
// Settle them on the old escrow first. With no core set selected it only alerts: P2P is dormant then,
// and a refusal would crash-loop a deploy that changes nothing for these rows. A chain
// with no P2P after the switch counts too: its live rows are just as stranded. Sellers' deposited
// (not locked) balances on an old escrow do not block boot — no row says who holds one — but they
// must withdraw() there themselves. Pending (unsigned) and buy-ads are not counted: a pending
// ad's signature is refused on the new escrow and a buy-ad has no voucher. A table it cannot read
// is an ALERT, not a refusal.
func checkP2PEscrowMove(db *gorm.DB, cfg *config.Config) error {
	if db == nil || cfg == nil {
		return nil
	}
	current := map[int64]string{}
	for id, cc := range cfg.P2PChains {
		current[id] = strings.ToLower(cc.EscrowAddress)
	}
	type row struct {
		ChainID       int64
		EscrowAddress string
		N             int64
	}
	var trades, ads []row
	err := db.Model(&models.P2PTrade{}).
		Select("chain_id, LOWER(COALESCE(escrow_address, '')) AS escrow_address, COUNT(*) AS n").
		Where("it_intent_id IS NULL AND status NOT IN ?",
			[]string{models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled, models.P2PTradeStatusResolved}).
		Group("chain_id, LOWER(COALESCE(escrow_address, ''))").Scan(&trades).Error
	if err == nil {
		err = db.Model(&models.P2PAd{}).
			Select("chain_id, LOWER(COALESCE(escrow_address, '')) AS escrow_address, COUNT(*) AS n").
			Where("status IN ? AND COALESCE(side, '') <> ?",
				[]string{models.P2PAdStatusActive, models.P2PAdStatusPaused}, models.P2PAdSideBuy).
			// A retired slice pool's LP stock ad is no sell-ad anyone can open (as excludePoolAds).
			Where("(pm_id IS NULL OR NOT EXISTS (SELECT 1 FROM payment_merchants pool_lp WHERE pool_lp.id = p2p_ads.pm_id AND pool_lp.kind = ?))",
				models.PaymentMerchantKindLP).
			Group("chain_id, LOWER(COALESCE(escrow_address, ''))").Scan(&ads).Error
	}
	if err != nil {
		log.Printf("[config] ALERT p2p_escrow_move_unchecked err=%s — boot goes on; re-check live P2P trades and ads on retired escrows by hand",
			services.RedactAlertText(err.Error()))
		return nil
	}
	var stranded []string
	for _, set := range []struct {
		what string
		rows []row
	}{{"trades", trades}, {"sell-ads", ads}} {
		for _, r := range set.rows {
			if want, ok := current[r.ChainID]; ok && r.EscrowAddress == want {
				continue
			}
			escrow := r.EscrowAddress
			if escrow == "" {
				escrow = "the pre-switch escrow"
			}
			stranded = append(stranded, fmt.Sprintf("%d live %s on chain %d at %s", r.N, set.what, r.ChainID, escrow))
		}
	}
	if len(stranded) == 0 {
		return nil
	}
	slices.Sort(stranded)
	// With no core set selected (what prod runs until cutover) P2P is dormant everywhere and nothing
	// moves: refusing would crash-loop a deploy that changes nothing for these rows. Alert instead.
	if cfg.CoreSet == "" || cfg.CoreSet == config.CoreSetOff {
		log.Printf("[config] ALERT p2p_escrow_move_pending %s — P2P is dormant until a core set is selected; settle these before it is",
			strings.Join(stranded, "; "))
		return nil
	}
	return fmt.Errorf("p2p_escrow_move_refused: %s — settle them on their escrow (trades) or cancel them (ads) before P2P moves to the core set", strings.Join(stranded, "; "))
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

// pmReleaserWorker reports, every 15 minutes, each active payment merchant whose maker wallet has
// not named the release key (setReleaser), or has since revoked it: its sell-ads cannot be signed
// or opened, and trades already paid to it cannot be released by AutoRelease. Each PM is alerted
// at most hourly (MissingPMReleasers' own dedupe); a failed check fails the tick.
type pmReleaserWorker struct{ trades *services.P2PTradeService }

func (w pmReleaserWorker) Name() string            { return "pm-releaser-check" }
func (w pmReleaserWorker) Interval() time.Duration { return 15 * time.Minute }
func (w pmReleaserWorker) Run(ctx context.Context) error {
	_, err := w.trades.AlertMissingPMReleasers(ctx)
	return err
}

// orderTimeoutWorker sweeps OrderEscrow orders whose deadline passed (services.OrderTimeoutClaimer).
type orderTimeoutWorker struct{ c *services.OrderTimeoutClaimer }

func (w orderTimeoutWorker) Name() string            { return "order-timeout-claimer" }
func (w orderTimeoutWorker) Interval() time.Duration { return 5 * time.Minute }
func (w orderTimeoutWorker) Run(ctx context.Context) error {
	claimed, settled, err := w.c.Sweep(ctx)
	if claimed+settled > 0 {
		log.Printf("[order-timeout] claimed=%d settled=%d", claimed, settled)
	}
	return err
}

// registerOrderTimeouts runs the deadline sweep whenever orders escrow in an OrderEscrow. Without a
// release key it still records orders the escrow paid out, but claims nothing.
func registerOrderTimeouts(scheduler *jobs.Scheduler, escrow *services.EscrowService, settler services.OrderSettler,
	release services.ChainSender, sonic *blockchain.EVMClient) {
	if escrow == nil || escrow.CurrentEscrowAddress() == "" || sonic == nil {
		log.Printf("[order-timeout] dormant: no OrderEscrow on Sonic")
		return
	}
	if release == nil {
		log.Printf("[order-timeout] WARNING no release key: overdue orders are recorded but not claimed")
	}
	scheduler.Register(orderTimeoutWorker{services.NewOrderTimeoutClaimer(config.DB, escrow, settler, release, sonic.ChainID().Int64())},
		jobs.AlertOnFailureStreak())
}
