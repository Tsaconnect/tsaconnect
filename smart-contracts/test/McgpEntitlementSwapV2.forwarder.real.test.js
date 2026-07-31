// Request validity against the REAL OpenZeppelin ERC2771Forwarder.
//
// MockForwarder proves the callee resolves _msgSender() correctly, but it has no
// signature check, no nonce and no deadline — so it proves nothing about whether
// a request is legitimate. Everything the relayer depends on lives here.
//
// The eip712Domain test is the one that saves a day of debugging: the Go relayer
// packs a domain independently, and a mismatch surfaces only as an opaque
// InvalidSigner revert. This is the same class of bug as the P2P chainId
// invariant documented in tsa-dev/CLAUDE.md.

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

const PREMIUM_BUY = 0;
const mcgp = (n) => ethers.parseUnits(String(n), 18);
const usdc = (n) => ethers.parseUnits(String(n), 6);
const refOf = (s) => ethers.keccak256(ethers.toUtf8Bytes(s));
// minCredit, maxCreditPerTx, creditWindow, creditWindowCap, maxDebitPerTx, debitWindow, debitWindowCap
const DEFAULT_CAPS = [0, mcgp(1_000_000_000), 3600, mcgp(1_000_000_000), mcgp(1_000_000_000), 3600, mcgp(1_000_000_000)];

const FORWARDER_NAME = "TSAConnectForwarder";

