# Add Robinhood Chain to the multi-chain wallet

**Date:** 2026-07-11
**Branch:** `feat/robinhood-chain`
**Status:** Implemented — pending review

## Goal

Add **Robinhood Chain** as a supported EVM network in the wallet, so users see
native **ETH** balances (with USD value) and the chain's canonical stablecoin
**USDG** on both mainnet and testnet, exactly like any other wallet chain
(Base, Arbitrum, …).

## What Robinhood Chain is

A permissionless, Ethereum-compatible **Arbitrum-Orbit L2** for onchain finance
/ real-world assets. Native gas token is **ETH** (not a bespoke token). Its
canonical stablecoin is **USDG** (Paxos Global Dollar) — bridged USDC/USDT
arrive on Robinhood Chain as USDG, so USDG (not USDT/USDC) is the correct
stablecoin to wire here.

### Verified network parameters

All values confirmed against the live public RPCs on 2026-07-11
(`eth_chainId`, and `decimals()`/`symbol()` for USDG).

| | Mainnet | Testnet |
|---|---|---|
| Chain ID | **4663** (`0x1237`, "HOOD" on a keypad) | **46630** (`0xb626`) |
| Public RPC | `https://rpc.mainnet.chain.robinhood.com` | `https://rpc.testnet.chain.robinhood.com` |
| Explorer | `https://robinhoodchain.blockscout.com` | `https://explorer.testnet.chain.robinhood.com` |
| Native | ETH (18 dec) | ETH (18 dec) |

### Verified token contracts (mainnet)

| Token | Address | Decimals | Notes |
|---|---|---|---|
| USDG (Global Dollar) | `0x5fc5360D0400a0Fd4f2af552ADD042D716F1d168` | **6** (on-chain confirmed) | canonical stablecoin |
| WETH | `0x0Bd7D308f8E1639FAb988df18A8011f41EAcAD73` | 18 | not wired (native ETH is shown instead) |

No verified **testnet** USDG address is published, so testnet stablecoin support
is left env-gated (off by default), consistent with how the other non-Sonic
chains handle testnet stablecoins.

## Scope / design decisions

- **Wallet-only chain**, like Base/Arbitrum — NOT a P2P chain. Robinhood Chain
  has no P2PEscrow / FeeManager deployment, so it is intentionally left out of
  `Config.P2PChains` and `Config.FeeManagerChains`. No contract work.
- **Native ETH** works automatically: it's already in the CoinGecko price map,
  and the balance path prices a chain's `NativeCurrency` symbol.
- **USDG** is added as a first-class stablecoin: config token address, a
  CoinGecko id (`global-dollar`) for USD pricing, a `SupportedToken` seed row,
  and an app `DEFAULT_TOKENS` entry. USDG decimals are read on-chain at balance
  time (the handler queries `decimals()`); the seeded `6` is only a fallback.
- **YAGNI:** Robinhood's tokenized stock/ETF tokens (AAPL, TSLA, SPY, …) are
  out of scope — that's a separate product surface, not "add a chain".
- **Public RPC defaults**, overridable via env — matches every other chain. The
  docs note public endpoints are rate-limited; production should point
  `ROBINHOOD_MAINNET_RPC_URL` at an Alchemy/QuickNode key.

## Changes

### Backend (`tsa-api-go`)

`internal/config/config.go`
- `mainnetChains["robinhood"]` → RPC `rpc.mainnet.chain.robinhood.com`, chainID
  `4663`, native `ETH`; env overrides `ROBINHOOD_MAINNET_RPC_URL` /
  `ROBINHOOD_MAINNET_CHAIN_ID`.
- `testnetChains["robinhood"]` → RPC `rpc.testnet.chain.robinhood.com`, chainID
  `46630`, native `ETH`; env overrides `ROBINHOOD_TESTNET_*`.
- `mainnetTokens["robinhood:USDG"]` → USDG address, env override
  `ROBINHOOD_MAINNET_USDG_ADDRESS`.
- `testnetTokens` — `robinhood:USDG` env-gated via `ROBINHOOD_TESTNET_USDG_ADDRESS`
  (off by default).

`internal/services/price_service.go`
- `coinIDs["USDG"] = "global-dollar"` so USDG shows a USD value.

`internal/handlers/wallet_handler.go`
- `seedDefaultTokens()` gains `{"USDG", "Global Dollar", 6, <color>, ["robinhood"]}`.

### App (`tsa-app`)

`constants/chains.ts`
- `ChainKey` union += `'robinhood'` (the compiler then forces the entries below).
- `MAINNET_CHAINS.robinhood` and `TESTNET_CHAINS.robinhood`.
- `NETWORK_DISPLAY_NAMES.robinhood = 'Robinhood'`.
- `CHAIN_KEYS += 'robinhood'`.
- `DEFAULT_TOKENS`: new `USDG` entry (`chains: ['robinhood']`); add `'robinhood'`
  to the existing `ETH` entry's `chains` so ETH lists Robinhood among its L2s.

## Operational note (live DB)

`seedDefaultTokens()` only runs against an **empty** `supported_tokens` table.
On the already-seeded production DB, add the USDG row via the normal admin/DB
path so USDG surfaces there too:

```
symbol=USDG, name="Global Dollar", decimals=6, chains=["robinhood"], is_active=true
```

Native ETH needs no DB row — it surfaces automatically.

## Verification

- `go build ./...` in `tsa-api-go` (compiles with the new config/handler).
- `go test ./internal/handlers ./internal/config` (config + wallet handler).
- App `tsc --noEmit` (the `ChainKey` union change is compiler-checked to be
  exhaustive across all three `Record<ChainKey, …>` maps).
- Live RPC probes recorded above (chainId, USDG decimals/symbol).
