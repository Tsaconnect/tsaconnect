-- One-off: set the legal name for ONE verified user Didit cannot answer for.
--
-- WHY: the money-in gate refuses a verified user with no verified_legal_name
-- ("your identity is verified, but we could not confirm the legal name on it").
-- Run `tsa-api -backfill-legal-names` first; it fills every user Didit still
-- holds a session for and prints the ids it could not. This file is for those.
--
-- The name must be exactly as on the user's ID document: central-account
-- payments are matched against the sender's bank-account name with it.
--
-- SAFETY: touches only a verified user whose name is still empty, so it can
-- never overwrite a captured name (that is the name-change review's job).
-- Run: psql -v ON_ERROR_STOP=1 -v user_id='<uuid>' -v legal_name='<NAME AS ON ID>' -f <this file>

BEGIN;

UPDATE users
SET verified_legal_name = btrim(regexp_replace(:'legal_name', '\s+', ' ', 'g')),
    verified_legal_name_at = now(),
    updated_at = now()
WHERE id = :'user_id'
  AND verification_status = 'verified'
  AND (verified_legal_name IS NULL OR verified_legal_name = '');

-- now() is fixed for the transaction, so this matches only the row set above.
INSERT INTO verification_logs (id, user_id, action, status, notes, metadata, created_at, updated_at)
SELECT gen_random_uuid(), id, 'didit_name_captured', 'verified',
       'Legal name set by operator one-off', '{"source":"one-off"}'::jsonb, now(), now()
FROM users
WHERE id = :'user_id' AND verified_legal_name_at = now();

COMMIT;
