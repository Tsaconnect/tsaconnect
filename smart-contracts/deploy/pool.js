// smart-contracts/deploy/pool.js
//
// TSA Liquidity pool contracts on any EVM chain whose deploy/chains.js entry
// has a `pool` block (spec 2026-10-08 §4.2, S1): a FeeManager and a
// P2PEscrowV3 that accepts only the pool tokens, the fee set to zero TWICE
// (FeeManager rows active at 0 AND the legacy feeBps 0 — an inactive row falls
// back to feeBps, S2), a 60-minute payment window (S3), every value read back
// and asserted, and only then ownership handed to the owner's wallet (decision
// 18). One-step Ownable: the printed owner address is final — check it twice.
//
// NEVER run scripts/seed-feemanager-rates.js against the pool FeeManager: it
// sets a non-zero p2p_platform rate.
//
//   POOL_RELEASE_ADMIN=0x… POOL_SYSTEM_WALLET=0x… [POOL_OWNER_ADDRESS=0x… POOL_OWNER_CONFIRM=0x…] \
//     npx hardhat run deploy/pool.js --network bscTestnet
const { getAddress } = require("ethers");
const { resolveChain, assertOwnerConfirm, handOverOwnership } = require("./lib");
const feemanager = require("./feemanager");
const p2pEscrow = require("./p2p-escrow");

const FEATURES = ["p2p_platform", "p2p"];

function pick(tokens, symbols) {
  const out = {};
  for (const sym of symbols) {
    if (!tokens[sym]) throw new Error(`pool: token ${sym} has no address on this network`);
    out[sym] = tokens[sym];
  }
  return out;
}

async function assertPool(hre, { fm, esc, tokens, allTokens, pool }) {
  const { ethers } = hre;
  const fail = (m) => { throw new Error(`pool: assertion failed — ${m}`); };
  const fmAddr = await fm.getAddress();
  if (getAddress(await esc.feeManager()) !== getAddress(fmAddr)) fail("escrow.feeManager() is not the pool FeeManager");
  if ((await esc.feeBps()) !== 0n) fail("escrow.feeBps() is not 0");
  if ((await esc.paymentWindow()) !== BigInt(pool.paymentWindowSec)) fail(`paymentWindow() is not ${pool.paymentWindowSec}`);
  if (!(await esc.isAdmin(pool.releaseAdmin))) fail(`release admin ${pool.releaseAdmin} is not an escrow admin`);
  if (getAddress(await esc.systemWallet()) !== getAddress(pool.systemWallet)) fail("systemWallet is not the pool's");
  for (const [sym, addr] of Object.entries(tokens)) {
    if (!(await esc.acceptedTokens(addr))) fail(`${sym} is not accepted`);
    const key = await esc.tokenSymbol(addr);
    for (const f of FEATURES) {
      const [bps, flat, exempt, active] = await fm.rateFor(f, key);
      if (bps !== 0n || flat !== 0n || exempt || !active) fail(`${f}/${key} is not (0, 0, false, active)`);
    }
    const erc = await ethers.getContractAt("MockERC20", addr); // any ERC-20 ABI with decimals()
    if (Number(await erc.decimals()) < 6) fail(`${sym} has fewer than 6 decimals`);
  }
  for (const [sym, addr] of Object.entries(allTokens)) {
    if (!addr || tokens[sym]) continue;
    if (await esc.acceptedTokens(addr)) fail(`non-pool token ${sym} is accepted`);
  }
}

async function deploy(hre, { chain } = {}) {
  const { ethers } = hre;
  const cfg = chain || resolveChain(hre);
  const pool = cfg.pool;
  if (!pool) throw new Error(`pool: network ${hre.network.name} has no pool block in deploy/chains.js`);
  if (!pool.releaseAdmin) throw new Error("pool: POOL_RELEASE_ADMIN (the PM release EOA) is required");
  if (!pool.systemWallet) throw new Error("pool: POOL_SYSTEM_WALLET (the conversion treasury) is required");
  if (pool.requireOwner && !pool.owner) {
    throw new Error("pool: POOL_OWNER_ADDRESS (the owner's wallet, decision 18) is required on this network");
  }
  const tokens = pick(cfg.tokens, pool.tokens);
  const [deployer] = await ethers.getSigners();
  const deployerAddr = await deployer.getAddress();
  if (pool.requireOwner) {
    // Review m1: on a requireOwner network the hot deployer key never owns the
    // pool, and the irreversible owner address is confirmed by typing it twice.
    assertOwnerConfirm({
      label: "pool", owner: pool.owner, confirm: pool.ownerConfirm, deployerAddr,
      ownerVar: "POOL_OWNER_ADDRESS", confirmVar: "POOL_OWNER_CONFIRM",
    });
  }
  const finalOwner = pool.owner || deployerAddr;
  const fwd = chain ? { chain } : {};

  const fmOut = await feemanager.deploy(hre, { ...fwd, deferOwnership: true, writeLedgerKey: "PoolFeeManager" });
  const escOut = await p2pEscrow.deploy(hre, {
    ...fwd, feeManager: fmOut.proxy, deferOwnership: true,
    admins: [pool.releaseAdmin], tokens, systemWallet: pool.systemWallet, writeLedgerKey: "PoolP2PEscrowV3",
  });
  const fm = await ethers.getContractAt("FeeManager", fmOut.proxy);
  const esc = await ethers.getContractAt("P2PEscrowV3", escOut.proxy);
  try {
    const features = [], keys = [], rates = [];
    for (const addr of Object.values(tokens)) {
      const key = await esc.tokenSymbol(addr); // the FeeManager key is the escrow's own symbol
      for (const f of FEATURES) { features.push(f); keys.push(key); rates.push([0, 0, false, true]); }
    }
    await (await fm.setRates(features, keys, rates)).wait();
    await (await esc.setFeeBps(0)).wait();
    await (await esc.setPaymentWindow(pool.paymentWindowSec)).wait();
    await assertPool(hre, { fm, esc, tokens, allTokens: cfg.tokens, pool });
  } catch (e) {
    e.deployed = { feeManager: fmOut.proxy, escrow: escOut.proxy };
    console.error(`pool: NOT handed over — still owned by the deployer ${deployerAddr}. FeeManager=${fmOut.proxy} escrow=${escOut.proxy}`);
    throw e;
  }
  if (getAddress(finalOwner) !== getAddress(deployerAddr)) {
    console.log(`pool: transferring BOTH contracts to ${finalOwner} (one-step Ownable — this address is final)`);
  }
  // Review m1: both owners are read back, so a failed second transfer is never silent.
  await handOverOwnership("pool", [fm, esc], finalOwner);
  console.log(`pool: ready FeeManager=${fmOut.proxy} escrow=${escOut.proxy} owner=${finalOwner} window=${pool.paymentWindowSec}s`);
  return { feeManager: fmOut.proxy, escrow: escOut.proxy, owner: finalOwner };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy, assertPool };
