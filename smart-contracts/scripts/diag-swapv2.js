// Read-only diagnosis of the V2 swap: is the operator authorized, are the
// debit caps initialized, is it paused?
const { ethers } = require("hardhat");
const PROXY = "0x5e19b7288E624921128742e6305372a0C980D6CE";
const ABI = [
  "function operators(address) view returns (bool)",
  "function maxDebitPerTx() view returns (uint256)",
  "function debitWindow() view returns (uint256)",
  "function debitWindowCap() view returns (uint256)",
  "function paused() view returns (bool)",
  "function premiumBalanceOf(address) view returns (uint256)",
  "function premiumCredited(address) view returns (uint256)",
  "function premiumBought(address) view returns (uint256)",
];
async function main() {
  const op = process.env.OPERATOR;
  const user = process.env.USER_WALLET;
  const c = new ethers.Contract(PROXY, ABI, ethers.provider);
  const fmt = (v) => ethers.formatUnits(v, 18);
  console.log("proxy:", PROXY);
  console.log("code at proxy:", (await ethers.provider.getCode(PROXY)).length > 2 ? "YES" : "NO — wrong address/network");
  console.log("paused:", await c.paused());
  console.log("maxDebitPerTx:", fmt(await c.maxDebitPerTx()), "MCGP  <-- 0 means caps NEVER initialized: every debit reverts");
  console.log("debitWindowCap:", fmt(await c.debitWindowCap()), "MCGP");
  console.log("debitWindow:", (await c.debitWindow()).toString(), "seconds");
  if (op) console.log(`operator ${op} authorized:`, await c.operators(op), " <-- false means every debit reverts");
  if (user) {
    console.log(`user ${user} spendable:`, fmt(await c.premiumBalanceOf(user)));
    console.log(`  credited:`, fmt(await c.premiumCredited(user)), " bought:", fmt(await c.premiumBought(user)));
  }
}
main().catch((e) => { console.error(e.message); process.exit(1); });
