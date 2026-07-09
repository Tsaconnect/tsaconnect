# Multi-Chain Wallet — Non-EVM Chains (Tron, BTC, Solana)

**Date:** 2026-07-09
**Status:** Design — awaiting review (revised after independent adversarial review)
**Scope:** Wallet feature only (not marketplace/P2P). Receive + balances + **send**, self-custody.
**Chains, in build order:** Tron → Bitcoin → Solana. (TON, SUI = later cycle. Pi = dropped. Robinhood Chain = deferred, trivial EVM config add if it ever goes public.)

---

## 1. Background & the core problem

The TSA wallet is **100% EVM today**, top to bottom:

- Keys: BIP-39 mnemonic in `expo-secure-store`, derived + signed with **ethers.js** (secp256k1, default path `m/44'/60'/0'/0/0`, EIP-155). App signs locally; backend never holds keys.
- Tx construction: pure `go-ethereum` (`internal/blockchain/`), unsigned-tx JSON → app signs → `/submit-tx` broadcasts.
- Balance reads: `ethclient` (backend) + `ethers.JsonRpcProvider` (app). EVM already guards gas via `ensureGas` (`wallet.ts`).
- Swaps: LiFi, `chainTypes=EVM` only. Confirmation drift is corrected by the chain reconciler + timeout watchdog (a CLAUDE.md cross-cutting invariant).
- Multi-wallet: the app supports **N wallets = N mnemonics** (`tsa-wallet-mn-{address}`).

The PM wants BTC, Solana, TON, Tron, SUI, Pi, and Robinhood Chain added. **Six of seven are non-EVM** and share nothing with the current stack. "Build all seven with swaps" is not feasible for a solo developer. This spec covers the **feasible subset**: the three highest-value non-EVM chains, receive + balances + send, reusing the existing self-custody model. Swap is deferred.

## 2. Decisions locked in

| Decision | Choice | Rationale |
|---|---|---|
| Chains, cycle 1 | Tron, BTC, Solana | Highest value / lowest effort. Tron carries TRC-20 USDT rails (core to Nomba/Africa). |
| Build order | Tron → BTC → Solana | Tron is the reference vertical. |
| Depth | Receive + balances + **send** | "Send is integral." Swap deferred. |
| Key management | **Reuse existing self-custody mnemonic** — no Turnkey | One BIP-39 seed the user already backed up covers all chains. Turnkey = $0.10/send forever; reuse = $0, single custody model. |
| **Signing split** | **App signs; backend builds txs** | App holds only derivation + raw-signing primitives. All chain-specific tx construction lives in Go. Minimizes RN library/supply-chain/bundle risk. |
| Dropped / deferred | Pi dropped; Robinhood deferred | No open liquidity (Pi); not a public wallet chain (Robinhood). |

### 2.1 Derivation paths — PINNED (this is load-bearing for the "one seed phrase" promise)

The user's single mnemonic derives a **different key per chain** (same seed, standard paths). "Reuse" means *one mnemonic*, **not** that the EVM private key signs other chains. Paths are pinned to match the dominant external wallets so an exported seed reproduces the **same addresses** elsewhere:

| Chain | Path | Matches | Note |
|---|---|---|---|
| EVM (existing) | `m/44'/60'/0'/0/0` | MetaMask etc. | unchanged |
| **Tron** | `m/44'/195'/0'/0/0` | TronLink, imToken | **standard Tron key**, *not* the EVM key. (Rejected: `m/44'/60'` reuse — gives a 1:1 EVM↔Tron map but shows a different address than every other Tron wallet on import = support fire.) |
| **Bitcoin** | `m/84'/0'/0'/0/0` (**BIP-84**) | native SegWit wallets | P2WPKH `bc1...`. (BIP-44 `m/44'/0'` would give legacy `1...` — wrong.) |
| **Solana** | `m/44'/501'/0'/0'` | **Phantom** | ed25519 via SLIP-0010. (Solana wallets disagree on path; Phantom is the de-facto standard.) |

BIP-39 passphrase is empty everywhere (ethers uses empty; keep consistent). **§10 interop vectors assert each address matches what TronLink / a BIP-84 wallet / Phantom produce from the same seed** — not just internal determinism.

