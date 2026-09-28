-- READ-ONLY diagnostic: operator debits in flight against a wallet whose link
-- to the account was never proven.
--
-- WHY THIS EXISTS: until fix/wallet-link-proof, POST /api/wallet/register and
-- every callerWallet route linked ANY address to the caller's account with no
-- signature — including another account's wallet in different letter case.
-- The operator then spent that wallet's Spendable MCGP for the caller (IT sell
-- paying naira to the caller's bank, bills, marketplace pay-spendable). The fix
-- stops new debits from unproven wallets; it cannot recall one already in
-- flight. This lists them so settlement can be held on a suspicious hit.
--
-- HOW TO READ IT:
--   shared_with_other_account = true  → the debited wallet is also linked (in
--       any case) to ANOTHER live account. This is the exploit's fingerprint:
--       hold the payout / release and investigate before anything settles.
--   unproven = true alone              → expected for every row at first deploy
--       (no account has proved a wallet yet); only a concern alongside other
--       signals (new account, first sell, payout bank ≠ usual).
--
-- WHEN TO RUN: right after deploying fix/wallet-link-proof, and again before
-- releasing any held payout it flags. Changes nothing.

WITH debits AS (
  SELECT 'it_sell'::text AS kind, id::text AS ref, user_id, wallet_address AS wallet, status, created_at
    FROM it_intents
   WHERE direction = 'sell'
     AND status IN ('processing', 'debiting', 'debited', 'needs_review')
     AND (asset IS NULL OR asset = '' OR asset = 'MCGP')
  UNION ALL
  SELECT 'bill', order_id::text, user_id, wallet_address, status, created_at
    FROM bill_payments
   WHERE status IN ('debiting', 'debited')
  UNION ALL
  SELECT 'order_mcgp', order_id::text, buyer_id, buyer_wallet, status, created_at
    FROM order_mcgp_payments
   WHERE status IN ('debiting', 'held', 'releasing')
)
SELECT *
  FROM (
    SELECT d.kind, d.ref, d.user_id, d.wallet, d.status, d.created_at,
           NOT EXISTS (
             SELECT 1 FROM wallet_link_challenges c
              WHERE c.user_id = d.user_id
                AND c.address = LOWER(d.wallet)
                AND c.used_at IS NOT NULL
           ) AS unproven,
           EXISTS (
             SELECT 1 FROM users u
              WHERE LOWER(u.wallet_address) = LOWER(d.wallet)
                AND u.id <> d.user_id
                AND u.account_status <> 'deleted'
           ) AS shared_with_other_account,
           (SELECT string_agg(u.id::text, ',') FROM users u
             WHERE LOWER(u.wallet_address) = LOWER(d.wallet)
               AND u.id <> d.user_id) AS other_account_ids
      FROM debits d
  ) x
 WHERE x.unproven OR x.shared_with_other_account
 ORDER BY x.shared_with_other_account DESC, x.created_at DESC;
