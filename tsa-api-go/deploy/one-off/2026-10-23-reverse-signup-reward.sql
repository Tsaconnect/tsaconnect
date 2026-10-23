-- One-off: take back the 100 TP "signup_reward" credits (and what they unlocked).
--
-- WHY: owner 2026-10-23 — the signup form carries no 100 TP award. Approving a signup
-- claim used to credit 100 TP (source_type 'signup_reward'); that code is gone, the credits
-- it made remain. Run 2026-10-23-signup-reward-tp-report.sql first and read it.
--
-- WHAT IT DOES, per user credited (all inside ONE open transaction you COMMIT or ROLLBACK):
--   1. Adds a compensating tp_earnings row (-credit, source_type 'signup_reward_reversal');
--      the original row stays, so history nets to zero and nothing is destroyed.
--   2. Lowers users.tp_balance by the credit.
--   3. Removes the level_reward entries the user no longer has TP for (threshold > the new
--      balance), subtracts their MCGP from reward_claimables, and lowers
--      user_reward_states.highest_awarded_level to the level the new balance supports.
--      The removed entries are copied to reward_entries_reversed_20261023 first.
--
-- REFUSES (aborts the transaction) when:
--   * any credited user already sits in a PUBLISHED distribution epoch (their MCGP may
--     already be claimable on-chain — that needs a decision, not a script);
--   * the credit was already reversed (re-running does nothing twice);
--   * the new balance would go negative.
--
-- Run in psql (interactive, so the transaction stays open for your review):
--   \i 2026-10-23-reverse-signup-reward.sql
-- then read the output and type COMMIT; (or ROLLBACK;).

BEGIN;

-- Who, and how much, net of any earlier reversal.
CREATE TEMP TABLE _rev ON COMMIT DROP AS
SELECT e.user_id,
       SUM(e.tp_earned) FILTER (WHERE e.source_type = 'signup_reward')          AS credited,
       COALESCE(SUM(e.tp_earned) FILTER (WHERE e.source_type = 'signup_reward_reversal'), 0) AS reversed
FROM tp_earnings e
WHERE e.source_type IN ('signup_reward', 'signup_reward_reversal')
GROUP BY e.user_id
HAVING SUM(e.tp_earned) FILTER (WHERE e.source_type = 'signup_reward') IS NOT NULL;

DELETE FROM _rev WHERE credited + reversed <= 0;   -- already fully reversed

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM _rev) THEN
    RAISE EXCEPTION 'nothing to reverse (no signup_reward credit, or already reversed)';
  END IF;
  IF EXISTS (
    SELECT 1 FROM reward_distribution_entries en
    JOIN reward_distributions d ON d.epoch = en.epoch
    WHERE d.status = 'published' AND en.user_id IN (SELECT user_id FROM _rev)
  ) THEN
    RAISE EXCEPTION 'a credited user is in a PUBLISHED distribution epoch: handle by hand';
  END IF;
  IF EXISTS (
    SELECT 1 FROM users u JOIN _rev r ON r.user_id = u.id
    WHERE u.tp_balance - (r.credited + r.reversed) < 0
  ) THEN
    RAISE EXCEPTION 'reversal would make a tp_balance negative: look at the user first';
  END IF;
END $$;

-- The balance each user will have, and the level it supports (thresholds 1,10,100,...).
CREATE TEMP TABLE _after ON COMMIT DROP AS
SELECT r.user_id, (r.credited + r.reversed) AS amount,
       u.tp_balance - (r.credited + r.reversed) AS new_balance,
       CASE WHEN u.tp_balance - (r.credited + r.reversed) >= 1
            THEN floor(log(10, u.tp_balance - (r.credited + r.reversed)))::int + 1 ELSE 0 END AS new_level
FROM _rev r JOIN users u ON u.id = r.user_id;

-- 3a. Keep a copy of the level entries about to go.
CREATE TABLE IF NOT EXISTS reward_entries_reversed_20261023 (LIKE reward_entries);
INSERT INTO reward_entries_reversed_20261023
SELECT re.* FROM reward_entries re JOIN _after a ON a.user_id = re.user_id
WHERE re.kind = 'level_reward' AND re.source_level > a.new_level;

-- 3b. MCGP they carried comes off the cumulative claimable.
UPDATE reward_claimables c
SET claimable_mcgp = GREATEST(0, c.claimable_mcgp - x.mcgp), updated_at = now()
FROM (SELECT re.user_id, SUM(re.mcgp_amount) AS mcgp
      FROM reward_entries re JOIN _after a ON a.user_id = re.user_id
      WHERE re.kind = 'level_reward' AND re.source_level > a.new_level
      GROUP BY re.user_id) x
WHERE c.user_id = x.user_id;

DELETE FROM reward_entries re USING _after a
WHERE re.user_id = a.user_id AND re.kind = 'level_reward' AND re.source_level > a.new_level;

UPDATE user_reward_states s SET highest_awarded_level = LEAST(s.highest_awarded_level, a.new_level),
       updated_at = now()
FROM _after a WHERE s.user_id = a.user_id;

-- 1 + 2. Compensating TP row, then the balance.
INSERT INTO tp_earnings (id, user_id, source_user_id, source_type, source_id, generation,
                         fee_amount_usd, percentage, tp_earned, created_at)
SELECT gen_random_uuid(), a.user_id, a.user_id, 'signup_reward_reversal', gen_random_uuid(), 0,
       0, 0, -a.amount, now()
FROM _after a;

UPDATE users u SET tp_balance = a.new_balance FROM _after a WHERE u.id = a.user_id;

-- Review before committing.
SELECT u.username, a.amount AS tp_removed, a.new_balance AS tp_now, a.new_level AS level_now,
       s.highest_awarded_level AS awarded_level_now, c.claimable_mcgp AS claimable_mcgp_now
FROM _after a
JOIN users u ON u.id = a.user_id
LEFT JOIN user_reward_states s ON s.user_id = a.user_id
LEFT JOIN reward_claimables c ON c.user_id = a.user_id;
SELECT user_id, source_level, reward_usd, mcgp_amount FROM reward_entries_reversed_20261023 ORDER BY user_id, source_level;

\echo 'Review the two tables above. Type COMMIT; to apply or ROLLBACK; to undo.'
