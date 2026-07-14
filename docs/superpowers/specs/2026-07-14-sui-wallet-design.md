# Sui Wallet — Design

**Date:** 2026-07-14
**Status:** Approved — implementation
**Parent spec:** `2026-07-09-multichain-wallet-nonevm-design.md` (this is the Sui vertical delta)
**Sibling:** `2026-07-12-solana-wallet-design.md` (Solana is the closest analog — same ed25519 curve)
**Scope:** Wallet feature only. Receive + balances + **send** + history for Sui (SUI + Coin<T> tokens),
self-custody, plus auto-discover / manual-import of arbitrary coin types.

---

## 1. Context

Tron, BTC and Solana are built and merged as the reference non-EVM verticals. Sui is the next chain.
It slots into the exact same architecture: the app derives keys + signs on-device with `@noble`; the
Go backend builds unsigned txs, reads balances, and submits. The `chains.ChainClient` interface,
`/wallet/nonevm/*` routes, `WalletAddress`/`WalletActivity` models, and the non-EVM reconciler are all
chain-agnostic and need no structural change.

Sui is the **closest analog to Solana** — both use **ed25519**, so the app's existing SLIP-0010
derivation (`ed25519Slip10`) and `signEd25519Message` reuse almost verbatim. The deltas from Solana:

- Different coin type / path: **`m/44'/784'/0'/0'/0'`** (fully hardened), vs Solana `m/44'/501'/0'/0'`.
- Address is **`0x || hex(blake2b256(0x00 || pubkey))`** (66 chars), vs Solana's base58 raw pubkey.
- What gets signed is a **32-byte digest** — `blake2b256([0,0,0] || BCS(TransactionData))` — so the
  `SigningPayload.HashHex` field carries a real digest (its documented purpose), unlike Solana which
  put the whole message there.
- The RPC signature is **`base64(0x00 || sig64 || pubkey32)`** = 97 bytes. ed25519 gives no key
  recovery, so **the app must hand the backend its pubkey** (packed into `sigHex`) to assemble it.
- Sui's object/coin model: a token balance is the **sum of many `Coin<T>` objects**; sends require
  **coin selection**. Gas is paid from `Coin<SUI>` objects.

### 1.1 Ground-truth interop vectors (verified against `@mysten/sui` v1.45.2)

Generated from the official SDK and cross-checked with independent `@noble` primitives. These are the
fixtures the app + backend tests assert against.

Mnemonic: `test test test test test test test test test test test junk`

| Path | Address | PublicKey (hex) |
|---|---|---|
| `m/44'/784'/0'/0'/0'` | `0xc88ef07b9b8b2fc3b7daad9478f4e1337f01792e2eab9c3794494e610636026e` | `1e6b2604ae257d45be4fb5bb3e9179936c6840552886895fe4434182228d610b` |
| `m/44'/784'/1'/0'/0'` | `0x31e57c79ce7793434b5f607d19832d74821bbeca30596b99eb27050c36b543a7` | `1bf38f0b7fc49372e59816ddeb0703b489409b55fd17b448954d8ab81e629951` |

Sign vector (account 0):
- `txBytes` (hex, 60 bytes): `deadbeef0011...ccddeeff` (`deadbeef00112233445566778899aabbccddeeff` × 3)
- signing digest = `blake2b256([0,0,0] || txBytes)` = `74202195b64a7fec6f90c304ec5ea2996641d0eb7877d3a98defefa47229a9f9`
- raw ed25519 sig (64 bytes) = `46762fd90ded89381877837159493c411e8f7ff902b7d40a291ead95001474a0fcbc4cbdaae58f148d5de01536ef88894485c5044ab8dc2a62dc5faa68956306`
- serialized RPC signature (97 bytes, base64) = `AEZ2L9kN7Yk4GHeDcVlJPEEej3/5ArfUCikerZUAFHSg/LxMvarljxSNXeAVNu+IiUSFxQRKuNwqYtxfqmiVYwYeayYEriV9Rb5Ptbs+kXmTbGhAVSiGiV/kQ0GCIo1hCw==`
- address confirmed = `0x || hex(blake2b256(0x00 || pubkey))`.

