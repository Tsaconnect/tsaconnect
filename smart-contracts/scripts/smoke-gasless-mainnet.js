// Checkpoint B, on real Sonic mainnet: a wallet holding ZERO S completes an
// MCGP spend, with the relayer paying the gas.
//
// This is the proof the whole project exists for. The contract suite already
// covers the mechanism against a real ERC2771Forwarder, but only a live run
// exercises the actual chain, RPC, gas market and deployed bytecode together.
//
// Requires the deployer to hold MCGP (funded from V1's withdrawable excess) and
// to be V2's owner, which it is until ownership transfers at cutover.
//
//   FUND_MCGP=100 npx hardhat run scripts/smoke-gasless-mainnet.js --network sonicMainnet
//
// Env:
//   FUND_MCGP        MCGP to move into V2 as backing (default 50)
//   CREDIT_MCGP      spend-only premium to credit the test wallet (default 10)
//   SPEND_MCGP       amount the zero-S wallet spends (default 1)
//   KEEP_FORWARDER=1 leave trustedForwarder set afterwards (default: restore to 0x0)
//   CHECK_ONLY=1     print the plan and exit

const { ethers } = require("hardhat");

const SWAP = "0x5e19b7288E624921128742e6305372a0C980D6CE";
const FORWARDER = "0x569F8a58dFCCaA600d92B641F1654dCEd2dA23e4";
const MCGP = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21";
const ZERO = "0x0000000000000000000000000000000000000000";

const TYPES = {
  ForwardRequest: [
    { name: "from", type: "address" },
    { name: "to", type: "address" },
    { name: "value", type: "uint256" },
    { name: "gas", type: "uint256" },
    { name: "nonce", type: "uint256" },
    { name: "deadline", type: "uint48" },
    { name: "data", type: "bytes" },
  ],
};

const mcgp = (n) => ethers.parseUnits(String(n), 18);
const fmt = (v) => ethers.formatUnits(v, 18);