## 3. Goals / Non-goals

**Goals**
- One seed phrase covers EVM + Tron + BTC + Solana (standard paths, externally reproducible).
- Receive (address + QR), balances (native + key tokens), **send**, and **transaction history** for all three.
- A `family` abstraction so UI, readers, and tx builders branch off a common interface, not hard-coded EVM.
- Per-family **gas/fee handling** so USDT-only users get a clear "you need TRX/SOL to send" path, never a silent revert.
- Per-family **confirmation + reconcile** (BTC N-conf, Solana sub-second, Tron ~3s differ materially).
- **Aggressively conserve free RPC/indexer tiers** (§7) — pilot ~$0/mo, scale stays cheap.
- Dormant-by-default: absent RPC config → clear error, dev stays bootable.

**Non-goals (this cycle):** swap/bridge for non-EVM (later; Rango/Symbiosis); TON/SUI/Pi/Robinhood; marketplace/P2P for these chains; Turnkey/MPC; BTC multi-address/gap-limit scanning (single-address MVP, §11); non-EVM swaps.

## 4. Cost model (for the PM)

Key management = **$0** (reuse mnemonic). RPC/indexer is the only line.

| Phase | Cost | Notes |
|---|---|---|
| Pilot / MVP | **~$0/mo** | Free RPC tiers + free deposit webhooks. |
| Early growth (~500 users, ~2k sends/mo) | **~$49/mo** | Solana RPC (Helius Developer); Tron/BTC free tiers. |
| Real launch / scale | **~$150–300/mo** | Paid RPC on all three for **reliability/SLA** (not capacity — free tiers have the throughput, not the guarantees). |

No per-tx fee ever; sends cost only the chain's own gas, paid by the user. Two lines the PM should know are **not** in the above and will appear later: a **price feed** for fiat balance display (may reuse an existing feed), and any **indexer/webhook** upgrade beyond free tiers for history/deposits at scale. "BTC free at scale" is optimistic — mempool.space free has no SLA; treat it as reliability spend, not capacity.

**Free-tier reference limits** (peak-qps is what matters — traffic is bursty, everyone opens the app in the morning, and one shared gateway means the whole userbase shares one qps ceiling):

| Chain | Free provider | Limit | Fallback | Free deposit push |
|---|---|---|---|---|
| Tron | TronGrid | 100K req/day, 15 qps | GetBlock/QuickNode | none free → reconcile only |
| Bitcoin | mempool.space | ~10 rps | Blockstream Esplora | **mempool.space WS address sub** |
| Solana | Helius free | 1M credits/mo, 10 rps | public RPC (dev only) | **Helius webhooks (free tier)** |

## 5. Architecture

### 5.1 The `family` abstraction

A `family` discriminator: `evm | tron | btc | solana`. Everything that assumes EVM branches on it.

**Backend — `ChainClient` interface** (Go), one impl per family, wrapped by the shared caching + rate-limiting layer (§7):

```go
type ChainClient interface {
    Family() string
    ValidateAddress(addr string) error
    GetBalances(ctx, addr string) ([]Balance, error)      // native + tokens, fewest upstream calls
    EstimateFee(ctx, req TransferRequest) (FeeQuote, error) // gas-token, amount, whether new-account/ATA fee applies
    PrepareTransfer(ctx, req TransferRequest) (UnsignedTx, []SigningPayload, error) // returns hash(es) to sign
    AssembleAndBroadcast(ctx, unsigned UnsignedTx, sigs []Signature) (txHash string, err error)
    GetTxStatus(ctx, txHash string) (TxStatus, error)     // family-specific confirmation semantics
    History(ctx, addr string, cursor string) ([]Activity, string, error)
}
```

- Registered in `map[chainKey]ChainClient`. Absent RPC config → not registered → routes return "chain unavailable" (dormant-by-default).
- The existing EVM path is refactored **behind** this interface as `family: evm` — no behavior change, but it removes special-casing and proves the abstraction.
- **Go tx-construction libs live here:** `btcsuite/btcd` + `btcutil` (BTC PSBT/sighash/coin-selection), `gagliardetto/solana-go` (Solana tx + ATA), TronGrid HTTP (`createtransaction` / `triggersmartcontract`) for Tron. Supply-chain/bundle concerns are far lower server-side than in the RN bundle.

