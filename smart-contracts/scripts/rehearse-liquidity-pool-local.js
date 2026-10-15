// Local rehearsal fixture for the Go vault adapter (tsa-api-go
// internal/services/liquidity_vault_node_test.go, build tag vaultnode).
// Deploys a mock 18-dp USDT and TsaLiquidityPool on a LOCAL hardhat node
// (refuses any other chain), allow-lists one LP and deposits its 25 principal
// (no collateral: the LPs' deposits are the pool's liquidity), and prints the
// addresses as JSON. Hardhat's well-known dev keys only. `provider` is the
// company's refill wallet (funded and approved for refill/payPending runs).
//
//   npx hardhat node &   npx hardhat run scripts/rehearse-liquidity-pool-local.js --network localhost
const hre = require("hardhat");
const { ethers, upgrades } = hre;

async function main() {
  const { chainId } = await ethers.provider.getNetwork();
  if (chainId !== 31337n) throw new Error(`refusing chain ${chainId}: local hardhat (31337) only`);
  const [owner, provider, operator, guardian, treasury, lp] = await ethers.getSigners();
  const u = (n) => ethers.parseUnits(String(n), 18);
  const token = await (await ethers.getContractFactory("MockERC20")).deploy("Tether USD", "USDT", 18);
  const Pool = await ethers.getContractFactory("TsaLiquidityPool");
  const pool = await upgrades.deployProxy(Pool, [{
    token: await token.getAddress(), owner: owner.address, collateralProvider: provider.address,
    operator: operator.address, guardian: guardian.address, recipient: treasury.address,
    minDeposit: u(5), maxPerLp: u(5000), totalCap: u(50000), maxFillPerTx: u(1000), fillWindow: 86400, fillWindowCap: u(10000), maxSpreadBps: 300, withdrawLock: 0,
  }], { kind: "uups" });
  await pool.waitForDeployment();
  const deployBlock = await ethers.provider.getBlockNumber();
  const poolAddr = await pool.getAddress();
  for (const s of [provider, lp]) {
    await (await token.mint(s.address, u(1000))).wait();
    await (await token.connect(s).approve(poolAddr, ethers.MaxUint256)).wait();
  }
  await (await pool.connect(operator).setDepositor(lp.address, true)).wait();
  await (await pool.connect(lp).deposit(u(25))).wait();
  console.log(JSON.stringify({ vault: poolAddr, token: await token.getAddress(), deployBlock,
    operator: operator.address, treasury: treasury.address, lp: lp.address, refillWallet: provider.address }));
}

main().catch((e) => { console.error(e); process.exit(1); });
