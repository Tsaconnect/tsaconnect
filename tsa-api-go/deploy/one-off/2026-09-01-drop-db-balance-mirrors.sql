-- One-off: drop the DB-held crypto balance mirrors.
--
-- WHY THIS EXISTS: `assets.balance`, `assets.usd_value`, `wallets.total_balance`
-- and `wallets.total_usd_value` stored a copy of what the user holds on-chain.
-- Nothing refreshed them after the wallet moved to live RPC reads, so every row
-- is a number that disagrees with the chain — and `portfolios` computed
-- allocation and risk analytics from those stale inputs, reporting confident
-- results with no basis.
--
-- Crypto balances are now read from the chain on every request
-- (handlers.GetWalletBalances) and entitlement balances from the swap contract.
-- The endpoints that served these tables (/assets, /portfolio/*,
-- /market/watchlist, POST /transactions) are removed in the same release, so
-- nothing reads them any more.
--
-- WHEN TO RUN: after the release that removes those endpoints is deployed,
-- never before — an older binary still serving from this database selects
-- wallets.total_balance and will error once the column is gone.
--
-- The `transactions` table is deliberately NOT touched: it is still read by
-- GET /api/transactions and written by the reward-claim service.
--
-- EFFECT: irreversible. Take a dump first if the historical rows matter for
-- audit; they are not used by any running code.

BEGIN;

DROP TABLE IF EXISTS assets;
DROP TABLE IF EXISTS portfolios;

ALTER TABLE wallets DROP COLUMN IF EXISTS total_balance;
ALTER TABLE wallets DROP COLUMN IF EXISTS total_usd_value;

COMMIT;
