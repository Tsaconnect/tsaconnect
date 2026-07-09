# AGENTS.md

Three independent projects, no shared build:

| Project | Stack | Entrypoint | Per-project doc |
|---------|-------|-----------|-----------------|
| `tsa-api-go/` | Go 1.24 + Gin + GORM/PostgreSQL | `cmd/server/main.go` | `tsa-api-go/CLAUDE.md` |
| `tsa-app/` | React Native 0.81 + Expo 54 + expo-router v6 | `app/index.tsx` | `tsa-app/CLAUDE.md` |
| `smart-contracts/` | Solidity 0.8.24 + Hardhat + OpenZeppelin (UUPS) | `contracts/` | — |

## Commands

```bash
# Backend (from tsa-api-go/)
go run ./cmd/server              # run (default port 5000, or: make run)
go test ./... -v -count=1        # -count=1 disables test caching
go test ./internal/handlers -v   # single package
go vet ./...                     # always after edits

# App (from tsa-app/)
npx expo start                   # dev server (regenerates typed routes)
npm test                         # jest --watchAll
npm run lint                     # expo lint (ESLint)

# Contracts (from smart-contracts/)
npx hardhat compile
npx hardhat test test/P2PEscrow.test.js
npx hardhat run scripts/deploy-p2p-escrow.js --network sonicTestnet  # CHECK_ONLY=1 dry-run
```

## Backend gotchas

- **EIP-712 chainId** must equal what the RPC actually reports, *not* the configured `SONIC_CHAIN_ID`. `main.go` probes `eth_chainId` at boot. See `cmd/server/main.go`.
- **`SONIC_NETWORK`** (`mainnet`/`testnet`, default `mainnet`) flips the whole Sonic stack (RPC, chainID, token addresses, P2PEscrow proxy) via `sonicPresets` in `internal/config/config.go`. Legacy env vars override individual fields as hot-fix escape hatch.
- **P2P routes live only when `P2P_ESCROW_ADDRESS` is set** (checked via `IsConfigured()`). Every P2P endpoint also requires JWT + Persona-verified KYC.
- **Dormant-by-default pattern**: Nomba, Cloudinary, LiFi, Persona KYC, IT, MCGP swap — if credentials absent, service boots and routes return clear errors. Don't add hard failures.
- **Tx flow**: API *prepares* unsigned txs → app signs + broadcasts → API *confirms*. Background reconciler (30s ticker in `internal/jobs/chain_reconciler.go`) corrects DB drift. Timeout watchdog (1m ticker in `internal/jobs/timeout_watchdog.go`) handles expired `pending_payment`.
- **P2P model `TableName()`**: GORM defaults pluralize `p2p_*` to `p2_p_*`. Every P2P model MUST pin `TableName()` to `p2p_*`.
- **SQLite test rigs** use raw DDL (not `AutoMigrate`, which rejects `gen_random_uuid()` defaults) and `SetMaxOpenConns(1)`.
- **Handler structure**: `WalletHandler` has its own struct with `DB`, `Config`, `BlockchainService`. Other handlers use `*Handlers` struct (central in `internal/handlers/handlers.go`).
- **Response format**: `{"success": bool, "message": string, "data": object}` or `{"success": false, "errors": array}`.
- **Air live-reload** is configured via `.air.toml` in `tsa-api-go/`.
- **WebSocket** at `/ws/notifications?token=<jwt>` — JWT passed as query param (not header). Hub in `internal/ws/`.
- **Event bus** (`internal/events/bus.go`) decouples handlers from notifications/WS.
- **Fee model v2** is gated on `FEE_MODEL_V2_ENABLED` (off by default). When off, fee/TP engine stays nil in handlers.
- **P2P chain reconciler** resolves escrow per-chain via `P2PEscrowRegistry` (Sonic + optional BSC).

## App gotchas

- **API base URL**: `https://tsa.mcgpchain.com/api` (hardcoded in `constants/api/config.ts`).
- **Navigation**: Route groups `()` are transparent. Navigate with simplified paths: `` router.push(`/product/${id}`) `` not the full group path. Dynamic routes need a `<Stack.Screen>` in the parent `_layout.tsx`.
- **Typed routes** live at `.expo/types/router.d.ts` — regenerate with `npx expo start` if routes seem missing.
- **API responses use `id`** (UUID string from Go/GORM), not `_id`. Screens historically used `_id` (from an old mongo-era frontend); always prefer `id`.
- **Product data shape**: `category` is a UUID string, `images` may lack `url`, `rating` can be null, `attributes` optional.
- **Currency**: App uses `$` (USD), not `₦`.
- **State**: plain Context API + AsyncStorage. No Redux/Zustand.
- **`AuthContext/AuthContext.tsx`** — primary: auth state, user, tokens. **`AuthContext/AppContext.tsx`** — cart, categories, app state.
- **Path alias**: `@/*` maps to project root (`tsconfig.json`).
- **Expo SDK 54** — no separate build step; `.expo/` is gitignored.
- **Jest** runs in `--watchAll` mode by default. Run `npx jest path/to/test.ts` for a single file.

## Contracts gotchas

- **Cancun EVM**: Hardhat config uses `evmVersion: "cancun"` (Sonic supports Cancun on both mainnet and testnet).
- **Deploy system**: `deploy/all.js` reuses contracts already recorded in `deployments/<network>.json`. `FORCE_REDEPLOY=1` to overwrite. Running a single `deploy/<contract>.js` always deploys fresh.
- **Upgrades**: `npx hardhat run deploy/upgrade.js --network sonicMainnet`. The old `scripts/upgrade-p2p-escrow-v3.js` is deleted — its `initializeV3` re-initializer would revert if run again.
- **p2p-smoke-test.md** (`docs/p2p-smoke-test.md`) is the integration test reference for the P2P escrow contract — read it when touching contract interactions.

## References

- `docs/p2p-smoke-test.md` — full end-to-end flow for P2P trading
- `docs/p2p/STATUS.md` — P2P gotchas learned the hard way (GORM naming, event dedup, typed routes)
- `docs/p2p/architecture.md` — P2P design and roadmap
- `.remember/now.md` — current session state/context (used by the developer for handover notes)

## Git

- Current branch: `main` (P2P trading feature developed on `feat/p2p-trading`)
- Do not commit unless explicitly asked
