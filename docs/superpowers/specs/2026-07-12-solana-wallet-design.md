# Solana Wallet — Design

**Date:** 2026-07-12
**Status:** Approved — implementation
**Parent spec:** `2026-07-09-multichain-wallet-nonevm-design.md` (this is the Solana vertical delta)
**Scope:** Wallet feature only. Receive + balances + **send** + history for Solana (SOL + SPL), self-custody, plus auto-discover / manual-import of arbitrary SPL tokens.

---

## 1. Context

Tron and BTC are already built and merged as the reference non-EVM verticals. Solana is the third
and final chain in the parent spec's cycle-1. The scaffolding was laid ahead of time:

- `deriveSolanaAddress` already exists in `tsa-app/services/chains/derivation.ts` (SLIP-0010
  ed25519, Phantom path `m/44'/501'/0'/0'`, base58 pubkey). Interop is asserted against Phantom.
- `family: 'solana'` / `chainKey: 'solana'` already appear in the app's type unions
  (`constants/chains.ts`) and in the backend `NonEVMChainConfig`.

What is missing is the actual vertical: the backend `ChainClient` implementation, config + registry
wiring, the app signing/send path, screen wiring, and SPL-token support.

## 2. Decisions locked in

| Decision | Choice | Rationale |
|---|---|---|
| RPC provider | **Public RPC** (`api.mainnet-beta.solana.com` / `api.devnet.solana.com`), dormant until an env URL is set | Zero setup for the first cut. Swap to Helius (API key) before real launch for SLA + webhooks. |
| Deposit detection | **Reconcile-only** to start | Same as Tron today. The non-EVM reconcile worker polls active addresses. Helius webhooks deferred. |
| SPL tokens | **Auto-discover + manual import** | `getTokenAccountsByOwner` lists all held tokens; users can also paste a mint to import one. Reuses the existing EVM custom-token flow. |
| Signing split | App signs (ed25519), backend builds txs | Unchanged from parent spec. App holds only derivation + `@noble` ed25519 signing; all tx construction in Go. |
| Blockhash expiry | Re-prepare + re-sign once | Durable nonce deferred; the re-sign-once mirrors the EVM "nonce too low" retry. |

### 2.1 Derivation — already pinned (parent spec §2.1)

| Chain | Path | Curve | Matches |
|---|---|---|---|
| Solana | `m/44'/501'/0'/0'` | ed25519 (SLIP-0010, all-hardened) | Phantom |

Address = base58 of the 32-byte ed25519 public key (no checksum). Already implemented and
test-vectored in `derivation.ts`.

## 3. Goals / Non-goals

**Goals**
- Receive (address + QR), balances (SOL + **all** held SPL tokens), send (SOL + SPL), history.
- Fee preflight that blocks a no-SOL send with a clear message, and includes **ATA-creation rent**
  when the recipient has no associated token account for the mint being sent.
- Auto-discovery of held SPL tokens + manual mint import, reusing the existing custom-token system.
- Dormant-by-default: absent RPC config → chain not registered → routes return "chain unavailable".

**Non-goals (this cycle):** Helius webhooks / push deposits; swap/bridge; durable nonce; priority-fee
tuning beyond a fixed compute-unit price; SPL token-2022 extensions (standard SPL only for MVP);
multi-signer / versioned-lookup-table txs.

## 4. Architecture

### 4.1 Backend — `internal/chains/solana/` (new package)

Implements the existing `chains.ChainClient` interface (`internal/chains/chain_client.go`), mirroring
`tron/` and `btc/`. Registered per (network, chainKey) so mainnet + devnet can be live at once.

- **`rpc.go`** — JSON-RPC client over the configured RPC URL. Methods used:
  `getBalance`, `getTokenAccountsByOwner` (jsonParsed), `getMultipleAccounts`, `getLatestBlockhash`,
  `getFeeForMessage`, `getSignatureStatuses`, `getSignaturesForAddress`, `sendTransaction`,
  `getAccountInfo` (recipient ATA existence + mint metadata for import).
- **`address.go`** — validate a base58 ed25519 pubkey (decodes to exactly 32 bytes; on the ed25519
  curve is not separately checked — Solana itself accepts off-curve system addresses).
