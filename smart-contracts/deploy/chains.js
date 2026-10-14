// smart-contracts/deploy/chains.js
//
// Single source of truth for per-chain contract addresses (Sonic + BSC).
// Public on-chain addresses only — deployer keys stay in .env.
// Defaults migrated from the legacy scripts; env vars still override.
//
// Sonic mainnet addresses verified from smart-contracts/.env:
//   MAINNET_MCGP_ADDRESS=0x517600323e5E2938207fA2e2e915B9D80e5B2b21
//   MAINNET_USDC_ADDRESS=0x29219dd400f2Bf60E5a23d13Be72B486D4038894
//   MAINNET_USDT_ADDRESS=0x6047828dc181963ba44974801ff68e538da5eaf9
//
// Sonic testnet addresses from deploy-fresh-escrows-v2-testnet.js and .env.
// BSC testnet addresses from wire-bsc-p2p-escrow.js.
// BSC mainnet stablecoin addresses are canonical well-known contracts.

const CANONICAL_PERMIT2 = "0x000000000022D473030F116dDEE9F6B43aC78BA3";

// Signer roles that are the SAME address on every EVM chain since the 2026-10
// signer consolidation — what lets deploy/deterministic-escrow.js land one
// escrow address everywhere. The operator (release admin) goes into the proxy's
// initialize() calldata, so changing it changes the deterministic address.
// Owner and treasury are per-chain setup, not part of the address.
const DETERMINISTIC_ROLES = {
  owner: process.env.DET_OWNER || "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96",     // PM
  operator: process.env.DET_OPERATOR || "0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9", // release admin
  treasury: process.env.DET_TREASURY || "0xA4123022c84E66A5E955fd96C5468A78bE84e205", // systemWallet
};

// Shared pool, 100% company collateral (deploy/liquidity-pool.js, spec
// 2026-10-14). Amounts are whole tokens. Deploy-time inputs:
// The owner's wallet is the collateral provider, the guardian and the
// owner by default (owner, 2026-10-14); env overrides each. No timelock in
// development; the market-ready contracts will not be upgradeable.
//   LPOOL_OWNER_CONFIRM — the owner typed again.
function liquidityPool() {
  return {
    token: "USDT",
    collateralProvider: process.env.LPOOL_COLLATERAL_PROVIDER || DETERMINISTIC_ROLES.owner,
    operator: process.env.LPOOL_OPERATOR || DETERMINISTIC_ROLES.operator,
    guardian: process.env.LPOOL_GUARDIAN || DETERMINISTIC_ROLES.owner,
    recipient: process.env.LPOOL_RECIPIENT || DETERMINISTIC_ROLES.treasury,
    owner: process.env.LPOOL_OWNER || DETERMINISTIC_ROLES.owner,
    ownerConfirm: process.env.LPOOL_OWNER_CONFIRM || null,
    minDeposit: 5,
    maxPerLp: 5000,
    totalCap: Number(process.env.LPOOL_TOTAL_CAP || 50000),
    // Room for pool_order_max (2000) plus its spread: fill + spread is capped.
    maxFillPerTx: 2100,
    fillWindowSec: 24 * 3600,
    fillWindowCap: 20000,
    // A fill's spread may be at most this share of its amount: the console's
    // pool_spread ceiling (300 bps). Bounds what a leaked operator key can
    // credit to LPs (review 2026-10-14).
    maxSpreadBps: 300,
    // A deposit cannot be withdrawn for this long (owner 2026-10-14): no
    // depositing just before a fill and leaving right after with its spread.
    withdrawLockSec: 24 * 3600,
  };
}

// Public on-chain addresses only — deployer keys stay in .env.
// Defaults migrated from the legacy scripts; env vars still override.
const chains = {
  sonicMainnet: {
    tokens: {
      USDT: process.env.MAINNET_USDT_ADDRESS || "0x6047828dc181963ba44974801ff68e538da5eaf9",
      USDC: process.env.MAINNET_USDC_ADDRESS || "0x29219dd400f2Bf60E5a23d13Be72B486D4038894",
      MCGP: process.env.MAINNET_MCGP_ADDRESS || "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
    },
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: process.env.OWNER_ADDRESS || null,        // null → deployer at runtime
    admins: (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean),
    systemWallet: process.env.SYSTEM_WALLET_ADDRESS || null,
  },
  sonicTestnet: {
    tokens: {
      USDT: process.env.TESTNET_USDT_ADDRESS || "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad",
      USDC: process.env.TESTNET_USDC_ADDRESS || "0x685CD180e603A15507f575E7A347610b83d36d41",
      MCGP: process.env.TESTNET_MCGP_ADDRESS || "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
    },
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: process.env.OWNER_ADDRESS || null,
    admins: (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean),
    systemWallet: process.env.SYSTEM_WALLET_ADDRESS || null,
  },
  bscMainnet: {
    tokens: {
      USDT: process.env.BSC_MAINNET_USDT_ADDRESS || "0x55d398326f99059fF775485246999027B3197955",
      USDC: process.env.BSC_MAINNET_USDC_ADDRESS || "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d",
      MCGP: null, // no MCGP on BSC yet
    },
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: process.env.OWNER_ADDRESS || null,
    admins: (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean),
    systemWallet: process.env.SYSTEM_WALLET_ADDRESS || null,
    liquidityPool: liquidityPool(),
  },
  bscTestnet: {
    tokens: {
      USDT: process.env.BSC_TESTNET_USDT_ADDRESS || "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7",
      USDC: process.env.BSC_TESTNET_USDC_ADDRESS || "0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d",
      MCGP: null,
    },
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: process.env.OWNER_ADDRESS || null,
    admins: (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean),
    systemWallet: process.env.SYSTEM_WALLET_ADDRESS || null,
    liquidityPool: liquidityPool(),
  },
};


module.exports = { chains, CANONICAL_PERMIT2, DETERMINISTIC_ROLES };
