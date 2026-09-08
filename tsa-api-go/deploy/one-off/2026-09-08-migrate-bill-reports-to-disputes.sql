-- Fold bill_reports into the unified disputes table.
--
-- Run ONCE, after the release that creates `disputes` is live. The API creates
-- the table through AutoMigrate; only this data move is deliberate, which is
-- why it lives here rather than in the boot path.
--
-- bill_reports is deliberately NOT dropped. Nothing reads it after this runs,
-- and leaving it costs nothing while making the release trivially reversible —
-- a later cleanup drops it once the unified queue has been in production long
-- enough to trust.
--
-- Idempotent: re-running inserts nothing, because the unique index on
-- (subject_type, subject_id) is what ON CONFLICT keys against.

BEGIN;

INSERT INTO disputes (
    id, subject_type, subject_id, user_id, reason, evidence_urls,
    status, external_ref, escalation_note,
    resolved_by, resolution, resolved_at,
    created_at, updated_at
)
SELECT
    r.id,
    'bill_order',
    r.order_id,
    r.user_id,
    r.reason,
    '',              -- bill reports never carried evidence
    r.status,        -- open / resolved / rejected map across unchanged
    '',
    '',
    r.resolved_by,
    COALESCE(r.resolution, ''),
    r.resolved_at,
    r.created_at,
    r.updated_at
FROM bill_reports r
ON CONFLICT (subject_type, subject_id) DO NOTHING;

COMMIT;

-- Verification (run separately; both counts should match):
--   SELECT count(*) FROM bill_reports;
--   SELECT count(*) FROM disputes WHERE subject_type = 'bill_order';