## 2. Decisions locked in

| Decision | Choice | Rationale |
|---|---|---|
| RPC provider | **Public fullnode** (`https://fullnode.mainnet.sui.io:443` / `…testnet…`), dormant until an env URL is set | Zero setup for the first cut; swap to a keyed provider before real launch. |
| Deposit detection | **Reconcile-only** | Same as the other non-EVM chains. |
| Tokens | **Auto-discover + manual import** | `suix_getAllBalances` lists every held coin type; users can also paste a coin type to import one. Reuses the existing custom-token flow. |
| Signing split | App signs (ed25519), backend builds txs | Unchanged. App holds only derivation + `@noble` ed25519 signing; all tx construction in Go. |
| Server-side tx build | **`unsafe_paySui` / `unsafe_pay` fullnode RPC** returns BCS `TransactionBytes` | Matches the build-server/sign-client/submit-server flow with no local BCS builder. `unsafe_*` is flagged for eventual removal in RPC 2.0 (Mysten #13700) but works on current mainnet/testnet — documented as the migrate-to-local-PTB follow-up. |
| RPC client | **Hand-rolled JSON-RPC over `net/http`** (like Tron's `trongrid.go`) | No maintained official Go SDK; a thin client avoids a pre-1.0 third-party dependency for money movement, consistent with the Tron precedent. |
| Gas sizing | **`sui_dryRunTransactionBlock`** to size the budget + `suix_getReferenceGasPrice` for price | Never hardcode; dry-run then add headroom. |

### 2.1 Derivation

| Chain | Path | Curve | Address |
|---|---|---|---|
| Sui | `m/44'/784'/0'/0'/0'` | ed25519 (SLIP-0010, all-hardened) | `0x` + hex(blake2b256(`0x00` ‖ pubkey)) |

Reuses `ed25519Slip10` (already used for Solana) with the Sui path. Verified against the SDK (§1.1).

## 3. Goals / Non-goals

**Goals**
- Receive (address + QR), balances (SUI + **all** held coin types), send (SUI + tokens), history.
- Fee preflight that blocks a no-SUI send with a clear message (gas is always paid in SUI).
- Coin selection: a send of amount N picks enough `Coin<T>` objects (and lets the node merge/split).
- Auto-discovery of held coins + manual coin-type import, reusing the existing custom-token system.
- Dormant-by-default: absent RPC config → chain not registered → routes return "chain unavailable".

**Non-goals (this cycle):** push deposits / websockets; swap/bridge; sponsored (gasless) txs; multisig
/ zkLogin; secp256k1/r1 keys (ed25519 only); regulated-coin deny-list UX beyond surfacing the error;
local PTB builder (deferred until `unsafe_*` is removed).

## 4. Architecture

### 4.1 Backend — `internal/chains/sui/` (new package)

Implements `chains.ChainClient`, mirroring `solana/`. Registered per (network, chainKey) so mainnet +
testnet can be live at once.

- **`address.go`** — `ValidateAddress`: `0x` + exactly 64 lowercase hex chars (32 bytes). Normalizes
  short/long forms to the 32-byte canonical form for comparison. `AddressFromPubkey(pub)` =
  `0x`+hex(blake2b256(`0x00`‖pub)) — used by tests / signature assembly sanity.
- **`rpc.go`** — JSON-RPC client over the configured fullnode URL. Methods used:
  `suix_getBalance`, `suix_getAllBalances`, `suix_getCoins`, `suix_getReferenceGasPrice`,
  `unsafe_paySui`, `unsafe_pay`, `sui_dryRunTransactionBlock`, `sui_executeTransactionBlock`,
  `sui_getTransactionBlock`, `suix_queryTransactionBlocks`.
- **`client.go`** — the `ChainClient`:
  - `Family() → "sui"`
  - `GetBalances` — `suix_getAllBalances` → one `Balance` per coin type (native SUI, 9 decimals,
    `isNative`; tokens keyed by coin type, decimals from `suix_getCoinMetadata` or the config
    registry). Configured tokens always shown (even at 0); auto-discovered coins shown when held.
  - `EstimateFee` — dry-run the built tx (or a reference-gas-price estimate when a dry-run isn't
    possible) → `computation + storage − rebate`. `GasSymbol:"SUI"`, `GasDecimals:9`. `HasEnoughGas`
    compares the sender's SUI balance to `budget + (amount if native)`. `Note` on shortfall.
  - `PrepareTransfer` — validate recipient; `suix_getCoins` to select input coin object(s); build the
    tx via `unsafe_paySui` (native) / `unsafe_pay` (token, gas paid from separate SUI coins); get the
    BCS `TransactionBytes`. Compute the signing digest `blake2b256([0,0,0] || txBytes)`. Return
    `PreparedTx{ Family:"sui", Raw:<base64 txBytes>, Payloads:[{Index:0, HashHex:<digest hex>,
    Curve: ed25519}] }`. **HashHex is the real 32-byte digest** (contrast Solana).
  - `AssembleAndBroadcast` — `sigs[0].SigHex = hex(pubkey32 ‖ sig64)` (96 bytes). Split it, verify the
    pubkey hashes to the sender address (defensive), assemble `base64(0x00 ‖ sig ‖ pubkey)`, and call
    `sui_executeTransactionBlock(base64 txBytes, [serializedSig], {showEffects:true},
    WaitForEffectsCert)`. Returns the tx digest.
  - `GetTxStatus` — `sui_getTransactionBlock(digest, {showEffects:true})`; `effects.status.status ==
    "success"` → `Success`; presence → `Confirmed` (Sui has instant finality once effects are certed).
  - `History` — `suix_queryTransactionBlocks` with `FromAddress` and `ToAddress` filters, merged +
    sorted by timestamp (bounded window, cursor deferred like the others).

### 4.2 Backend — config + wiring

- `config.go`: `addSui(network, rpcURL, usdc)` mirroring `addSolana`. Env `SUI_MAINNET_RPC_URL` /
  `SUI_TESTNET_RPC_URL` (+ back-compat `SUI_RPC_URL` + `SUI_NETWORK`). `NativeSymbol:"SUI"`,
  `NativeDecimals:9`, `Tokens` seeded from `SUI_MAINNET_USDC_TYPE` etc. Unset URL → not added → dormant.
- `main.go`: add `case "sui":` to the `cfg.NonEVMChains` registry loop.
- `jobs/nonevm_reconciler.go`: chain-agnostic; Sui needs no branch (walks all registered addresses via
  the registry + `GetTxStatus`). Confirm during execution.

### 4.3 App — signing + send + wiring

- `derivation.ts`: add `sui: "m/44'/784'/0'/0'/0'"` to `DERIVATION_PATHS`; add `deriveSuiAddress`
  (blake2b256(0x00‖pubkey)), `deriveSuiPrivateKey` (the 32-byte ed25519 seed hex), `deriveSuiPublicKey`
  (32-byte pubkey hex — needed so the app can hand the backend the key), and `isValidSuiAddress`.
- `sign.ts`: reuse `signEd25519Message` — ed25519 signs the 32-byte digest carried in `hashHex` (the
  digest is the "message"; RFC-8032 EdDSA over 32 bytes). No new signer needed.
- `services/chains/suiWallet.ts`: `getSuiAddress` / `getSuiBalances` / `loadSuiWallet`, mirroring
  `solanaWallet.ts`.
- `services/chains/suiSend.ts`: `prepareSui` / `broadcastSui` / `sendSui`, mirroring `solanaSend.ts`.
  `broadcastSui` signs the digest AND packs `sigHex = pubkeyHex ‖ sigHex` so the backend can build the
  97-byte serialized signature.
- `constants/chains.ts`: add `'sui'` to the `family`/`NonEVMChainKey` unions, `NONEVM_CHAINS`,
  `NONEVM_CHAIN_KEYS`, and `TOKEN_RANK`; explorer `https://suiscan.xyz` (with `?network=testnet`).
- Screen wiring (`home.tsx`, `send.tsx`, `receive.tsx`): add Sui to the `Promise.all` loaders, the
  `isSui` family branch, address validation, params builder, and the review/broadcast branches —
  mirroring the Solana additions.

### 4.4 "Show / import any Sui token"

Reuses the existing custom-token system:
- **Auto-discover:** `GetBalances` returns every held coin type.
- **Manual import:** the import-token path accepts a **Sui coin type** (`pkg::module::STRUCT`) when the
  active chain is Sui; the backend resolves decimals/symbol via `suix_getCoinMetadata`; stored keyed
  `sui:<coinType>`. Invalid / unknown coin types rejected with a clear message. (Import UI extension is
  a fast-follow; auto-discovery covers the common case in this cut.)

## 5. The four correctness invariants (must not break)

1. **Derivation path** `m/44'/784'/0'/0'/0'`, all segments hardened (SLIP-0010 ed25519).
2. **Address** = `0x` + hex(blake2b256(`0x00` ‖ pubkey)).
3. **Signing digest** = `blake2b256([0,0,0] ‖ BCS(TransactionData))` — sign the intent-prefixed digest,
   never the raw tx bytes.
4. **RPC signature** = `base64(0x00 ‖ sig64 ‖ pubkey32)` = 97 bytes.

All four are asserted by interop tests against the §1.1 SDK vectors, on both the app and backend sides.

## 6. Error handling & edge cases

- **Dormant chain:** no RPC URL → not registered → "chain unavailable"; server still boots.
- **No-SUI send:** blocked at `EstimateFee` — gas is always SUI, so a token send with zero SUI fails
  preflight with "you need SUI for the network fee", never a failed submit.
- **Coin fragmentation:** amount exceeds the largest single `Coin<T>` → select multiple objects; the
  `unsafe_pay` path merges them. If total held < amount+gas → clear "insufficient balance".
- **Signer/sender binding:** enforced by the handler's user-ownership check on `from` plus the node
  rejecting a tx whose sender ≠ the signing pubkey (see §8a C2); `AssembleAndBroadcast` additionally
  re-verifies the signature over the exact broadcast bytes so a mismatched key/sig fails fast.
- **Regulated USDC (deny-list):** a blocked transfer surfaces the node's execution error verbatim.
- **Gas underestimation:** dry-run sizes the budget with headroom; still-failed execution surfaces the
  effects error.

## 7. Testing

- **Interop vectors (app + backend):** mnemonic → address + pubkey match the SDK (§1.1); digest
  construction `blake2b256([0,0,0]‖txBytes)` matches; the assembled 97-byte `base64(flag‖sig‖pk)`
  matches the SDK's serialized signature byte-for-byte.
- **App unit tests:** `deriveSuiAddress` / `deriveSuiPublicKey` / `deriveSuiPrivateKey` /
  `isValidSuiAddress` against vectors; `broadcastSui` packs `pubkey‖sig` and posts correctly (mocked
  fetch), mirroring `solanaSend.test.ts`.
- **Backend unit tests (no live RPC):** address validate/normalize; digest builder; signature assemble
  (split `pubkey‖sig` → `base64(flag‖sig‖pk)`, and the address-mismatch rejection); fee-quote logic;
  balance/coin-type symbol naming.
- **Dormant-mode test:** no RPC config → clean "chain unavailable", server boots.
- **Testnet E2E (manual/documented):** derive → receive → faucet → balance → send SUI + a token →
  confirm → history.

## 8. Rollout order

1. App: `deriveSui*` + interop/signature vectors green (foundation, fully vector-locked).
2. Backend: `sui/` package — address, rpc, client (+ unit tests, digest + signature-assembly vectors).
3. Backend: config `addSui` + `main.go` registration; confirm reconciler needs no branch.
4. App: `suiWallet.ts` + `suiSend.ts` + `constants/chains.ts` wiring (NONEVM_CHAINS / KEYS / RANK).
5. App: screen wiring (home / send / receive).
6. Self code-review; build + test both projects green.
7. Testnet E2E pass (user); document the keyed-RPC switch + the `unsafe_*` → local-PTB follow-up.

## 8a. Independent review resolutions (2026-07-14)

An independent reviewer verified the four §5 invariants byte-for-byte against the `@mysten/sui`
vectors (including that the app's existing `signEd25519Message` on the 32-byte digest reproduces the
SDK signature exactly). Adopted findings:

- **C1 — `Raw` is base64, not hex.** Sui's `PreparedTx.Raw` is base64 `TransactionData`, decoded with
  `base64.StdEncoding` (not Solana's hex path), with its own size bound (`maxPreparedRawBytes`). The
  Solana `maxPreparedRawHex`/hex-decode must NOT be copied.
- **C2 — server-side signature re-verification.** `AssembleAndBroadcast` re-derives the digest
  `blake2b256([0,0,0] ‖ base64decode(Raw))` and verifies the supplied signature against it AND the
  supplied pubkey with Go `ed25519.Verify` **before** broadcast. Since `prepared.Raw` is client-
  supplied (there is no server-stored prepared tx), this proves *mutual consistency* of signature ↔
  pubkey ↔ broadcast-bytes (a malformed/mismatched signature fails fast, saving a wasted broadcast) —
  it does **not** by itself prove the tx only spends the sender's funds. That authority is enforced by
  (a) the handler checking `from` is a wallet address owned by the authenticated user, and (b) the node
  rejecting any tx whose sender ≠ the signing pubkey. Together (a)+(b) bind signer to sender.
- **S1 — signature packing pinned.** `sigHex = hex(pubkey32 ‖ sig64)`, exactly 96 bytes;
  `AssembleAndBroadcast` asserts the length, then builds `base64(0x00 ‖ sig ‖ pubkey)`. Order is
  asserted by an interop test. (A pubkey→sender match inside `AssembleAndBroadcast` is intentionally
  NOT added: the `ChainClient` interface passes no sender there, and the signer↔sender binding is
  already covered by C2(a)+(b). `AddressFromPubkey` remains as the derivation rule, exercised by tests.)
- **S2/S3 — no hand-wavy fee fallback.** No dry-run dependency for the MVP: a concrete conservative
  `defaultGasBudgetMist` (0.01 SUI) is used as both the preflight estimate and the `unsafe_*` gas
  budget (a budget is a cap, not a charge — only actual gas is spent). Native preflight requires
  `balance ≥ amount + budget`, so send-max on a single coin fails preflight with a clear message, never
  at broadcast. Dry-run-based sizing is a documented follow-up.
- **S4 — status.** `GetTxStatus` sets `Confirmed` on the tx being present with certified effects, and
  `Success` independently from `effects.status.status == "success"`, so a landed-but-failed tx is
  `Confirmed=true, Success=false` (the reconciler writes `failed`, not `confirmed`).
- **N1 — address validation (deliberate deviation).** Both app and backend require the **full form**
  `0x` + exactly 64 hex (case-insensitive, normalized lowercase). This is stricter than the SDK's
  short-form `normalizeSuiAddress`, chosen deliberately: for a payment target, rejecting a truncated
  paste is safer than silently left-padding it to a different valid address. App `isValidSuiAddress`
  and backend `ValidateAddress` use the identical rule (parity).
- **N4 — untrusted coin metadata.** Auto-discovered coins with unresolvable decimals are dropped (a
  10^n-inflated balance on a financial screen is worse than hiding the row), mirroring Solana's `emit`
  guard. Symbols for unregistered coin types are treated as untrusted/spoofable.
- **N2 — owner scoping** is inherited from the generic `/wallet/nonevm/*` handlers (WalletAddress /
  WalletActivity are `user_id`-scoped); Sui adds no handler code, so the recent hardening is preserved.

## 9. Known follow-ups (documented, not blocking)

- `unsafe_*` RPC is deprecated for eventual removal (RPC 2.0). Migrate tx-build to a local BCS/PTB
  builder (`pattonkan/sui-go` or hand-rolled) when it lands. The `ChainClient` boundary makes this a
  `rpc.go`/`client.go`-local change.
- Manual coin-type import UI (auto-discovery ships this cut).
- Coin object consolidation (merge dust) — not needed while `unsafe_pay` merges per-send.