**App — minimal crypto only.** The app does **not** bundle bitcoinjs-lib / TronWeb / @solana/web3.js. It holds:
- Derivation: `@scure/bip39` (mnemonic→seed), `@scure/bip32` (secp256k1 HD), SLIP-0010 (ed25519) — audited, pure-JS, no WASM.
- Signing primitives: `@noble/curves` (secp256k1 ECDSA + ed25519), `@noble/hashes`.
- It receives signing payloads (hashes/messages) from the backend, signs them with the derived key, returns the signature(s). No chain-specific SDK, no `tiny-secp256k1` WASM (Hermes-hostile), no `@solana/web3.js` (Dec-2024 key-exfil supply-chain history).

### 5.2 Send flow (mirrors EVM prepare/submit; now fee-aware and family-correct)

1. **Enable:** app derives the per-chain address (pinned paths, §2.1), registers it: `POST /wallet/addresses {wallet_id, chain, family, address}` → `wallet_addresses`.
2. **Fee preflight (`EstimateFee`) — CRITICAL:** before showing "send", backend returns the gas-token, estimated fee, and any surcharge. The UI **blocks and explains** if the user lacks native gas:
   - **Tron:** requires TRX for bandwidth/energy; TRC-20 needs a mandatory **`fee_limit`** (without it the tx *silently reverts*); sending to a never-activated account costs **~1 TRX activation**. Show "You need ~X TRX to send USDT."
   - **Solana:** requires SOL for the base fee **plus ~0.002 SOL rent if the recipient has no ATA** (the send instruction must include `createAssociatedTokenAccount`); set a **compute-unit price (priority fee)** or it may not land under congestion.
   - **BTC:** fee paid in BTC (sat/vB from mempool.space fee API); enforce the **dust limit** (~294 sat), fold sub-dust change into fee, iterate coin-selection because adding an input changes size changes fee.
3. **Prepare:** `POST /wallet/prepare-tx` → backend builds the unsigned tx and returns the **signing payload(s)** (Tron txID; each BTC input sighash; Solana serialized message).
4. **Sign:** app signs the payload(s) locally with the derived key (`@noble` secp256k1/ed25519). Returns signature(s) only.
5. **Submit:** `POST /wallet/submit-tx {chain, unsignedRef, sigs}` → backend assembles the signed tx and broadcasts.
6. **Confirm/reconcile (§5.4):** backend tracks confirmation with **family-specific** semantics, emits a WS event, invalidates the sender's balance cache.

**Timing quirks handled:** Tron ref-block/expiration window; BTC RBF fee-bump for stuck low-fee txs; Solana blockhash expiry (~150 slots / 60–90s) → re-prepare + re-sign once (mirrors the EVM "nonce too low" retry), or use a **durable nonce** to sidestep the tight mobile window.

### 5.3 Data model

- **`wallet_addresses`** — `(user_id, wallet_id, chain_key, family, address, created_at)`, unique on **`(wallet_id, chain_key)`**. Keyed on `wallet_id` (not just `user_id`) to respect the existing **multi-wallet** reality — every wallet/mnemonic gets its own per-chain addresses, and the reconciler walks all registered addresses. EVM's existing single-address registration backfills opportunistically.
- **`wallet_activity`** — cached, normalized transaction history per `(chain_key, address)` (hash, direction, amount, token, counterparty, status, block_time, confirmations), hydrated from indexer calls (§6) and updated by confirm/reconcile. Avoids re-hitting the indexer on every history view.
- Token registry: non-EVM tokens keyed `"chain:symbol"` (`"tron:USDT"` TRC-20 contract, `"solana:USDC"` SPL mint), config-baked, long-TTL.

### 5.4 Confirmation & reconcile (money movement — do not treat chains uniformly)

`GetTxStatus` is family-specific:
- **BTC:** 0-conf = pending; surface confirmation count; treat as final at N confirmations (config, e.g. 2–3). ~10 min/block.
- **Solana:** `confirmed` vs `finalized` commitment; sub-second but can be dropped if blockhash expired before landing.
- **Tron:** ~3s blocks; check `receipt.result == SUCCESS` (a broadcast tx can still *revert* — e.g. `fee_limit` too low).

