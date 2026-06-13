# Fee/TP Overhaul — SP9 Scale Notes

**Status:** Done (lightweight validation) · **Date:** 2026-06-08 · **Branch:** `feat/fee-tp-overhaul`
**Scope:** SP9 of the fee/TP overhaul (design: `2026-06-07-fee-tp-overhaul-design.md` §3, §4, §8).

This is a **lightweight** scale validation, proportionate to an early-stage platform
with few users. It does **not** build a load-test harness. Instead it uses targeted Go
benchmarks (`internal/services/fee_tp_scale_bench_test.go`, on the existing in-memory
SQLite test setup) to confirm the **complexity claims** the design makes about its hot
paths — specifically, that the per-fee and per-epoch paths stay flat as the user table
grows, and that the one O(eligible) path (merkle build) is linear and acceptably fast at
admin-publish cadence.

## How to reproduce

```bash
cd tsa-api-go
# Bounded fast paths (fixed iteration count so seeding isn't repeated):
go test -bench '^Benchmark(OnFeeSettled|SettleGlobalPoolEpoch|SettleUserGlobalShare)$' \
    -benchmem -benchtime=300x -run '^$' ./internal/services/
# O(eligible) merkle build (slow at 10k — keep iterations small):
go test -bench '^BenchmarkBuildDistribution$' -benchmem -benchtime=5x -run '^$' ./internal/services/
```

### SQLite caveat (read the numbers as curves, not latencies)

The in-memory test DB is pinned to **one connection** (`MaxOpenConns(1)` — in-memory
SQLite is per-connection). Absolute `ns/op` is therefore **higher** than production
PostgreSQL would be, and serialized. What's load-bearing is the **shape** — flat vs
growing in user count, and linear vs worse in eligible count — which is DB-independent.
Treat the milliseconds below as relative, not as production SLAs.

---

## Results (AMD EPYC, single-conn in-memory SQLite)

| Hot path | Runs on | Claimed complexity (in user count) | Bench (1k → 10k users) | allocs/op | Verdict |
|---|---|---|---|---|---|
| `OnFeeSettled` | **every fee** | ~constant (≤10-gen bounded walk + bounded write) | **2.88 ms → 2.69 ms** (flat) | 2648 → 2647 (flat) | **SAFE** |
| `SettleGlobalPoolEpoch` | per epoch (daily) | O(1) writes + one `COUNT(*)` | **0.44 ms → 0.88 ms** (~2.4× for 10×) | 331 → 331 (flat) | **SAFE (watch the COUNT)** |
| `SettleUserGlobalShare` | per user (lazy, on read/claim) | O(1) per user | **0.38 ms → 0.35 ms** (flat) | 379 → 379 (flat) | **SAFE** |

| `BuildDistribution` | per publish (admin-triggered) | O(eligible users) | 100 → **19 ms**, 1k → **116 ms**, 10k → **1.27 s** | 21k → 217k → 2.28M (linear) | **SAFE now, WATCH at scale** |

---

## Per-path analysis

### 1. `OnFeeSettled` — runs on every fee — **SAFE**

The hottest path (one call per settled platform fee). Each call:
- resolves uplines gen1..gen10 via **one bounded recursive-CTE walk** (depth-capped at
  `MaxTPGenerations` = 10, cycle-guarded), then
- does a **bounded write**: ≤11 `TPEarning` rows + ≤2 `CashbackEarning` rows + 1 guard
  row + any global-pool accrual, all in one tx.

Nothing in that depends on how many *other* users exist. The benchmark holds the referral
chain fixed at the full 11-deep shape and varies only the unrelated filler population
(1k vs 10k): **2.88 ms → 2.69 ms** (the dip is noise) and **allocs identical** (2648 vs
2647). **Constant in total user count — claim confirmed.**

*Note:* `OnFeeSettled` is already run async/off the request path (`EnqueueFeeSettled`), so
even its few-ms cost never blocks an HTTP response.

### 2. `SettleGlobalPoolEpoch` — per epoch — **SAFE (watch the COUNT)**

The MasterChef `accRewardPerShare` fold: advance **one scalar index** + write **one epoch
audit row** per epoch, regardless of population. The benchmark confirms the **write work
is O(1)** — allocs are identical at 1k and 10k (331). The only user-count-dependent term
is the `COUNT(*) FROM users WHERE deleted_at IS NULL` denominator, and that shows up:
wall-time roughly **doubles** 1k→10k (0.44 ms → 0.88 ms) purely from the count scanning
more rows on SQLite.