- **`message.go`** — build + serialize a **legacy** Solana message (System `transfer` for SOL; SPL
  `transfer`/`transferChecked` preceded by a conditional `createAssociatedTokenAccount` instruction
  for the recipient). Programs: System, SPL Token, Associated-Token. Kept legacy (not v0) to avoid
  lookup-table complexity. This is the one piece with no Go SDK dependency by default — see §7.
- **`client.go`** — the `ChainClient`:
  - `Family() → "solana"`
  - `GetBalances` — `getBalance` (SOL, 9 decimals, native) + `getTokenAccountsByOwner` → one
    `Balance` per held SPL mint (decimals from the parsed account; symbol from the config token
    registry, else the mint address). **This is the auto-discovery of "any SOL token".**
  - `EstimateFee` — base fee (5000 lamports × sigs) + **ATA rent (~2 039 280 lamports ≈ 0.00204 SOL)
    when the recipient lacks an ATA for the mint** + a fixed priority fee. `HasEnoughGas` compares the
    sender's SOL balance; `Note` = "You need ~X SOL to send <symbol>." when short. `GasSymbol: "SOL"`.
  - `PrepareTransfer` — validate recipient; fetch a fresh blockhash; build the message; return
    `PreparedTx{ Family:"solana", Raw:<serialized message + metadata JSON>, Payloads:[{Index:0,
    HashHex:<serialized-message hex>, Curve: ed25519}] }`. Note: ed25519 signs the **whole message**,
    so `HashHex` carries the full serialized message bytes, not a 32-byte digest (the field is opaque
    hex — same contract as Tron's txID).
  - `AssembleAndBroadcast` — decode the 64-byte ed25519 signature, place it in the tx signature slot,
    base64-encode the wire tx, `sendTransaction` (skipPreflight=false). Returns the signature (txid).
  - `GetTxStatus` — `getSignatureStatuses`; map `confirmed`/`finalized` → `Confirmed`; the `err`
    field → `Success:false` so a landed-but-failed tx is not reported as success.
  - `History` — `getSignaturesForAddress` (bounded window, cursor deferred like Tron) enriched via
    parsed transactions to set direction/amount/symbol/counterparty.

### 4.2 Backend — config + wiring

- `config.go`: `addSolana(network, rpcURL)` mirroring `addTron`/`addBTC`. Env
  `SOLANA_MAINNET_RPC_URL` / `SOLANA_DEVNET_RPC_URL` (+ back-compat `SOLANA_RPC_URL` + `SOLANA_NETWORK`).
  `NativeSymbol:"SOL"`, `NativeDecimals:9`, `Tokens` seeded from `SOLANA_MAINNET_USDC_MINT` etc. Unset
  URL → not added → dormant.
- `main.go`: add `case "solana":` to the `cfg.NonEVMChains` registry loop.
- `jobs/nonevm_reconciler.go`: Solana falls out of the family switch for free (it walks all registered
  addresses via the registry + `GetTxStatus`); confirm it has no EVM/Tron-specific assumptions and add
  a Solana branch only if one is needed.

### 4.3 App — signing + send + wiring

- `derivation.ts`: add `deriveSolanaPrivateKey(mnemonic)` returning the 32-byte ed25519 seed key hex
  (companion to the existing `deriveSolanaAddress`).
- `sign.ts`: add `signEd25519Message(msgHex, privKeyHex)` → 64-byte hex signature via
  `@noble/curves/ed25519`.
- `services/chains/solanaSend.ts`: prepare→sign→submit orchestration, mirroring `tronSend.ts` /
  `btcSend.ts`; branch on `family === 'solana'`. Handles the blockhash-expired re-prepare-once retry.
- `constants/chains.ts`: add `solana` to `NONEVM_CHAINS` and `NONEVM_CHAIN_KEYS` (currently
  `['tron','btc']`) so Solana appears in the wallet / receive / send / token lists. `family:'solana'`
  is already in the union.
- Send/receive screens already branch on `family` and call `nonevmApi.ts`; adding Solana to
  `NONEVM_CHAIN_KEYS` + the send switch is the wiring.

### 4.4 "Show / import any SOL token"

Reuses the existing custom-token system (`hooks/useTokens.tsx`, `customTokens`, the `import-token`
screen, `getDiscoveredTokens`) rather than building a new one:

- **Auto-discover:** `GetBalances` already returns every held SPL token → they surface in the token
  list with on-chain balances, no user action.
- **Manual import:** the `import-token` screen accepts a **Solana mint address** when the active chain
  is Solana. The backend resolves the mint's decimals/symbol via `getAccountInfo`/`getMultipleAccounts`
  on the mint account; stored as a custom token keyed `solana:<mint>`. Invalid / non-mint addresses are
  rejected with a clear error.

## 5. Confirmation & reconcile

`GetTxStatus` is Solana-specific: `confirmed` vs `finalized` commitment; a tx can be dropped if the
blockhash expired before it landed. The non-EVM reconcile worker walks non-terminal Solana sends +
active receive addresses and corrects DB drift against chain truth (reconcile-only deposits).

## 6. Error handling & edge cases

- **Dormant chain:** no RPC URL → not registered → "chain unavailable"; server still boots.
- **No-SOL send:** blocked at `EstimateFee` with "you need SOL" — never a failed submit.
- **Recipient has no ATA:** the send instruction includes `createAssociatedTokenAccount`; the ~0.00204
  SOL rent is added to the fee quote and paid by the sender.
- **Blockhash expiry:** re-prepare + re-sign once, then surface a clear "please retry" if it still fails.
- **Address validation** (base58 → 32 bytes) before any prepare, to avoid wasted broadcasts.
- **Manual import of a bad mint:** `getAccountInfo` returns non-mint / missing → reject with a message.

## 7. Library decision (architectural call — decided during execution)

**Decision: use `github.com/gagliardetto/solana-go` server-side for tx construction, RPC, and message
serialization** (the parent spec's named choice), behind the `ChainClient` interface.

I initially considered hand-rolling the serializer to avoid the dependency, but building an SPL send
correctly requires deriving the recipient's **Associated Token Account** — a program-derived address
that needs ed25519 on-curve checking (`findProgramAddress`) plus exact byte layouts for the System /
SPL-Token / Associated-Token instructions and Solana's shortvec message encoding. Hand-rolling all of
that for **money movement** is unjustified risk when a battle-tested library exists.

The parent spec's supply-chain caution was about `@solana/web3.js` — the *JavaScript, on-device*
library (Dec-2024 key-exfil incident). That concern does not transfer here: `solana-go` runs
**server-side only**, and the server never holds a seed or signs — the app still signs on-device with
`@noble` ed25519. So the heavy SDK stays off the user's device exactly as the spec's "heavy chain SDKs
live server-side" principle intends. The cost is server-side transitive dependencies (acceptable in a
Go API that already depends on go-ethereum, GORM, etc.); the `ChainClient` boundary keeps the choice
local and swappable.

## 8. Testing

- **Interop vector:** known mnemonic → address matches Phantom (already asserted in `derivation.test.ts`;
  extend to cover the private key + a known signature if practical).
- **Fee preflight:** SOL-only account sending USDC → ATA-create + rent included; no-SOL account →
  blocked with the correct message.
- **Client unit tests (mocked RPC):** balance parse (SOL + multiple SPL), message build + serialize for
  SOL and SPL (with/without ATA-create), ed25519 assemble, `confirmed`/`finalized` status mapping,
  history parse.
- **Message serializer golden tests:** serialized bytes for a fixed input match a known-good vector
  (guards the hand-rolled serializer from §7).
- **Dormant-mode test:** no RPC config → clean "chain unavailable", server boots.
- **Devnet E2E (manual/documented):** derive → receive → balance → send SOL + SPL → confirm → history.

## 9. Rollout order

1. App: `deriveSolanaPrivateKey` + `signEd25519Message` + interop/signature vectors green.
2. Backend: `solana/` package — rpc, address, message serializer (+ golden tests), client (+ unit tests).
3. Backend: config `addSolana` + `main.go` registration + reconciler branch.
4. App: `solanaSend.ts` + `constants/chains.ts` wiring (NONEVM_CHAINS / NONEVM_CHAIN_KEYS).
5. SPL token import: extend `import-token` + backend mint-resolution endpoint.
6. Devnet E2E pass; document the paid-RPC (Helius) switch for launch.

## 10. Open items (resolved during execution)

- Confirm the non-EVM reconciler needs no Solana-specific branch (expected: none).
- Confirm the price feed covers SOL for fiat display (reuse existing; SOL is a top asset).
