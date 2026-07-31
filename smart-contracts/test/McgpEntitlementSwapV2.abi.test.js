// ABI compatibility with the deployed V1.
//
// The backend decodes events by topic0 (`VerifyPremiumSpent`, `VerifyOperatorEvent`
// in tsa-api-go/internal/services/mcgp_swap_service.go) and packs calls by method
// NAME (`s.pack("spend", …)`). Both break silently on a rename: an eth_call against
// a missing method returns 0x, and a changed topic0 simply never matches.
//
// These are cheap assertions that would have caught the kind of drift a port
// introduces without any test noticing.

const { expect } = require("chai");
const { ethers } = require("hardhat");

// GROUND TRUTH — extracted from the deployed V1 source at
// mcgp-otc-contracts/contracts/McgpEntitlementSwap.sol. V2 must keep every one of
// these identical; it may ADD events, never change or drop one.
const V1_EVENTS = [
  "LegacySeeded(address,uint256)",
  "LegacyRedeemed(address,uint256,uint256)",
  "PremiumBought(address,uint256,uint256)",
  "PremiumSold(address,uint256,uint256)",
  "PremiumSpent(address,address,uint256,bytes32)",
  "PremiumWithdrawn(address,uint256)",
  "PremiumCredited(address,uint256,bytes32)",
  "PremiumDebitPending(address,uint256,bytes32)",
  "PremiumDebitSettled(address,uint256,bytes32)",
  "PremiumDebitReversed(address,uint256,bytes32)",
  "OperatorAdded(address)",
  "OperatorRemoved(address)",
  "GuardianAdded(address)",
  "GuardianRemoved(address)",
  "CreditCapsUpdated(uint256,uint256,uint256,uint256)",
  "PhaseAdded(uint8,uint256,uint256,string)",
  "PhaseActivated(uint8,uint256)",
  "PhasePriceUpdated(uint8,uint256,uint256)",
  "Funded(address,uint256)",
  "Withdrawn(address,address,uint256)",
];

// Methods the Go service packs by name (mcgp_swap_service.go) plus the reads the
// admin console's hand-maintained ABI declares (tsa-admin/src/abi/mcgp-entitlement-swap.ts).
const CONSUMER_METHODS = [
  // backend — writes
  "buyPremium", "spend", "sellPremium", "sellLegacy", "withdrawPremiumToWallet",
  "creditPremium", "debitPremium", "settleDebit", "reverseDebit",
  // backend — reads
  "premiumBalanceOf", "premiumBought", "premiumCredited", "pendingSettle",
  "legacyEntitlement", "legacyRedeemed", "legacyRemaining", "quote", "backing",
  // admin console
  "owner", "paused", "activePhase", "getPhases", "addPhase", "updatePhasePrice",
  "fund", "withdraw", "pause", "unpause",
];

describe("McgpEntitlementSwapV2 — ABI compatibility", function () {
  let iface;

  before(async function () {
    iface = (await ethers.getContractFactory("McgpEntitlementSwapV2")).interface;
  });

  it("keeps every V1 event signature byte-for-byte (same topic0)", async function () {
    const actual = new Set(
      iface.fragments.filter((f) => f.type === "event").map((f) => f.format("sighash")),
    );
    const missing = V1_EVENTS.filter((sig) => !actual.has(sig));
    expect(missing, `V2 dropped or renamed V1 events: ${missing.join(", ")}`).to.deep.equal([]);
  });

  it("computes the same topic0 as V1 for the events the backend decodes", async function () {
    // Spot-check the two the Go service actually parses, by hash rather than name.
    expect(ethers.id("PremiumSpent(address,address,uint256,bytes32)")).to.equal(
      iface.getEvent("PremiumSpent").topicHash,
    );
    expect(ethers.id("PremiumCredited(address,uint256,bytes32)")).to.equal(
      iface.getEvent("PremiumCredited").topicHash,
    );
  });

  it("exposes every method the backend and admin console call", async function () {
    const missing = CONSUMER_METHODS.filter((name) => {
      try {
        return iface.getFunction(name) === null;
      } catch {
        return true; // not present, or ambiguous
      }
    });
    expect(missing, `V2 is missing consumer methods: ${missing.join(", ")}`).to.deep.equal([]);
  });

  it("adds only the V2-specific events, so nothing unexpected entered the ABI", async function () {
    const actual = iface.fragments
      .filter((f) => f.type === "event")
      .map((f) => f.format("sighash"));
    const added = actual.filter((sig) => !V1_EVENTS.includes(sig));

    // Inherited OZ events (Ownable/Pausable/ERC1967) plus V2's two new ones.
    expect(added.sort()).to.deep.equal(
      [
        "DebitCapsUpdated(uint256,uint256,uint256)",
        "FeeManagerUpdated(address,address)",
        "Initialized(uint64)",
        "OwnershipTransferred(address,address)",
        "Paused(address)",
        "TrustedForwarderUpdated(address,address)",
        "Unpaused(address)",
        "Upgraded(address)",
      ].sort(),
    );
  });

  it("keeps quote(uint8,uint256) callable with a raw enum index", async function () {
    // The Go side passes a uint8, not a named enum — so the selector must accept one.
    const fn = iface.getFunction("quote");
    expect(fn.inputs[0].type).to.equal("uint8");
    expect(fn.inputs[1].type).to.equal("uint256");
  });
});
