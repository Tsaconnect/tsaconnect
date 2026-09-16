-- One-off: retire the legacy it_rates table.
--
-- WHY THIS EXISTS: it_rates was repriced from Naira (buy_rate_kobo) to USD
-- (buy_price_usd_micros). The new columns are NOT NULL with no default, so
-- AutoMigrate's ADD COLUMN fails against a table that still holds legacy rows:
--   ERROR: column "buy_price_usd_micros" contains null values
--
-- WHEN TO RUN: only on a database that still has the legacy column — a host restored
-- from a pre-2026-08-05 dump. Check first:
--   SELECT column_name FROM information_schema.columns
--    WHERE table_name = 'it_rates' AND column_name = 'buy_rate_kobo';
-- No rows returned means this script is not needed. Do not run it.
--
-- EFFECT: drops all IT rate rows. The admin re-sets the USD price after deploy.
-- There is no data migration — the legacy rows carry no USD value to convert.
--
-- RUN BEFORE `tsa-api -migrate`, never after.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'it_rates' AND column_name = 'buy_rate_kobo'
    ) THEN
        DROP TABLE it_rates;
        RAISE NOTICE 'legacy it_rates dropped; AutoMigrate will recreate it';
    ELSE
        RAISE NOTICE 'it_rates has no legacy column; nothing to do';
    END IF;
END $$;

COMMIT;
