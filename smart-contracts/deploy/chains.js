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
// The consolidated hot keys themselves (2026-10 signer consolidation), fixed
// here so an env override of DET_OPERATOR / DET_TREASURY can never make them
// admit-able: deploy/p2p-direct.js refuses both as admin or owner regardless.
const CANONICAL_HOT_KEYS = {
  operator: "0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9",
  treasury: "0xA4123022c84E66A5E955fd96C5468A78bE84e205",
};

const DETERMINISTIC_ROLES = {
  owner: process.env.DET_OWNER || "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96",     // PM
  operator: process.env.DET_OPERATOR || CANONICAL_HOT_KEYS.operator, // release admin
  treasury: process.env.DET_TREASURY || CANONICAL_HOT_KEYS.treasury, // systemWallet
};

const addressList = (v) => (v || "").split(",").map((s) => s.trim()).filter(Boolean);

// Direct (user-to-user) P2P escrow on BSC (deploy/p2p-direct.js). Deployed
// through deploy/deterministic-escrow.js with its own FeeManager; owner /
// systemWallet are DETERMINISTIC_ROLES (DET_OWNER, DET_TREASURY) like every
// deterministic escrow. Operator-supplied at deploy time:
//   ARBITER_ADDRESSES       — comma list, the dispute arbiters: the escrow's
//                             ONLY admins (in initialize(), so part of the address);
//   DIRECT_FORBIDDEN_ADMINS — comma list that must never be an admin or the
//                             owner: the relayer and any other hot key (the
//                             operator, treasury — roles and CANONICAL_HOT_KEYS —,
//                             deployer and routine deploy keys are always refused);
//   DIRECT_RATES_JSON       — the live FeeManager rates, e.g.
//                             {"p2p_platform":{"bps":100},"p2p":{"bps":50}}.
function directBlock() {
  return {
    tokens: ["USDT", "USDC"],
    paymentWindowSec: 1800,
    arbiters: addressList(process.env.ARBITER_ADDRESSES),
    forbiddenAdmins: addressList(process.env.DIRECT_FORBIDDEN_ADMINS),
    rates: process.env.DIRECT_RATES_JSON || null,
  };
}

// Shared pool, no company collateral (deploy/liquidity-pool.js, spec
// 2026-10-14 + no-collateral amendment). Amounts are whole tokens. Deploy-time
// inputs: the owner's wallet is the refill wallet ("collateralProvider" role,
// name kept for the storage layout), the guardian and the
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
    minDeposit: 1, // PM 2026-10-15: $1 (what the live mainnet pool reads)
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

// Audit-ready core set (deploy/core.js; spec docs/specs/2026-10-27-contracts-audit-ready-redesign.md).
// FeeConfig + OrderEscrow (Sonic) + P2PTradeEscrow. Everything here is applied at initialization, so the
// admin owns the contracts from their first block and the deployer never holds a role.
// Rates mirror what the live contracts charge today (Sonic FeeManager has no rows: the escrows run
// on their legacy fallback of 2% + $0.10 for orders and 0.5% for P2P; BSC charges 1.9% p2p_platform).
// Limits are the pre-audit value caps (decision D7): $1,000 per order/trade, $20,000 owed per contract.
// Amounts are in each token's base units: Sonic stables have 6 decimals, BSC stables and MCGP 18.
//   CORE_ADMIN / CORE_ADMIN_CONFIRM — the admin (default: the PM), typed twice on mainnets.
//   CORE_FEE_RECIPIENT              — default: the system fee wallet.
//   CORE_RESOLVERS / CORE_PAUSERS   — comma lists, granted at initialization.
//   CORE_FORWARDER                  — overrides the chain's forwarder (must be a TSAForwarder).
//   CORE_FORBIDDEN                  — comma list of extra addresses that may hold no role and may
//                                     not be the fee recipient (e.g. the relayer key).
const SYSTEM_FEE_WALLET = "0xaF326D5D242C9A55590540f14658adDDd3586A8d";
const units = (n, decimals) => (BigInt(n) * 10n ** BigInt(decimals)).toString();
function coreBlock({ chainId, stableDecimals, forwarder, forwarderName = null, order, p2p }) {
  const stableLimits = { maxPerOrder: units(1000, stableDecimals), maxOwed: units(20000, stableDecimals) };
  return {
    chainId, // asserted against the RPC before anything is sent
    decimals: { USDT: stableDecimals, USDC: stableDecimals, MCGP: 18 }, // asserted against each token
    forbidden: addressList(process.env.CORE_FORBIDDEN),
    admin: process.env.CORE_ADMIN || DETERMINISTIC_ROLES.owner,
    adminConfirm: process.env.CORE_ADMIN_CONFIRM || null,
    feeRecipient: process.env.CORE_FEE_RECIPIENT || SYSTEM_FEE_WALLET,
    resolvers: addressList(process.env.CORE_RESOLVERS),
    pausers: addressList(process.env.CORE_PAUSERS),
    forwarder: process.env.CORE_FORWARDER || forwarder,
    // The EIP-712 name the forwarder was deployed with (TSAForwarder's constructor argument);
    // deploy/core.js refuses a forwarder that does not report it.
    forwarderName: process.env.CORE_FORWARDER_NAME || forwarderName,
    order, // { SYMBOL: { bps, flat } } or null when the chain has no OrderEscrow
    p2p, //   { SYMBOL: { platformBps, gasBps } }
    limits: {
      USDT: stableLimits,
      USDC: stableLimits,
      // MCGP at its $0.10 OTC price: 10,000 MCGP ≈ $1,000, 200,000 MCGP ≈ $20,000.
      MCGP: { maxPerOrder: units(10000, 18), maxOwed: units(200000, 18) },
    },
  };
}
const sonicCore = ({ chainId, forwarder, forwarderName = null }) =>
  coreBlock({
    chainId,
    stableDecimals: 6,
    forwarder,
    forwarderName,
    order: {
      USDT: { bps: 200, flat: "100000" }, // 2% merchant fee (seller pays) + $0.10 flat (buyer)
      USDC: { bps: 200, flat: "100000" },
      MCGP: { bps: 0, flat: "0" }, // MCGP orders were fee-exempt
    },
    p2p: { USDT: { platformBps: 0, gasBps: 50 }, USDC: { platformBps: 0, gasBps: 50 } }, // MCGP not in P2P (2026-09-28)
  });
const bscCore = ({ chainId }) =>
  coreBlock({
    chainId,
    stableDecimals: 18,
    forwarder: null, // the direct escrow never had a forwarder (decision Q12)
    order: null,
    p2p: { USDT: { platformBps: 190, gasBps: 0 }, USDC: { platformBps: 190, gasBps: 0 } },
  });

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
    core: sonicCore({ chainId: 146, forwarder: "0x569F8a58dFCCaA600d92B641F1654dCEd2dA23e4", forwarderName: "TSAConnectForwarder" }),
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
    core: sonicCore({ chainId: 14601, forwarder: null }),
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
    direct: directBlock(),
    core: bscCore({ chainId: 56 }),
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
    direct: directBlock(),
    core: bscCore({ chainId: 97 }),
  },
};


module.exports = { chains, CANONICAL_PERMIT2, CANONICAL_HOT_KEYS, DETERMINISTIC_ROLES, SYSTEM_FEE_WALLET };
