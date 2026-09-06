# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Sibling repos (admin console, landing site, MCGP OTC contracts) and the shared `docs/` are mapped
in `../CLAUDE.md`.

## Monorepo layout

Three independent projects that together make up **TSA Connect**, a marketplace + multi-chain
wallet + P2P crypto-trading platform:

| Dir | Stack | Role | Deep docs |
|-----|-------|------|-----------|
| `tsa-api-go/` | Go 1.24 + Gin + GORM/PostgreSQL | REST + WebSocket backend, blockchain orchestration | `tsa-api-go/CLAUDE.md` |
| `tsa-app/` | React Native 0.81 + Expo 54 + expo-router v6 | Mobile app (User / Merchant / Admin roles) | `tsa-app/CLAUDE.md` |
| `smart-contracts/` | Solidity 0.8.24 + Hardhat + OpenZeppelin (UUPS) | Escrow contracts on Sonic / BSC | — |

There is no top-level build, package manager, or workspace tooling — each project is built and
run on its own. **Read the per-project `CLAUDE.md` for commands, file layout, and conventions
specific to that project.** This file covers only the cross-cutting picture.

## How the three projects connect

The app calls the Go API over HTTPS/WebSocket; the Go API is the only thing that talks to the
smart contracts (RPC reads + building unsigned txs). Contracts are deployed once to Sonic/BSC;
the API's config hardcodes the deployed proxy addresses per network.

The **P2P trading feature** is the clearest example of all three working together — read
`docs/p2p-smoke-test.md` for the full end-to-end flow. In short:

- **`P2PEscrow.sol`** is a pool-based escrow. A seller `deposit()`s once into a per-(seller,token)
  `available` balance, then publishes unlimited ads *off-chain* by signing **EIP-712 `AdVoucher`s**
  (no gas, instant, editable). Funds move `available → locked` only when a buyer `openTrade()`s.
  Lifecycle: `openTrade → markPaid → release` (happy path); failure paths are `cancelByTimeout`,
  `raiseDispute`, `adminResolve`.
- **The Go API** stores ads/trades/messages/disputes in PostgreSQL (`p2p_*` models/tables),
  verifies voucher signatures, builds the unsigned on-chain txs, and emits chat/system messages.
- **The app** signs vouchers and txs client-side, shows the marketplace/trade/chat/admin UIs.

### Cross-cutting invariants (do not break these)

- **Signed-but-not-broadcast pattern.** The API *prepares* unsigned txs; the app signs +
  broadcasts; the API *confirms*. If a confirm is dropped, the **chain reconciler** (30s ticker,
  `tsa-api-go/internal/jobs/chain_reconciler.go`) walks non-terminal trades and corrects DB drift
  against on-chain truth. The **timeout watchdog** (1m ticker) handles expired `pending_payment`
  trades. Both are registered in `cmd/server/main.go`.
- **EIP-712 domain chainId must match the chain the RPC actually reports.** `main.go` probes
  `eth_chainId` at boot and uses that for the voucher domain, *not* the configured `SONIC_CHAIN_ID`
  — a mismatch makes every `ecrecover` resolve to a wrong address and rejects all signatures with
  "recovered 0x… expected 0x…". When touching P2P signing, preserve this.
- **Bitcoin swap deposits are memo-or-nothing.** A LiFi Bitcoin route is a *deposit*: the quote
  names an address, the exact sats, and (THORChain-style bridges) an OP_RETURN memo that is the
  only link between that payment and the user's swap. `internal/chains/btc` attaches the memo as a
  zero-value nulldata output and prices it into the fee; families that cannot carry one reject a
  non-empty `TransferRequest.MemoHex` instead of dropping it. The app
  (`tsa-app/services/chains/btcLifi.ts`) refuses to broadcast when the route's amount differs from
  the quoted one, or when its memo is not encodable — a wrong or memo-less deposit to a bridge is
  unrecoverable. LiFi's exact UTXO `transactionRequest` shape is verified by
  `tsa-api-go/scripts/lifi-btc-probe.sh`; run it from a host that can reach li.quest before
  enabling Bitcoin swaps in a new environment.
- **Money invariant.** Sum of `token_amount` over active `p2p_trades` must equal on-chain
  `P2PEscrow.balanceOf(token)` minus the live ad-pool `remaining`. Drift is logged as
  `p2p_reconciler_drift_total`.

## Network / chain configuration (`tsa-api-go/internal/config/config.go`)

This is the trickiest cross-cutting concern. The Sonic stack flips between **mainnet** and
**testnet** as a single unit via `SONIC_NETWORK` (default `mainnet`). The `sonicPresets` map bundles
RPC URL, chainID, token addresses, and the P2PEscrow proxy address so they can never drift apart
piecemeal — flipping them individually breaks the EIP-712 domain (see invariant above). Individual
fields are still overridable via legacy env vars (`SONIC_RPC_URL`, `*_TOKEN_ADDRESS`,
`P2P_ESCROW_ADDRESS`) as a hot-fix escape hatch.

`Config.Networks` holds a full `mainnet` and `testnet` `NetworkConfig` (chains + `"chain:symbol"`
→ address maps) for the wallet's multi-chain support (Sonic, BSC, Ethereum, Polygon, Arbitrum,
Base, Optimism, Avalanche, Linea). Token addresses are keyed `"chain:symbol"` (e.g. `"sonic:USDT"`).

Several integrations are **dormant-by-default**: if their credentials are absent the service boots
and the relevant routes return a clear error (Nomba bank directory, P2P trade routes when
`P2P_ESCROW_ADDRESS` is unset, Cloudinary, LiFi, Didit KYC). Don't add hard failures for missing
optional credentials — keep dev environments bootable.

## Commands cheat-sheet

```bash
# Backend (run from tsa-api-go/)
go run ./cmd/server              # run, default port 5000 (or: make run)
go test ./... -v -count=1        # all tests (or: make test)
go test ./internal/jobs -v       # single package

# App (run from tsa-app/)
npx expo start                   # dev server
npm test                         # jest
npm run lint

# Contracts (run from smart-contracts/)
npx hardhat compile
npx hardhat test                 # all
npx hardhat test test/P2PEscrow.test.js
npx hardhat run scripts/deploy-p2p-escrow.js --network sonicTestnet   # CHECK_ONLY=1 for dry-run
```

## Backend subsystems added since `tsa-api-go/CLAUDE.md` was written

That doc predates several packages — when working in the backend, also note:

- `internal/events/` — in-process event bus (`bus.go`); decouples handlers from notifications/WS.
- `internal/ws/` — WebSocket hub + client; pushes trade/chat/notification events to the app.
- `internal/jobs/` — periodic-worker scheduler (`scheduler.go`) + the P2P timeout watchdog and
  chain reconciler. Phase 1 is in-process; the scheduler doc notes the Phase 2 path to River.
- `internal/services/` — now includes the P2P stack (`p2p_service.go` exchange-rate hybrid of Open
  Exchange Rates + Bybit P2P; `p2p_escrow_service.go`, `p2p_trade_service.go`, `p2p_message_service.go`),
  `escrow_service.go`, `service_contact_service.go`, `otc_service.go`, `nomba_client.go`,
  `email_service.go`, `notification_service.go`.
- Many more handlers (KYC, deposits, orders, exchange, swap + LiFi, private sale, notifications,
  service-contact). Route groups live in `internal/routes/routes.go`.
