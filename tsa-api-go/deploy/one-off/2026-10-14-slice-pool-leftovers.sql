-- READ-ONLY diagnostic: what the retired slice pool (spec 2026-10-08) left
-- behind. Run against PRODUCTION before deploying feat/liquidity-shared-pool.
--
-- DEPLOY PRECONDITION. The shared-pool build has no code that releases,
-- cancels or forwards anything the slice pool opened. Boot refuses
-- (`[liquidity] ALERT liquidity_slice_leftovers_refused slices=… intents=…
-- forwards=…`, cmd/server/slice_leftovers.go) while any of these exist:
--   (1) a slice (p2p_trades.it_intent_id set) not released/cancelled/resolved;
--   (2) a tsa_pool order opened by the slice path (no pool_orders.fill_units)
--       in opening / pending_payment / payment_received / awaiting_funds /
--       delivering / buy_awaiting_rate;
--   (3) an LP naira forward (provider_ref 'pool:' or 'pool-f22:') still
--       pending or submitted.
-- Settle every row on the CURRENT release (it still runs the slice machinery)
-- and re-run this file until sections 1–3 are empty. Section 3b (failed LP
-- forwards) and section 4 (LP stock ads) do not block boot but must be
-- closed by hand: a failed forward is naira still owed to an LP, and an
-- active stock ad is hidden by the new build but its escrow deposit is not.
--
-- ON-CHAIN (not checkable from SQL): every LP's `available` balance in the
-- BSC slice escrow P2PEscrowV3 0xeBF3Fc37C57Eff8353bB4c30F51b6899F4ae22cD must
-- be 0 (withdrawn by the LP) before deploy — the new build has no screen for
-- withdrawing it. For each seller_address in section 4:
--   cast call 0xeBF3Fc37C57Eff8353bB4c30F51b6899F4ae22cD \
--     "balanceOf(address,address)(uint256,uint256)" <seller_address> \
--     0x55d398326f99059fF775485246999027B3197955 --rpc-url <BSC mainnet RPC>
-- (BSC USDT; returns (available, locked)). Both must read 0. `locked` > 0
-- means a slice is still open on-chain even if the DB says otherwise.
--
-- Changes nothing.

-- 1. Live slices.
SELECT t.id, t.it_intent_id, t.status, t.token, t.token_amount, t.fiat_amount,
       t.chain_id, t.on_chain_trade_id, t.seller_id AS lp_user_id,
       a.pm_id AS lp_id, a.seller_address AS lp_wallet,
       t.open_tx_hash, t.release_tx_hash, t.dropped_at, t.created_at, t.updated_at
  FROM p2p_trades t
  LEFT JOIN p2p_ads a ON a.id = t.ad_id
 WHERE t.it_intent_id IS NOT NULL
   AND t.status NOT IN ('released', 'cancelled', 'resolved')
 ORDER BY t.created_at;

-- 2. Live slice-era pool orders. Run BEFORE the deploy, every tsa_pool order
--    is slice-era (pool_orders.fill_units does not exist yet), so none is
--    excluded here; the boot check excludes vault orders after the migration.
SELECT i.id, i.user_id, i.status, i.asset, i.asset_amount, i.ngn_kobo,
       i.failure_reason, i.created_at, i.updated_at,
       (SELECT count(*) FROM p2p_trades s WHERE s.it_intent_id = i.id) AS slices,
       (SELECT count(*) FROM p2p_trades s WHERE s.it_intent_id = i.id
          AND s.status NOT IN ('released', 'cancelled', 'resolved')) AS live_slices
  FROM it_intents i
 WHERE i.provider = 'tsa_pool'
   AND i.status IN ('opening', 'pending_payment', 'payment_received',
                    'awaiting_funds', 'delivering', 'buy_awaiting_rate')
 ORDER BY i.created_at;

-- 3. LP naira forwards not landed (3a blocks boot; 3b = failed, owed by hand).
SELECT f.id, f.trade_id, f.pm_id AS lp_id, f.provider_ref, f.status, f.amount,
       f.currency, f.attempts, f.failure_reason, f.started_at, f.submitted_at,
       f.created_at,
       CASE WHEN f.status IN ('pending', 'submitted') THEN 'blocks_boot'
            ELSE 'owed_by_hand' END AS kind
  FROM p2p_settlement_forwards f
 WHERE (f.provider_ref LIKE 'pool:%' OR f.provider_ref LIKE 'pool-f22:%')
   AND f.status IN ('pending', 'submitted', 'failed')
 ORDER BY f.created_at;

-- 4. LP stock ads still open (cancel each, and check its seller_address on-chain above).
SELECT a.id, a.pm_id AS lp_id, pm.user_id AS lp_user_id, pm.name AS lp_name,
       a.seller_address, a.token, a.chain_id, a.status, a.amount, a.remaining,
       a.reserved, a.updated_at
  FROM p2p_ads a
  JOIN payment_merchants pm ON pm.id = a.pm_id AND pm.kind = 'lp'
 WHERE a.deleted_at IS NULL
   AND a.status NOT IN ('cancelled', 'exhausted')
 ORDER BY a.pm_id, a.created_at;

-- Every LP wallet that ever posted stock (for the on-chain balanceOf check).
SELECT DISTINCT a.pm_id AS lp_id, a.seller_address, a.chain_id
  FROM p2p_ads a
  JOIN payment_merchants pm ON pm.id = a.pm_id AND pm.kind = 'lp'
 ORDER BY a.pm_id;
