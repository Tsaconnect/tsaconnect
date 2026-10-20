-- One merchant role (owner 2026-10-19): TSA Pay approvals now grant role='merchant'
-- like every other merchant approval. Run once AFTER the API release is live and
-- BEFORE the app update ships, so the app never sees a TSA Pay owner without it.
-- Only owners of a provisioned, non-suspended TSA Pay merchant: suspended ones were
-- switched off on purpose, and an approved request with no merchant row yet gets the
-- role when its approval is retried. Review the SELECT first.
SELECT u.id, u.email, u.role FROM users u
WHERE u.role = 'user'
  AND u.id IN (SELECT user_id FROM pay_merchants WHERE status <> 'suspended');

BEGIN;
UPDATE users SET role = 'merchant'
WHERE role = 'user'
  AND id IN (SELECT user_id FROM pay_merchants WHERE status <> 'suspended');
-- Check the row count against the SELECT above, then COMMIT (or ROLLBACK).
