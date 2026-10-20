-- One merchant role (owner 2026-10-19): TSA Pay approvals now grant role='merchant'
-- like every other merchant approval. Run once AFTER the release is live, to give
-- the same role to owners approved before it. Review the SELECT first. Suspended
-- merchants are left alone: an admin switched them off on purpose.
SELECT u.id, u.email, u.role FROM users u
WHERE u.role = 'user' AND (
  u.id IN (SELECT user_id FROM pay_merchants WHERE status <> 'suspended')
  OR u.id IN (SELECT user_id FROM merchant_requests
              WHERE business_type = 'payment_acceptance' AND status = 'approved')
);

BEGIN;
UPDATE users SET role = 'merchant'
WHERE role = 'user' AND (
  id IN (SELECT user_id FROM pay_merchants WHERE status <> 'suspended')
  OR id IN (SELECT user_id FROM merchant_requests
            WHERE business_type = 'payment_acceptance' AND status = 'approved')
);
-- Check the row count against the SELECT above, then COMMIT (or ROLLBACK).
