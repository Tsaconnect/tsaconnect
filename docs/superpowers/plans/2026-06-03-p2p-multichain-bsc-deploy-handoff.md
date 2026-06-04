# P2P Multi-Chain — Plan 6: BSC Deploy Hand-off (operator runbook)

> **This plan is run by the owner, not the agent** — it deploys a contract (needs funded keys + the key-custody decision §10.6). The backend (Plans 1–5) is built so BSC stays **dormant** until you complete the steps below. Nothing here is gated on the agent; nothing the agent shipped depends on this to keep Sonic working.

**Goal:** Deploy `P2PEscrowV2` to BSC (testnet first), point the backend at it, and verify it comes up — at which point BSC P2P (USDT/USDC) goes live alongside Sonic.

---

## 0. Prerequisites (your decisions/assets)
- **Key custody (§10.6) — RESOLVED: same as Sonic.** `OWNER_ADDRESS` + `ADMIN_ADDRESSES` = the exact values you used for the **Sonic** `P2PEscrowV2` deploy; `SYSTEM_WALLET_ADDRESS=0xaF326D5D242C9A55590540f14658adDDd3586A8d` (the backend's configured system wallet). These addresses aren't committed to the repo — reuse your Sonic deploy values.
- **Funded BSC deployer:** the EOA running the deploy needs **tBNB** (testnet) / **BNB** (mainnet) for gas. Set its key as `PRIVATE_KEY` (or whatever `hardhat.config.js` expects) in `smart-contracts/.env`. *(Can't get tBNB? You can validate the whole flow on a local chain first — see the note in §1.5.)*
- **BSC token addresses:**
  - Mainnet (known): USDT `0x55d398326f99059fF775485246999027B3197955`, USDC `0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d`.
  - **Testnet:** deploy 18-decimal mocks via `scripts/deploy-bsc-test-tokens.js` (§1.5) — it prints the addresses + the exact env lines. ⚠️ Must be **18 decimals** (matches real BSC + the backend's per-(chain,token) decimals) — do NOT reuse Sonic's 6-decimal mocks.
- `BSCSCAN_API_KEY` in `.env` if you want source verification.

---

## 1. Dry-run (no txs)
```bash
cd smart-contracts
OWNER_ADDRESS=<owner> SYSTEM_WALLET_ADDRESS=<systemWallet> CHECK_ONLY=1 \
  npx hardhat run scripts/deploy-p2p-escrow-v2.js --network bscTestnet
```
Confirms compilation + the proxy deploy path on BSC testnet without sending config txs.

## 1.5 Deploy the BSC test stablecoins (testnet only)
```bash
cd smart-contracts
# MINT_TO=<tester wallet>   # optional; defaults to the deployer
npx hardhat run scripts/deploy-bsc-test-tokens.js --network bscTestnet
```
Deploys 18-decimal `USDT`/`USDC` mocks + mints, and prints the addresses plus the
exact `ACCEPTED_TOKENS=…` / `BSC_USDT_TOKEN_ADDRESS=…` / `BSC_USDC_TOKEN_ADDRESS=…`
lines to paste into steps 2–3. **No-tBNB validation:** run the same script (and
the escrow deploy) with `--network hardhat` or against `npx hardhat node`
(`--network localhost`, point `BSC_TESTNET_RPC` at `http://localhost:8545`) to
exercise the entire BSC path locally before you have testnet BNB.

## 2. Deploy to BSC testnet
```bash
cd smart-contracts
OWNER_ADDRESS=<same as your Sonic deploy> \
ADMIN_ADDRESSES=<same as your Sonic deploy> \
SYSTEM_WALLET_ADDRESS=0xaF326D5D242C9A55590540f14658adDDd3586A8d \
ACCEPTED_TOKENS=<USDT from §1.5>,<USDC from §1.5> \
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