This is exactly the "O(1) writes + one COUNT" the design (§4) claims, and it is a complete
non-issue at current scale: it runs **once per epoch (default daily)**. Even at 1M users it
is one aggregate query per day. On PostgreSQL the count is an index-only/visibility-map scan
(far cheaper than SQLite's full scan here). **Watch, don't fix:** if epochs ever go
high-frequency *and* the table reaches millions, cache the registered-user count (or keep a
maintained counter) so the fold needs no live COUNT. Milestone below.

### 3. `SettleUserGlobalShare` — per user (lazy) — **SAFE**

Lazy per-user credit: read the global index, lock **one** user row, write **one**
`TPEarning` + one balance update, advance the checkpoint. No scan of other users. Benchmark
is flat (0.38 ms → 0.35 ms) with identical allocs (379) across 1k/10k. **O(1) per user —
claim confirmed.** Because crediting is lazy (computed on read/claim, not pushed per epoch),
the global pool never incurs an O(users) write per epoch — the core design win.

### 4. `BuildDistribution` — per publish (admin) — **SAFE now, WATCH at scale**

This is the **one deliberately O(eligible)** path, and the design accepts that because
publishing is **infrequent and admin-triggered** (§8 SP9). The benchmark confirms clean
linear scaling — **100 → 19 ms, 1k → 116 ms, 10k → 1.27 s** (≈10× per 10×; the 100-leaf
case looks slightly super-linear only because of fixed per-build overhead). Allocs scale
linearly (21k → 217k → 2.28M).

At today's scale this is fine — even 10k eligible users is ~1.3 s on the worst-case
single-connection SQLite (and faster on PostgreSQL), for an action an admin runs maybe
daily. **Two things to keep an eye on as eligible-set grows** (neither warrants work now):

- **N+N per-user queries dominate the cost, not the tree math.** The build loops over every
  positive claimable and issues, *per user*, (a) an `IsClaimEligible` (SP7) check and (b) a
  `First(&user)` wallet lookup — i.e. ~2N round-trips, which is most of the wall-time at 10k.
  The in-memory merkle tree build itself is cheap by comparison. If the eligible set reaches
  tens of thousands, **batch these** (one `IN (...)` wallet+eligibility load) before anything
  else — it's the highest-leverage, lowest-risk optimization.
- **The whole tree is held in memory.** Leaves, all layers, and one persisted proof per leaf
  (a JSON array that grows ~log₂(N) per entry) are built and stored in RAM for the duration
  of the build. At 10k that's ~180 MB allocated over the build (transient, GC'd after). Fine
  now; at ~100k+ eligible it would be worth streaming/chunking the persist and/or storing
  proofs more compactly.

---

## Real risks flagged (honest) + the milestone to revisit each

Keep all of these **filed, not fixed** at current scale. Suggested revisit milestones:

| Risk | Where | Why it's fine now | Revisit at |
|---|---|---|---|
| **Dirty-set can grow unbounded if the reward processor falls behind.** `OnFeeSettled` marks every touched recipient dirty; `ProcessDirtyUsers` drains in batches (default 500/tick). If accrual outruns processing, the dirty backlog grows without bound. It is self-healing (idempotent, FIFO-ish) and never *loses* a reward, but a sustained backlog delays payouts and the dirty table grows. | `reward_service.go` `MarkUserDirty` / `ProcessDirtyUsers` | Fee volume is low; one tick easily drains the backlog. No metric yet on backlog depth. | **Add a `dirty` backlog gauge/alert** once daily fee events exceed the per-tick batch × ticks-per-interval (rough: > ~tens of thousands of fee events/day). Then size the batch/interval to the arrival rate, or move to a real queue (the River Phase-2 seam the design already notes). |
| **Merkle tree build is O(eligible) and held in memory.** See path #4. Linear time + linear memory; ~1.3 s / ~180 MB at 10k eligible on SQLite. | `reward_merkle_service.go` `BuildDistribution` | Admin-triggered, infrequent; 10k is ~1 s. | **At ~10k eligible:** batch the per-user eligibility + wallet lookups (kill the 2N round-trips). **At ~100k eligible:** stream/chunk the persist and reconsider in-RAM proof storage. |
| **Per-fee `SettleUserGlobalShare` lock contention.** Each call takes a `FOR UPDATE` row lock on the **user** being settled (and reads the singleton pool row). Per-user locks don't contend across different users, but `ProcessUserRewards` calls it inside the per-user reward tx — so a hot user settled concurrently from two paths serializes. The singleton-pool **read** is also a shared point, and `SettleGlobalPoolEpoch` takes `FOR UPDATE` on that same singleton row. | `fee_distribution_settlement.go` `SettleUserGlobalShare` / `SettleGlobalPoolEpoch` | Low concurrency; per-user locks are the right granularity and don't cross users. The epoch fold is once/day. | **Watch if** reward processing is ever parallelized across workers AND the same users are hot. The singleton-pool `FOR UPDATE` in the epoch fold is the one true global serialization point — fine at one fold/day, but don't make epoch folds high-frequency without revisiting it. |
| **`SettleGlobalPoolEpoch` live `COUNT(*)` of users.** See path #2. One COUNT per epoch; sub-linear here, cheaper on PostgreSQL. | `fee_distribution_settlement.go` | Once/day, one aggregate. | **At ~1M users *and* high-frequency epochs:** maintain a cached registered-user counter instead of a live COUNT. Not before. |
| **SQLite batch-insert bind-variable cap (test-only, but a production hint).** At 10k leaves, GORM's single-statement `Create(&entries)` (9 cols × 10k rows = 90k binds) exceeds SQLite's variable limit; the bench works around it with `CreateBatchSize`. **PostgreSQL has no such low cap**, so this is not a production bug — but a 90k-parameter single INSERT is still worth chunking. | `reward_merkle_service.go` `BuildDistribution` (entries insert) | PostgreSQL accepts the large batch; current eligible sets are small. | **Set a sane `CreateBatchSize`** on the publish insert before eligible sets reach low-thousands, so the publish is chunked rather than one giant statement. Cheap, do it opportunistically. |

## Bottom line

All three per-fee / per-epoch hot paths behave exactly as the design claims — **flat in
total user count** (`OnFeeSettled`, `SettleUserGlobalShare`) or **O(1) write + one COUNT**
(`SettleGlobalPoolEpoch`). The cumulative-index lazy-settlement design successfully avoids
any O(users)-per-epoch write. The only linear-in-population path, `BuildDistribution`, is
the intended trade-off (admin cadence) and is comfortably fast at 10k eligible. **Nothing
needs work for an early-stage platform.** The items above are filed with concrete milestones
so they get attention at the right user-count, not pre-emptively.
