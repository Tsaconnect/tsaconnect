-- One-off, READ-ONLY: who was credited the old 100 TP signup reward, and what that
-- TP unlocked.
--
-- WHY: the PM decided on 2026-10-23 that the signup form carries NO reward (claiming
-- comes later, paid through the bills rails). Before that, approving a signup claim
-- credited 100 TP (source_type = 'signup_reward'). That code is removed; credits it
-- already made remain until someone decides what to do with them.
--
-- THIS FILE CHANGES NOTHING. It does not reverse the TP, on purpose: the TP also
-- crossed reward levels (L1 $0.01, L2 $0.10, L3 $1.00 in MCGP), and reward_entries /
-- reward_claimables are an append-only ledger feeding the claim distribution
-- (claimable MCGP is never decremented). Deleting the TP while leaving those entries
-- would leave a level with no TP behind it. Reversing both is a deliberate, separately
-- reviewed step, taken only once the distribution's state is known.
--
-- Run: psql -v ON_ERROR_STOP=1 -f <this file>

-- 1. Everyone credited by a signup claim.
SELECT u.username, e.tp_earned, e.created_at, u.tp_balance
FROM tp_earnings e
JOIN users u ON u.id = e.user_id
WHERE e.source_type = 'signup_reward'
ORDER BY e.created_at;

-- 2. What those TP unlocked: levels awarded, and the MCGP they made claimable.
SELECT u.username, s.highest_awarded_level, c.claimable_mcgp
FROM tp_earnings e
JOIN users u ON u.id = e.user_id
LEFT JOIN user_reward_states s ON s.user_id = e.user_id
LEFT JOIN reward_claimables c ON c.user_id = e.user_id
WHERE e.source_type = 'signup_reward'
GROUP BY u.username, s.highest_awarded_level, c.claimable_mcgp;

-- 3. The level reward entries themselves (each level is recorded once, ever).
SELECT u.username, r.source_level, r.reward_usd, r.mcgp_amount, r.created_at
FROM reward_entries r
JOIN users u ON u.id = r.user_id
WHERE r.kind = 'level'
  AND r.user_id IN (SELECT user_id FROM tp_earnings WHERE source_type = 'signup_reward')
ORDER BY u.username, r.source_level;

-- 4. Would the user still be at that level without the 100 TP? (tp_balance less the credit.)
SELECT u.username, u.tp_balance AS tp_now,
       u.tp_balance - e.credited AS tp_without_signup_reward
FROM users u
JOIN (SELECT user_id, sum(tp_earned) AS credited
      FROM tp_earnings WHERE source_type = 'signup_reward' GROUP BY user_id) e ON e.user_id = u.id;