describe("McgpEntitlementSwapV2 — real ERC2771Forwarder", function () {
  let swap, mcgpToken, usdcToken, forwarder;
  let owner, alice, bob, relayer;

  beforeEach(async function () {
    [owner, alice, bob, relayer] = await ethers.getSigners();

    const MockERC20 = await ethers.getContractFactory("MockERC20");
    mcgpToken = await MockERC20.deploy("MCGP Token", "MCGP", 18);
    usdcToken = await MockERC20.deploy("USD Coin", "USDC", 6);
    await mcgpToken.waitForDeployment();
    await usdcToken.waitForDeployment();

    const Forwarder = await ethers.getContractFactory("TSAForwarder");
    forwarder = await Forwarder.deploy(FORWARDER_NAME);
    await forwarder.waitForDeployment();

    const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
    swap = await upgrades.deployProxy(
      Swap,
      [owner.address, await mcgpToken.getAddress(), await usdcToken.getAddress(), ...DEFAULT_CAPS],
      { kind: "uups" },
    );
    await swap.waitForDeployment();

    await swap.addPhase(PREMIUM_BUY, 20000, "premium buy");
    await swap.setTrustedForwarder(await forwarder.getAddress());

    await mcgpToken.mint(owner.address, mcgp(10_000_000));
    await usdcToken.mint(alice.address, usdc(1_000_000));

    // Give alice spendable premium to relay with.
    await mcgpToken.approve(swap.target, mcgp(10_000));
    await swap.fund(await mcgpToken.getAddress(), mcgp(10_000));
    const cost = await swap.quote(PREMIUM_BUY, mcgp(1000));
    await usdcToken.connect(alice).approve(swap.target, cost);
    await swap.connect(alice).buyPremium(mcgp(1000), cost);
  });

  async function domain() {
    const { chainId } = await ethers.provider.getNetwork();
    return {
      name: FORWARDER_NAME,
      version: "1",
      chainId,
      verifyingContract: await forwarder.getAddress(),
    };
  }

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

  // Builds a signed ForwardRequestData. `signer` defaults to `from` — pass a
  // different one to forge.
  async function signedRequest({ from, data, deadline, gas = 500_000n, signer = null, nonce = null }) {
    const dl = deadline ?? (await time()) + 3600;
    const n = nonce ?? (await forwarder.nonces(from.address));
    const message = {
      from: from.address,
      to: await swap.getAddress(),
      value: 0n,
      gas,
      nonce: n,
      deadline: dl,
      data,
    };
    const signature = await (signer ?? from).signTypedData(await domain(), TYPES, message);
    return { ...message, signature };
  }

  async function time() {
    const block = await ethers.provider.getBlock("latest");
    return block.timestamp;
  }

  const spendData = (to, amount, ref) => swap.interface.encodeFunctionData("spend", [to, amount, ref]);

  it("relays a valid request: the USER is debited and the RELAYER pays gas", async function () {
    const req = await signedRequest({ from: alice, data: spendData(bob.address, mcgp(100), refOf("real:1")) });

    const relayerBefore = await ethers.provider.getBalance(relayer.address);
    await forwarder.connect(relayer).execute(req);
    const relayerAfter = await ethers.provider.getBalance(relayer.address);

    expect(await swap.premiumBought(alice.address)).to.equal(mcgp(900));
    expect(await swap.premiumBought(bob.address)).to.equal(mcgp(100));
    expect(relayerAfter).to.be.lt(relayerBefore); // the relayer, not alice, paid
  });

  it("rejects a replayed request — the nonce is consumed", async function () {
    const req = await signedRequest({ from: alice, data: spendData(bob.address, mcgp(100), refOf("real:2")) });
    await forwarder.connect(relayer).execute(req);

    await expect(forwarder.connect(relayer).execute(req))
      .to.be.revertedWithCustomError(forwarder, "ERC2771ForwarderInvalidSigner");
    expect(await swap.premiumBought(bob.address)).to.equal(mcgp(100)); // moved exactly once
  });

  it("rejects an expired deadline", async function () {
    const req = await signedRequest({
      from: alice,
      data: spendData(bob.address, mcgp(100), refOf("real:3")),
      deadline: (await time()) - 1,
    });
    await expect(forwarder.connect(relayer).execute(req))
      .to.be.revertedWithCustomError(forwarder, "ERC2771ForwarderExpiredRequest");
  });

  it("rejects a request signed by someone other than `from`", async function () {
    const req = await signedRequest({
      from: alice,
      data: spendData(bob.address, mcgp(100), refOf("real:4")),
      signer: bob, // bob signs a request claiming to be alice
    });
    await expect(forwarder.connect(relayer).execute(req))
      .to.be.revertedWithCustomError(forwarder, "ERC2771ForwarderInvalidSigner");
    expect(await swap.premiumBought(alice.address)).to.equal(mcgp(1000));
  });

  it("rejects a request carrying the wrong nonce", async function () {
    const current = await forwarder.nonces(alice.address);
    const req = await signedRequest({
      from: alice,
      data: spendData(bob.address, mcgp(100), refOf("real:5")),
      nonce: current + 5n,
    });
    await expect(forwarder.connect(relayer).execute(req))
      .to.be.revertedWithCustomError(forwarder, "ERC2771ForwarderInvalidSigner");
  });

  it("bubbles the target's revert instead of half-executing", async function () {
    const req = await signedRequest({
      from: alice,
      data: spendData(bob.address, mcgp(999_999), refOf("real:6")), // more than alice holds
    });
    await expect(forwarder.connect(relayer).execute(req)).to.be.reverted;
    expect(await swap.premiumBought(alice.address)).to.equal(mcgp(1000)); // untouched
    expect(await swap.premiumBought(bob.address)).to.equal(0n);
  });

  it("a third party — not just our relayer — can submit a signed request", async function () {
    // execute() is permissionless. The backend's allowlist and rate limits are
    // budget controls; they are not access control on this contract.
    const req = await signedRequest({ from: alice, data: spendData(bob.address, mcgp(50), refOf("real:7")) });
    await forwarder.connect(bob).execute(req);
    expect(await swap.premiumBought(bob.address)).to.equal(mcgp(50));
  });

  it("the EIP-712 domain matches what an off-chain signer must pack", async function () {
    const [fields, name, version, chainId, verifyingContract, salt, extensions] =
      await forwarder.eip712Domain();
    const { chainId: netChainId } = await ethers.provider.getNetwork();

    expect(name).to.equal(FORWARDER_NAME);
    expect(version).to.equal("1");
    expect(chainId).to.equal(netChainId);
    expect(verifyingContract).to.equal(await forwarder.getAddress());
    expect(fields).to.equal("0x0f"); // name, version, chainId, verifyingContract
    expect(salt).to.equal("0x" + "00".repeat(32));
    expect(extensions.length).to.equal(0);
  });

  it("verify() agrees with execute() on a well-formed request", async function () {
    const req = await signedRequest({ from: alice, data: spendData(bob.address, mcgp(10), refOf("real:8")) });
    expect(await forwarder.verify(req)).to.equal(true);
    await forwarder.connect(relayer).execute(req);
    expect(await forwarder.verify(req)).to.equal(false); // nonce now consumed
  });
});
