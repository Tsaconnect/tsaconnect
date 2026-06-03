# P2P Multi-Chain — Plan 6: BSC Deploy Hand-off (operator runbook)

> **This plan is run by the owner, not the agent** — it deploys a contract (needs funded keys + the key-custody decision §10.6). The backend (Plans 1–5) is built so BSC stays **dormant** until you complete the steps below. Nothing here is gated on the agent; nothing the agent shipped depends on this to keep Sonic working.

**Goal:** Deploy `P2PEscrowV2` to BSC (testnet first), point the backend at it, and verify it comes up — at which point BSC P2P (USDT/USDC) goes live alongside Sonic.

---

## 0. Prerequisites (your decisions/assets)
- **Key custody (§10.6):** decide OWNER / ADMIN / SYSTEM_WALLET for the BSC escrow (same multisig as Sonic, or chain-specific). These go into the deploy env below.
- **Funded BSC deployer:** the EOA running the deploy needs **tBNB** (testnet) / **BNB** (mainnet) for gas. Set its key as `PRIVATE_KEY` (or whatever `hardhat.config.js` expects) in `smart-contracts/.env`.
- **BSC token addresses:**
  - Mainnet (known): USDT `0x55d398326f99059fF775485246999027B3197955`, USDC `0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d`.
  - **Testnet:** pick/deploy BSC-testnet USDT/USDC test tokens (faucet tokens or your own mock ERC-20s) and note their addresses — there's no canonical pair.
- `BSCSCAN_API_KEY` in `.env` if you want source verification.

---

## 1. Dry-run (no txs)
```bash
cd smart-contracts
OWNER_ADDRESS=<owner> SYSTEM_WALLET_ADDRESS=<systemWallet> CHECK_ONLY=1 \
  npx hardhat run scripts/deploy-p2p-escrow-v2.js --network bscTestnet
```
Confirms compilation + the proxy deploy path on BSC testnet without sending config txs.

## 2. Deploy to BSC testnet
```bash
cd smart-contracts
OWNER_ADDRESS=<owner> \
ADMIN_ADDRESSES=<admin1,admin2> \
SYSTEM_WALLET_ADDRESS=<systemWallet> \
ACCEPTED_TOKENS=<bscTestnetUSDT>,<bscTestnetUSDC> \
  npx hardhat run scripts/deploy-p2p-escrow-v2.js --network bscTestnet
```
- ⚠️ **Do NOT set `MCGP_TOKEN_ADDRESS`** — MCGP is Sonic-only. With it unset, `mcgpToken` stays `address(0)` and `_feeBpsForToken` charges the standard fee (verified safe).
- ⚠️ **Do NOT use `deploy-bsc-testnet-p2p-bundle.js`** — it deploys the deprecated **v1** escrow.
- **Record the printed proxy address** and that the contract initialized with **EIP-712 domain version `"2"`**.

## 3. Point the backend at it (BSC goes live)
Set on the API server and restart:
```bash
BSC_NETWORK=testnet
P2P_ESCROW_ADDRESS_BSC=<proxy address from step 2>
# P2P_ESCROW_EIP712_VERSION_BSC defaults to "2" — only set if it differs
# If the BSC testnet token addresses differ from the bscPresets defaults,
# also override them (the bscPresets testnet token fields ship empty):
BSC_USDT_TOKEN_ADDRESS=<bscTestnetUSDT>
BSC_USDC_TOKEN_ADDRESS=<bscTestnetUSDC>
```
Until `P2P_ESCROW_ADDRESS_BSC` is set, BSC is omitted from `Config.P2PChains` (dormant) and the app/API behave exactly as today (Sonic-only).

## 4. Verify boot
On restart the log should show (from `NewP2PEscrowRegistry` / `verifyP2PChainAtBoot`):
```
[config] P2P chains enabled: sonic(14601) + bsc(97, network=testnet, escrow=0x…)
[config] P2P escrow verified: bsc 0x… on chain 97 (NNNN bytes)
```
- If you see `WARNING: bsc escrow 0x… has NO bytecode on chain 97` → wrong address / wrong network.
- If you see `configured chainID=97 but RPC reports …` → the BSC RPC is pointed at the wrong network.

## 5. Smoke test
- Backend: `GET /api/p2p/ads?chainId=97` returns `[]` (no rows yet, but no error → the chain is enabled).
- Create a BSC USDT ad (via the app once Plan 7 lands, or the API with `{"chainId":97,"token":"USDT",…}`); confirm a buyer can open → markPaid → release end-to-end on BSC testnet (deposit/open/markPaid/release + a dispute + a timeout path). Confirm a deliberately-dropped confirm gets reconciled.
- Try creating an **MCGP ad on BSC** → it must be **rejected** (asset-curation: MCGP not allowed on BSC).

## 6. (When ready) complete the asset curation
Today Sonic serves all three tokens (transition). When you want stablecoin P2P to live on BSC only (spec §4.5), narrow Sonic's allow-list:
- In `internal/config/config.go` `Load()`, change the Sonic `P2PChainConfig.Assets` from `[]string{"USDT","USDC","MCGP"}` to `[]string{"MCGP"}`, and redeploy the backend.
- After that: new Sonic ads = MCGP only; new stablecoin ads = BSC. (Existing Sonic stablecoin ads/trades are unaffected — the allow-list gates *creation*, not existing rows.)

## 7. Mainnet (audit-gated)
Same as steps 1–6 with `--network bscMainnet`, the **mainnet** USDT/USDC addresses, your production OWNER multisig, and `BSC_NETWORK=mainnet`. **Gate on the P2PEscrowV2 external re-audit** (Sonic mainnet is itself still v1 awaiting it — see `2026-05-29-p2p-escrow-v2-two-sided-design.md`).

---

## Rollback
Unset `P2P_ESCROW_ADDRESS_BSC` and restart → BSC drops out of `Config.P2PChains`, P2P reverts to Sonic-only. No on-chain action needed (the BSC escrow simply goes unused; any in-flight BSC trades remain settleable by re-enabling).
