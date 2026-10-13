-- One-off: drop the one-row-per-user index on payment_merchants.
--
-- WHY: a user may now be both a payment merchant and a liquidity provider
-- (owner decision 2026-10-13). uq_payment_merchants_user_kind (user_id, kind),
-- built CONCURRENTLY by boot migration, is the new backstop; this drops the
-- old (user_id) one so the second role's row can exist.
--
-- WHEN TO RUN — all four must hold:
--   1. main contains feat/pm-and-lp AND the rebased feat/p2p-bsc-direct
--      (its lpBinding/routeFor reads the LP row by kind);
--   2. that main is what prod runs, and its boot log shows no
--      "[Migrate] WARNING uq_payment_merchants_user_kind";
--   3. the app OTA with the per-role screens is published;
--   4. nobody is about to deploy a feature branch.
-- Until then a second-role approval is refused with 409 second_role_not_enabled.
--
-- AFTER IT RUNS: never deploy a build that lacks GetByUserIDKind (anything
-- older than feat/pm-and-lp, or any branch not rebased on it). Such a build
-- reads "the" row for a user, picks either role, and can route an LP's BSC
-- money to the direct escrow. That is a rollback, and it is forbidden.
--
-- PRE-DUAL ROLLBACK: while no user has two rows
--   (SELECT user_id FROM payment_merchants GROUP BY 1 HAVING count(*) > 1
--    returns nothing), the old index can be restored with
--   CREATE UNIQUE INDEX CONCURRENTLY uq_payment_merchants_user ON payment_merchants (user_id);
--
-- SAFETY: refuses, dropping nothing, unless the new index exists, is valid and
-- unique, and is on exactly (user_id, kind). Run: psql -v ON_ERROR_STOP=1 -f <this file>

BEGIN;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_index
    WHERE indexrelid = to_regclass('uq_payment_merchants_user_kind')
      AND indisvalid AND indisunique
      AND pg_get_indexdef(indexrelid) LIKE '%(user_id, kind)'
  ) THEN
    RAISE EXCEPTION 'uq_payment_merchants_user_kind is missing, invalid or not on (user_id, kind); deploy the release first';
  END IF;
END
$$;

DROP INDEX IF EXISTS uq_payment_merchants_user;

COMMIT;
