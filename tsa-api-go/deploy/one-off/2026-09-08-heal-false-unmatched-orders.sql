-- One-off: relabel Paycrest orders wrongly marked `unmatched`.
--
-- WHY THIS EXISTS: `unmatched` means the user paid, the bank confirmed, and
-- the protocol never registered it — a real operator alarm with no API
-- remedy. The original detector was `expired AND amount_paid = 0`, which is
-- ALSO exactly what an order the user abandoned before paying looks like. So
-- every abandoned checkout was telling its user "We are recovering your
-- payment" for money they never sent, and paging an operator who finds
-- nothing.
--
-- ec8d007 fixed the detector: it now also requires payment_claimed_at, set
-- when the user taps "I've sent the money". But that only prevents NEW false
-- positives. `unmatched` is terminal (models.PaycrestOrderTerminal), so
-- PaycrestOrderService.Sync returns early and never re-evaluates a row that
-- was already mislabelled. Those rows need correcting here.
--
-- WHEN TO RUN: once, on any database that ran the pre-ec8d007 detector.
-- Check first — no rows returned means this script is not needed:
--   SELECT id, reference, amount, created_at FROM paycrest_order_records
--    WHERE status = 'unmatched' AND payment_claimed_at IS NULL;
--
-- EFFECT: those rows become `expired`, the ordinary "no payment received"
-- outcome. Rows WITH payment_claimed_at are left alone — those are the
-- genuine alarms this status exists for.
--
-- SAFETY: no row is touched where the user asserted payment, so a real
-- unmatched deposit cannot be erased by this. Run AFTER `tsa-api -migrate`
-- (payment_claimed_at must exist), unlike the DDL scripts here.

BEGIN;

DO $$
DECLARE
    healed integer;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'paycrest_order_records'
          AND column_name = 'payment_claimed_at'
    ) THEN
        RAISE EXCEPTION 'payment_claimed_at is missing — run `tsa-api -migrate` first';
    END IF;

    UPDATE paycrest_order_records
       SET status = 'expired',
           updated_at = NOW()
     WHERE status = 'unmatched'
       AND payment_claimed_at IS NULL;

    GET DIAGNOSTICS healed = ROW_COUNT;
    RAISE NOTICE 'relabelled % falsely-unmatched order(s) as expired', healed;
END $$;

COMMIT;