A **non-EVM reconcile worker** (sibling to the existing EVM chain reconciler) walks non-terminal sends + active receive addresses and corrects DB drift against chain truth, per-family. Dropped-between-broadcast-and-confirm is expected and handled, not assumed away.

## 6. Per-chain specifics

| | Tron | Bitcoin | Solana |
|---|---|---|---|
| Path / curve | `m/44'/195'/0'/0/0` / secp256k1 | `m/84'/0'/0'/0/0` / secp256k1 | `m/44'/501'/0'/0'` / ed25519 (SLIP-0010) |
| Address | base58check `0x41` | bech32 P2WPKH `bc1...` | base58 ed25519 pubkey |
| Tokens | TRC-20 (USDT) | native only | SPL (USDC/USDT) via ATAs |
| Balance | TronGrid `/v1/accounts/{addr}` → TRX + TRC-20, **one call** | indexer `/address/{addr}`, **one call** | `getBalance` + `getTokenAccountsByOwner` (batch `getMultipleAccounts`) |
| History | TronGrid tx/event endpoints | indexer address txs | `getSignaturesForAddress` |
| Deposit push | none free → reconcile | mempool.space WS address sub | Helius webhook (free) |
| Gas/fee gotcha | **`fee_limit` mandatory**; ~1 TRX activation for new accts; energy/bandwidth | dust limit; iterative fee/change; RBF | **ATA create + ~0.002 SOL rent**; priority fee/compute budget |
| Effort | **Low** (reference) | **Medium** (UTXO) | **Medium** (ATAs, blockhash) |

## 7. Free-tier conservation (first-class constraint)

Minimize upstream calls per refresh; refresh only when something changed.

1. **Single backend gateway** — all RPC/indexer calls go through the backend, never the app. One choke point for caching, rate-limiting, key rotation, fallback.
2. **Tiered caching** — balances short TTL (~20–30s); token metadata/decimals/mints effectively permanent; block height/blockhash/fee rates fetched **once per interval, shared across all users**.
3. **Single-flight coalescing** — dedupes **shared** upstream data (fee rates, blockhash, metadata) into one call. *Correction from review:* it does **not** help distinct per-address balance reads (different addresses ≠ identical requests) — those are bounded by caching + event-driven refresh, not coalescing.
4. **Event-driven refresh, not polling** (reuses the existing per-user WS / NotificationContext). Refresh only on: app foreground / wallet open, pull-to-refresh, **post-send** (targeted invalidation), and a slow jittered background reconcile for **active** addresses.
5. **Deposit push via free webhooks** — pull **Helius webhooks (Solana)** and **mempool.space WS address subscriptions (BTC)** into scope: event-driven (matches the standing preference), free, and they *reduce* polling load while giving users real "deposit arrived" notifications. Tron has no free push → covered by the reconcile worker; log that coverage gap rather than hiding it.
6. **Per-provider token-bucket sized below the free ceiling, for PEAK qps not daily average** (Tron cap ~10 qps vs 15; mempool ~7 rps vs 10; Helius within 10 rps / 1M credits). On 429: backoff → **circuit-break to fallback** (mempool→Blockstream). Emit a throttle metric so we see pressure before users do.
7. **Minimal-call reads** — prefer the endpoint returning the most per call (TronGrid account = TRX + all TRC-20 at once; BTC address = one call; Solana `getMultipleAccounts` batches native + SPL).

**Budget check (peak-aware):** with caching + event-driven refresh, steady-state upstream traffic is dominated by morning bursts. Token buckets sized to peak qps keep us inside Tron's 15 qps / mempool's 10 rps / Helius's 10 rps; daily/monthly ceilings (100K/day, 1M credits/mo) are comfortable for low-thousands of users. Free tiers hold past pilot; paid tiers buy SLA, not headroom.