async function main() {
  const [relayer] = await ethers.getSigners();
  const swap = await ethers.getContractAt("McgpEntitlementSwapV2", SWAP);
  const fwd = await ethers.getContractAt("TSAForwarder", FORWARDER);
  const token = await ethers.getContractAt("MockERC20", MCGP); // ERC-20 surface only

  const fund = mcgp(process.env.FUND_MCGP || "50");
  const credit = mcgp(process.env.CREDIT_MCGP || "10");
  const spend = mcgp(process.env.SPEND_MCGP || "1");

  const relayerMcgp = await token.balanceOf(relayer.address);
  console.log("relayer/owner :", relayer.address);
  console.log("relayer MCGP  :", fmt(relayerMcgp));
  console.log("relayer S     :", ethers.formatEther(await ethers.provider.getBalance(relayer.address)));
  console.log("plan          : fund", fmt(fund), "→ credit", fmt(credit), "→ gasless spend", fmt(spend));

  if (process.env.CHECK_ONLY) return console.log("CHECK_ONLY=1 — nothing sent.");
  if (relayerMcgp < fund) {
    throw new Error(`need ${fmt(fund)} MCGP, hold ${fmt(relayerMcgp)} — withdraw V1's excess to this address first`);
  }

  // A brand-new wallet that has never held anything. This is the whole point:
  // it cannot pay for its own transaction.
  const user = ethers.Wallet.createRandom().connect(ethers.provider);
  const payee = ethers.Wallet.createRandom();
  const userS = await ethers.provider.getBalance(user.address);
  console.log("\ntest wallet   :", user.address, "S balance:", ethers.formatEther(userS));
  if (userS !== 0n) throw new Error("test wallet is not empty — it must have zero S");

  // 1. Back the contract, then credit the user spend-only premium.
  console.log("\n1. funding V2 …");
  await (await token.approve(SWAP, fund)).wait();
  await (await swap.fund(MCGP, fund)).wait();

  console.log("2. adding operator + crediting the test wallet …");
  if (!(await swap.operators(relayer.address))) {
    await (await swap.addOperator(relayer.address)).wait();
  }
  const creditRef = ethers.keccak256(ethers.toUtf8Bytes(`smoke:credit:${Date.now()}`));
  await (await swap.creditPremium(user.address, credit, creditRef)).wait();
  console.log("   user premium:", fmt(await swap.premiumBalanceOf(user.address)));

  // 2. Turn gasless on. Safe here: V2 is dark, nothing points at it, and the
  //    only value inside is what this script just put there.
  console.log("3. wiring the forwarder …");
  await (await swap.setTrustedForwarder(FORWARDER)).wait();
  if (!(await swap.isTrustedForwarder(FORWARDER))) throw new Error("forwarder not trusted after set");

  // 3. The zero-S wallet signs a ForwardRequest. It never touches the chain.
  console.log("4. signing a ForwardRequest from the empty wallet …");
  const spendRef = ethers.keccak256(ethers.toUtf8Bytes(`smoke:spend:${Date.now()}`));
  const data = swap.interface.encodeFunctionData("spend", [payee.address, spend, spendRef]);
  const { chainId } = await ethers.provider.getNetwork();
  const domain = {
    name: "TSAConnectForwarder",
    version: "1",
    chainId,
    verifyingContract: FORWARDER,
  };
  const request = {
    from: user.address,
    to: SWAP,
    value: 0n,
    gas: 300_000n,
    nonce: await fwd.nonces(user.address),
    deadline: Math.floor(Date.now() / 1000) + 300, // 5 min, per the relayer's policy
    data,
  };
  const signature = await user.signTypedData(domain, TYPES, request);

  // 4. The relayer submits and pays.
  console.log("5. relaying …");
  const relayerBefore = await ethers.provider.getBalance(relayer.address);
  const tx = await fwd.execute({ ...request, signature });
  const receipt = await tx.wait();
  const relayerAfter = await ethers.provider.getBalance(relayer.address);

  // 5. Assert the thing we actually claim.
  const userAfterS = await ethers.provider.getBalance(user.address);
  const userPremium = await swap.premiumBalanceOf(user.address);
  const payeePremium = await swap.premiumBalanceOf(payee.address);

  console.log("\n── result ─────────────────────────────────────────────");
  console.log("tx                :", receipt.hash);
  console.log("gas used          :", receipt.gasUsed.toString());
  console.log("relayer S spent   :", ethers.formatEther(relayerBefore - relayerAfter));
  console.log("test wallet S     :", ethers.formatEther(userAfterS), "(unchanged — it paid nothing)");
  console.log("user premium      :", fmt(userPremium), `(was ${fmt(credit)})`);
  console.log("payee premium     :", fmt(payeePremium));
  console.log("payee is CREDITED :", fmt(await swap.premiumCredited(payee.address)),
    "/ bought:", fmt(await swap.premiumBought(payee.address)), "← provenance preserved");

  const problems = [];
  if (userAfterS !== 0n) problems.push("the test wallet's S balance changed — it should never pay");
  if (relayerAfter >= relayerBefore) problems.push("the relayer did not pay for the transaction");
  if (userPremium !== credit - spend) problems.push("user premium did not decrease by the spend");
  if (payeePremium !== spend) problems.push("payee did not receive the spend");
  if ((await swap.premiumBought(payee.address)) !== 0n) {
    problems.push("credited premium became BOUGHT — provenance invariant broken");
  }
  if (problems.length) throw new Error("SMOKE TEST FAILED:\n  - " + problems.join("\n  - "));

  // 6. Put it back to dark unless told otherwise, so enabling gasless stays a
  //    deliberate, separate release rather than a side effect of testing.
  if (!process.env.KEEP_FORWARDER) {
    console.log("\n6. restoring dark (setTrustedForwarder → 0x0) …");
    await (await swap.setTrustedForwarder(ZERO)).wait();
    console.log("   trustedForwarder:", await swap.trustedForwarder());
  } else {
    console.log("\n6. KEEP_FORWARDER=1 — leaving gasless ENABLED on V2");
  }

  console.log("\n✅ zero-S wallet completed an MCGP spend on Sonic mainnet.");
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
