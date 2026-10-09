// rehearse-pool.js — contract-level rehearsal of the TSA Liquidity pool on a
// testnet or a fork (spec 2026-10-08 §24.4, Plan A Task 12). Chain-generic:
// the escrow comes from deployments/<network>.json (PoolP2PEscrowV3).
//
// Keys are read from the operator's shell, never printed, never from a file:
//   REHEARSE_TREASURY_PK  — the conversion-treasury key (trade opener/buyer)
//   REHEARSE_ADMIN_PK     — the release EOA key (escrow admin)
//   REHEARSE_LP1_PK, REHEARSE_LP2_PK — two LP wallets holding the pool token + gas
//
//   PHASE=fund    npx hardhat run scripts/rehearse-pool.js --network bscTestnet   # testnet mock token only: mints to both LPs
//   PHASE=open    npx hardhat run scripts/rehearse-pool.js --network bscTestnet
//   PHASE=release npx hardhat run scripts/rehearse-pool.js --network bscTestnet
//   PHASE=cancel  npx hardhat run scripts/rehearse-pool.js --network bscTestnet   # ≥ 60 min after open
//   PHASE=report  npx hardhat run scripts/rehearse-pool.js --network bscTestnet
//
// State (trade ids, tx hashes, gas used) persists to .pool-rehearsal-<network>.json.
const fs = require("fs");
const path = require("path");
const hre = require("hardhat");
const { ethers } = hre;
const { readLedger } = require("../deploy/lib");
const { chains } = require("../deploy/chains");

const TYPES = { AdVoucher: [
  { name: "seller", type: "address" }, { name: "token", type: "address" },
  { name: "maxAmount", type: "uint256" }, { name: "minPerTrade", type: "uint256" },
  { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" },
  { name: "nonce", type: "bytes32" },
]};

function wallet(name) {
  const pk = process.env[name];
  if (!pk) throw new Error(`${name} is not set in this shell`);
  return new ethers.Wallet(pk.startsWith("0x") ? pk : "0x" + pk, ethers.provider);
}

async function main() {
  const net = hre.network.name;
  const statePath = path.join(__dirname, `.pool-rehearsal-${net}.json`);
  const state = fs.existsSync(statePath) ? JSON.parse(fs.readFileSync(statePath, "utf8")) : { gas: {} };
  const save = () => fs.writeFileSync(statePath, JSON.stringify(state, null, 2) + "\n");
  const escAddr = readLedger(net).PoolP2PEscrowV3?.proxy;
  if (!escAddr) throw new Error(`no PoolP2PEscrowV3 in deployments/${net}.json — run deploy/pool.js first`);
  const esc = await ethers.getContractAt("P2PEscrowV3", escAddr);
  const tokenAddr = chains[net].tokens[chains[net].pool.tokens[0]];
  const token = await ethers.getContractAt("MockERC20", tokenAddr);
  const dec = Number(await token.decimals());
  const treasury = wallet("REHEARSE_TREASURY_PK");
  const admin = wallet("REHEARSE_ADMIN_PK");
  const lps = [wallet("REHEARSE_LP1_PK"), wallet("REHEARSE_LP2_PK")];
  const gas = async (label, txp) => {
    const r = await (await txp).wait();
    state.gas[label] = r.gasUsed.toString();
    (state.tx ||= {})[label] = r.hash;
    save();
    return r;
  };
  const phase = process.env.PHASE || "report";

  if (phase === "fund") {
    // The BSC testnet pool token is a MockERC20 with a public mint (scripts/
    // deploy-bsc-test-tokens.js). On any mainnet or fork, refuse: fork LPs are
    // impersonated, never minted (review I3).
    const { chainId } = await ethers.provider.getNetwork();
    if (chainId !== 97n) throw new Error(`PHASE=fund is for BSC testnet (97) only, not chain ${chainId}`);
    for (const [i, lp] of lps.entries()) {
      await gas(`mint_lp${i + 1}`, token.connect(treasury).mint(lp.address, ethers.parseUnits("100", dec)));
      const bnb = await ethers.provider.getBalance(lp.address);
      console.log(`LP ${i + 1} ${lp.address}: ${ethers.formatUnits(await token.balanceOf(lp.address), dec)} token, ${ethers.formatEther(bnb)} tBNB`);
      if (bnb === 0n) console.warn(`LP ${i + 1} has no tBNB — fund it before PHASE=open`);
    }
  } else if (phase === "open") {
    const { chainId } = await ethers.provider.getNetwork();
    const domain = { name: "TSAConnectP2PEscrow", version: "2", chainId, verifyingContract: escAddr };
    const amounts = [ethers.parseUnits("6", dec), ethers.parseUnits("3", dec)]; // a 2-slice "order"
    state.trades = [];
    for (let i = 0; i < 2; i++) {
      const lp = lps[i];
      const dep = ethers.parseUnits("10", dec);
      await gas(`approve_lp${i + 1}`, token.connect(lp).approve(escAddr, dep));
      await gas(`deposit_lp${i + 1}`, esc.connect(lp).deposit(tokenAddr, dep));
      const v = { seller: lp.address, token: tokenAddr, maxAmount: dep * 100n, minPerTrade: 0, maxPerTrade: 0,
        deadline: Math.floor(Date.now() / 1000) + 90 * 86400, nonce: ethers.hexlify(ethers.randomBytes(32)) };
      const sig = await lp.signTypedData(domain, TYPES, v);
      const id = ethers.id(`rehearse-${net}-${Date.now()}-${i}`);
      await gas(`openTrade_slice${i + 1}`, esc.connect(treasury).openTrade(id, v, sig, amounts[i]));
      const t = await esc.trades(id);
      if (t.platformFeeBpsSnapshot !== 0n || t.gasFeeBpsSnapshot !== 0n) throw new Error(`slice ${i + 1} snapshot is not (0,0)`);
      state.trades.push({ id, lp: lp.address, amount: amounts[i].toString(), createdAt: t.createdAt.toString() });
    }
    save();
    console.log(`opened 2 slices; fee snapshots (0,0); state in ${statePath}`);
  } else if (phase === "release") {
    const s = state.trades[0];
    const before = await token.balanceOf(treasury.address);
    await gas("adminRelease", esc.connect(admin).adminRelease(s.id));
    const got = (await token.balanceOf(treasury.address)) - before;
    if (got.toString() !== s.amount) throw new Error(`treasury received ${got}, want ${s.amount} (fee must be 0)`);
    console.log(`released slice 1 to the treasury: ${ethers.formatUnits(got, dec)} exact`);
  } else if (phase === "cancel") {
    const s = state.trades[1];
    await gas("cancelByTimeout", esc.connect(treasury).cancelByTimeout(s.id));
    console.log("slice 2 cancelled after the window; LP 2's stock is available again");
  } else {
    console.log(JSON.stringify({ network: net, escrow: escAddr, decimals: dec, trades: state.trades, gas: state.gas, tx: state.tx }, null, 2));
  }
}

main().catch((e) => { console.error(e.message || e); process.exit(1); });