## 8. Error handling & edge cases
- **Dormant chains:** absent RPC config → not registered → clear "chain unavailable"; server still boots.
- **No-gas send:** blocked at `EstimateFee` with an explicit "you need TRX/SOL" message — never a silent Tron revert or a failed Solana submit.
- **Solana blockhash expiry:** re-prepare + re-sign once, or durable nonce.
- **BTC:** dust folding, iterative fee, RBF for stuck txs; re-prepare on spent-UTXO/insufficient-fee reject.
- **Address validation** per family before any prepare (base58check / bech32 / ed25519) to avoid wasted broadcasts.
- **Provider outage:** circuit-break + fallback; all-down → balances served stale-with-timestamp from cache, send disabled with a clear message.

## 9. Security
- Signing stays on-device (existing model); backend stores only **public** addresses, never the seed.
- **Library choice is a security decision:** `@scure`/`@noble` (audited, pure-JS, minimal deps) over bitcoinjs-lib+`tiny-secp256k1` (WASM/Hermes issues) and over `@solana/web3.js` (Dec-2024 key-exfil supply-chain incident). Pin exact versions; lockfile-audit in CI. Heavy chain SDKs live server-side (Go), off the user's device.
- Additional app crypto is an extension of the already-accepted EVM self-custody model, covered by derivation test vectors — not a new custody surface.

## 10. Testing
- **Interop derivation vectors:** known mnemonic → addresses that **match TronLink (m/44'/195'), a BIP-84 wallet (bc1...), and Phantom (m/44'/501'/0'/0')** — guards the "one seed phrase, reproducible elsewhere" promise, not just internal determinism.
- **Fee-preflight tests:** USDT-only account → send blocked with correct gas-token message per chain; Tron `fee_limit` present; Solana ATA-create + rent included when recipient lacks an ATA.
- **Family client unit tests** (mocked RPC): balance parsing, tx build, sighash/message, assemble+broadcast, family-specific confirmation.
- **Rate-limit/cache tests:** single-flight on shared data, TTL expiry, 429 → fallback.
- **Testnet E2E** per chain: Tron **Nile**, Bitcoin **testnet/signet**, Solana **devnet** — derive → receive → balance → send (incl. token) → confirm → history. Faucets listed in the plan. §13 covers how testnet selection binds to the app's network toggle.
- Dormant-mode test: no RPC config → clean error, boots.

## 11. Risks
- **BTC UTXO management** (coin selection, iterative fee, dust, RBF) — the fiddliest piece; Tron-first buys time.
- **Free-tier reliability** — fine for pilot; move to paid RPC before real launch (money movement).
- **Solana blockhash lifetime + priority fees** — tight mobile window; mitigate with re-sign-once or durable nonce, and always set a compute-unit price.
- **BTC single-address MVP = address reuse (privacy loss)**; change returns to the same address. Acceptable for MVP; explicitly deferring gap-limit/multi-address scanning. Documented, not silent.
- **`@noble` ed25519 for Solana** must match Phantom/`solana-go` signature expectations — covered by the interop vectors.

## 12. Rollout order
1. Backend `ChainClient` interface + caching/rate-limit/fee-preflight/reconcile scaffolding; refactor EVM behind it (no behavior change).
2. App derivation + `@noble` signing primitives; interop vectors green for all three paths **before** any chain UI.
3. **Tron** vertical E2E (reference): derive, receive, balance, history, fee-preflight, send (TRX + TRC-20 with `fee_limit`), confirm/reconcile, Nile E2E.
4. **Bitcoin** vertical (PSBT/UTXO/dust/RBF, mempool WS deposits).
5. **Solana** vertical (ed25519, SPL + ATA/rent, priority fee, Helius webhook deposits).
6. Harden free-tier conservation (peak-qps buckets, single-flight, fallback, throttle metrics); document the paid-RPC switch for launch.

## 13. Open items to confirm during planning
- **Network toggle binding:** the app's mainnet/testnet toggle is currently an EVM-coupled unit; specify how non-EVM testnet selection (Nile / BTC-testnet / devnet) binds to it.
- **Price feed** for fiat balance display — reuse existing or add a line.
- **Deposit-notification scope:** confirm Solana+BTC webhooks are in for cycle 1 (recommended — free + event-driven) vs. reconcile-only for all three to start.

Swap (Rango/Symbiosis) and TON/SUI are separate future specs.
