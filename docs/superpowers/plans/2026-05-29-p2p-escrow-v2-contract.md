# P2P Escrow v2 — Two-Sided Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `P2PEscrowV2.sol` — a two-sided, role-parameterized, UUPS-upgradeable escrow that supports both merchant-sells-crypto (pooled) and user-sells-crypto (just-in-time deposit) flows, with built-in-but-default-disabled Permit2/EIP-2771/optimistic-auto-release/dispute-fee hooks, tunable economic parameters, and a clean Hardhat test suite.

**Architecture:** Evolution of the existing `P2PEscrow.sol`. The four economic roles collapse to two (`seller` = crypto-provider/fiat-receiver/releaser; `buyer` = crypto-recipient/fiat-payer). A `funding` flag (`PooledFromMaker` | `DepositedByTaker`) selects the deposit source at open and the refund destination at cancel/dispute. All settlement, dispute, fee, and auto-release internals are shared. The contract is design-complete so it passes a single re-audit; off-chain services (relayer, keeper, dispute UX, backend, app) are separate sub-projects and out of scope here.

**Tech Stack:** Solidity 0.8.24, Hardhat, OpenZeppelin Upgradeable (UUPS, Ownable, Pausable, EIP712, ReentrancyGuard, Context), `@openzeppelin/hardhat-upgrades`, ethers v6, chai.

**Spec:** `docs/superpowers/specs/2026-05-29-p2p-escrow-v2-two-sided-design.md`
**Reference contract (read in full before starting):** `smart-contracts/contracts/P2PEscrow.sol`

---

## File structure

- **Create** `smart-contracts/contracts/P2PEscrowV2.sol` — the v2 contract (single file, mirrors v1's single-file layout).
- **Create** `smart-contracts/contracts/test/MockPermit2.sol` — minimal Permit2 stand-in for tests (implements `permitTransferFrom` by doing a plain `transferFrom`).
- **Create** `smart-contracts/contracts/test/MockForwarder.sol` — minimal EIP-2771 forwarder for tests (appends the signer's address to calldata and relays).
- **Create** `smart-contracts/contracts/test/MockERC20.sol` — only if the repo lacks a reusable mock (check first; v1 tests likely already have one — reuse it).
- **Create** `smart-contracts/test/P2PEscrowV2.test.js` — the v2 test suite.
- **Create** `smart-contracts/scripts/deploy-p2p-escrow-v2.js` — UUPS proxy deploy + config script (cutover/config-repoint stays in the migration sub-project).
- **Do NOT modify** `P2PEscrow.sol` or `test/P2PEscrow.test.js` (v1 stays intact for reference until cutover).

> The whole contract lives in one file to match v1 and keep the audit surface a single artifact. Tests are grouped by lifecycle area within one describe-tree.

---

## Conventions for every task

- Work from `smart-contracts/`.
- Run a single test file with: `npx hardhat test test/P2PEscrowV2.test.js`
- Run a focused test with grep: `npx hardhat test test/P2PEscrowV2.test.js --grep "name"`
- Compile with: `npx hardhat compile`
- Use `@openzeppelin/hardhat-upgrades` `upgrades.deployProxy(Factory, [owner, admins], { kind: 'uups' })` to deploy.
- Commit after each task with the message shown.

---

## Task 0: Toolchain prep (no code change)

**Files:** none (environment only)

- [ ] **Step 1: Install dependencies**

This repo's `node_modules` is not present. From `smart-contracts/`:

Run: `npm install`
Expected: completes; `node_modules/.bin/hardhat` exists.

- [ ] **Step 2: Confirm the upgrades plugin is available**

Run: `node -e "require('@openzeppelin/hardhat-upgrades'); require('@nomicfoundation/hardhat-network-helpers'); console.log('ok')"`
Expected: prints `ok`. If either errors `Cannot find module`, run `npm install --save-dev @openzeppelin/hardhat-upgrades @nomicfoundation/hardhat-ethers @nomicfoundation/hardhat-network-helpers` and confirm `hardhat.config.js` requires `@openzeppelin/hardhat-upgrades`. (`hardhat-network-helpers` provides `time` used by the timeout/auto-release tests.)

- [ ] **Step 3: Confirm the existing suite compiles and runs (baseline)**

Run: `npx hardhat compile && npx hardhat test test/P2PEscrow.test.js`
Expected: compiles; v1 tests pass. This proves the toolchain is healthy before we add v2.

- [ ] **Step 4: Locate or note the ERC20 mock used by v1 tests**

Run: `grep -rn "MockERC20\|TestERC20\|ERC20Mock" test/ contracts/`
Expected: identifies the existing mock contract name/path. Record it; reuse it in v2 tests instead of creating a new one. (If none exists, Task 1 Step 2 creates `MockERC20`.)

No commit (environment only).

---

## Task 1: Scaffold `P2PEscrowV2.sol` (compiles, deploys, defaults correct)

**Files:**
- Create: `smart-contracts/contracts/P2PEscrowV2.sol`
- Create (if needed): `smart-contracts/contracts/test/MockERC20.sol`
- Test: `smart-contracts/test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write the failing deploy/defaults test**

Create `test/P2PEscrowV2.test.js`:

```javascript
const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

async function deployV2() {
  const [owner, admin, alice, bob, carol, system] = await ethers.getSigners();
  const Factory = await ethers.getContractFactory("P2PEscrowV2");
  const escrow = await upgrades.deployProxy(Factory, [owner.address, [admin.address]], { kind: "uups" });
  await escrow.waitForDeployment();
  await escrow.connect(owner).setSystemWallet(system.address);
  return { escrow, owner, admin, alice, bob, carol, system };
}

async function deployToken(escrow, owner, supplyTo) {
  const ERC20 = await ethers.getContractFactory("MockERC20");
  const token = await ERC20.deploy("Tok", "TOK", 18);
  await token.waitForDeployment();
  for (const acct of supplyTo) await token.mint(acct.address, ethers.parseUnits("1000000", 18));
  await escrow.connect(owner).setTokenAcceptance(await token.getAddress(), true);
  return token;
}

module.exports = { deployV2, deployToken };

describe("P2PEscrowV2 — scaffold", () => {
  it("initializes owner, admin, and economic defaults", async () => {
    const { escrow, owner, admin } = await deployV2();
    expect(await escrow.owner()).to.equal(owner.address);
    expect(await escrow.isAdmin(admin.address)).to.equal(true);
    expect(await escrow.feeBps()).to.equal(50);
    expect(await escrow.paymentWindow()).to.equal(30 * 60);
    expect(await escrow.autoReleaseWindow()).to.equal(0);
    expect(await escrow.disputeFeeBps()).to.equal(0);
    expect(await escrow.trustedForwarder()).to.equal(ethers.ZeroAddress);
    expect(await escrow.permit2()).to.equal(ethers.ZeroAddress);
  });
});
```

- [ ] **Step 2: Create the ERC20 mock if Task 0 Step 4 found none**

Create `contracts/test/MockERC20.sol` only if needed:

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;
import "@openzeppelin/contracts/token/ERC20/ERC20.sol";

contract MockERC20 is ERC20 {
    uint8 private _dec;
    constructor(string memory n, string memory s, uint8 d) ERC20(n, s) { _dec = d; }
    function decimals() public view override returns (uint8) { return _dec; }
    function mint(address to, uint256 amt) external { _mint(to, amt); }
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "scaffold"`
Expected: FAIL — `P2PEscrowV2` artifact not found / cannot compile.

- [ ] **Step 4: Write the contract scaffold**

Create `contracts/P2PEscrowV2.sol`. (Mirrors v1's imports but swaps the non-upgradeable `ReentrancyGuard` for `ReentrancyGuardUpgradeable`, adds a settable-forwarder `_msgSender` override and a storage `__gap`.)

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/ReentrancyGuardUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/cryptography/EIP712Upgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/ContextUpgradeable.sol";
import "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

/// Minimal Permit2 signature-transfer interface (subset we call).
interface IPermit2 {
    struct TokenPermissions { address token; uint256 amount; }
    struct PermitTransferFrom { TokenPermissions permitted; uint256 nonce; uint256 deadline; }
    struct SignatureTransferDetails { address to; uint256 requestedAmount; }
    function permitTransferFrom(
        PermitTransferFrom calldata permit,
        SignatureTransferDetails calldata transferDetails,
        address owner,
        bytes calldata signature
    ) external;
}

/// @title P2PEscrowV2
/// @notice Two-sided pool/JIT escrow. See
///         docs/superpowers/specs/2026-05-29-p2p-escrow-v2-two-sided-design.md
contract P2PEscrowV2 is
    Initializable,
    UUPSUpgradeable,
    OwnableUpgradeable,
    ReentrancyGuardUpgradeable,
    PausableUpgradeable,
    EIP712Upgradeable
{
    using SafeERC20 for IERC20;

    enum Status { None, Pending, Paid, Released, Cancelled, Disputed, Resolved }
    enum Funding { PooledFromMaker, DepositedByTaker }

    struct AdVoucher {
        address seller;
        address token;
        uint256 maxAmount;
        uint256 minPerTrade;
        uint256 maxPerTrade;
        uint256 deadline;
        bytes32 nonce;
    }

    struct Trade {
        address seller;
        address buyer;
        address token;
        uint96  amount;
        uint40  createdAt;
        uint40  autoReleaseAt;
        Status  status;
        Funding funding;
        uint16  feeBps;
        bytes32 voucherHash;
    }

    bytes32 private constant AD_VOUCHER_TYPEHASH = keccak256(
        "AdVoucher(address seller,address token,uint256 maxAmount,uint256 minPerTrade,uint256 maxPerTrade,uint256 deadline,bytes32 nonce)"
    );

    // ── Hard caps / bounds on tunable params ───────────────────────────────
    uint16  public constant MAX_FEE_BPS = 500;            // 5%
    uint16  public constant MAX_DISPUTE_FEE_BPS = 500;    // 5%
    uint40  public constant MIN_PAYMENT_WINDOW = 5 minutes;
    uint40  public constant MAX_PAYMENT_WINDOW = 24 hours;
    uint40  public constant MAX_AUTO_RELEASE_WINDOW = 7 days;

    // ── Storage ────────────────────────────────────────────────────────────
    address public mcgpToken;
    address public systemWallet;

    mapping(address => bool) public acceptedTokens;
    mapping(address => bool) public admins;
    mapping(address => mapping(address => uint256)) public available;
    mapping(address => mapping(address => uint256)) public locked;
    mapping(bytes32 => uint256) public voucherFilled;
    mapping(bytes32 => bool) public voucherCancelled;
    mapping(bytes32 => Trade) public trades;
    mapping(address => uint256) public accountedBalance;

    // Tunable economic params (replace v1's FEE_BPS / TIMEOUT constants).
    uint16 public feeBps;
    uint16 public disputeFeeBps;
    uint40 public paymentWindow;
    uint40 public autoReleaseWindow;

    // Gasless hooks (disabled until set).
    address public trustedForwarder;
    address public permit2;

    // Per-merchant ⚡ auto-release opt-in (default false).
    mapping(address => bool) public autoReleaseOptIn;

    uint256[50] private __gap;

    // ── Events (v1 set, plus v2 additions) ─────────────────────────────────
    event Deposited(address indexed seller, address indexed token, uint256 amount);
    event Withdrawn(address indexed seller, address indexed token, uint256 amount);
    event VoucherCancelled(bytes32 indexed voucherHash, address indexed seller);
    event TradeOpened(bytes32 indexed tradeId, bytes32 indexed voucherHash, address indexed buyer, address seller, address token, uint256 amount);
    event TradeOpenedWithDeposit(bytes32 indexed tradeId, address indexed seller, address indexed buyer, address token, uint256 amount);
    event TradePaid(bytes32 indexed tradeId);
    event TradeReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeAutoReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeCancelled(bytes32 indexed tradeId, uint256 returnedToAvailable);
    event TradeDisputed(bytes32 indexed tradeId, address indexed by);
    event TradeResolved(bytes32 indexed tradeId, bool releasedToBuyer, uint256 amountMoved, uint256 feeToSystem);
    event TokenAcceptanceUpdated(address indexed token, bool accepted);
    event McgpTokenUpdated(address indexed oldToken, address indexed newToken);
    event SystemWalletUpdated(address indexed oldWallet, address indexed newWallet);
    event ExcessSwept(address indexed token, uint256 amount);
    event AdminAdded(address indexed account);
    event AdminRemoved(address indexed account);
    event FeeBpsUpdated(uint16 oldBps, uint16 newBps);
    event DisputeFeeBpsUpdated(uint16 oldBps, uint16 newBps);
    event PaymentWindowUpdated(uint40 oldWindow, uint40 newWindow);
    event AutoReleaseWindowUpdated(uint40 oldWindow, uint40 newWindow);
    event TrustedForwarderUpdated(address indexed oldFwd, address indexed newFwd);
    event Permit2Updated(address indexed oldP, address indexed newP);
    event AutoReleaseOptInUpdated(address indexed seller, bool optedIn);

    modifier onlyAdmin() { require(isAdmin(_msgSender()), "Not admin"); _; }

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() { _disableInitializers(); }

    function initialize(address initialOwner, address[] calldata initialAdmins) external initializer {
        __Ownable_init(initialOwner);
        __Pausable_init();
        __ReentrancyGuard_init();
        __EIP712_init("TSAConnectP2PEscrow", "2");

        feeBps = 50;
        paymentWindow = 30 minutes;
        // autoReleaseWindow, disputeFeeBps, trustedForwarder, permit2 stay zero (disabled).

        for (uint256 i = 0; i < initialAdmins.length; i++) {
            address a = initialAdmins[i];
            require(a != address(0), "Zero address");
            require(!admins[a], "Already admin");
            admins[a] = true;
            emit AdminAdded(a);
        }
    }

    function _authorizeUpgrade(address) internal override onlyOwner {}

    function isAdmin(address account) public view returns (bool) {
        return admins[account] || account == owner();
    }

    // ── EIP-2771 settable forwarder ────────────────────────────────────────
    function _msgSender() internal view override returns (address) {
        if (msg.sender == trustedForwarder && msg.data.length >= 20) {
            return address(bytes20(msg.data[msg.data.length - 20:]));
        }
        return super._msgSender();
    }

    function _msgData() internal view override returns (bytes calldata) {
        if (msg.sender == trustedForwarder && msg.data.length >= 20) {
            return msg.data[:msg.data.length - 20];
        }
        return super._msgData();
    }

    function _contextSuffixLength() internal view override returns (uint256) {
        return (msg.sender == trustedForwarder) ? 20 : super._contextSuffixLength();
    }

    // (deposit/withdraw/voucher/trade/admin functions added in later tasks)

    function pause() external onlyOwner { _pause(); }
    function unpause() external onlyOwner { _unpause(); }

    function setSystemWallet(address _systemWallet) external onlyOwner {
        require(_systemWallet != address(0), "Invalid system wallet");
        address old = systemWallet; systemWallet = _systemWallet;
        emit SystemWalletUpdated(old, _systemWallet);
    }

    function setTokenAcceptance(address token, bool accepted) external onlyOwner {
        require(token != address(0), "Invalid token");
        acceptedTokens[token] = accepted;
        emit TokenAcceptanceUpdated(token, accepted);
    }
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "scaffold"`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add contracts/P2PEscrowV2.sol contracts/test/MockERC20.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): scaffold two-sided escrow contract (storage, init, EIP-2771 forwarder, defaults)"
```

---

## Task 2: Deposit / withdraw (pooled) + accountedBalance + sweepExcess

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write failing tests**

Append to `test/P2PEscrowV2.test.js`:

```javascript
describe("P2PEscrowV2 — deposit/withdraw", () => {
  it("deposit credits available + accountedBalance", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await expect(escrow.connect(alice).deposit(await token.getAddress(), amt))
      .to.emit(escrow, "Deposited");
    const [avail] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(avail).to.equal(amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(amt);
  });

  it("withdraw debits available + accountedBalance", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await escrow.connect(alice).deposit(await token.getAddress(), amt);
    await escrow.connect(alice).withdraw(await token.getAddress(), amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(0);
  });

  it("sweepExcess refuses to touch accounted principal", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await escrow.connect(alice).deposit(await token.getAddress(), amt);
    await expect(escrow.connect(owner).sweepExcess(await token.getAddress(), 1))
      .to.be.revertedWith("Exceeds excess");
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "deposit/withdraw"`
Expected: FAIL — `deposit` is not a function.

- [ ] **Step 3: Implement deposit/withdraw/sweepExcess**

Insert before `pause()` in `P2PEscrowV2.sol`:

```solidity
    function deposit(address token, uint256 amount) external nonReentrant whenNotPaused {
        require(token != address(0), "Invalid token");
        require(acceptedTokens[token], "Token not accepted");
        require(amount > 0, "Amount must be > 0");
        address from = _msgSender();
        IERC20(token).safeTransferFrom(from, address(this), amount);
        available[from][token] += amount;
        accountedBalance[token] += amount;
        emit Deposited(from, token, amount);
    }

    function withdraw(address token, uint256 amount) external nonReentrant {
        require(amount > 0, "Amount must be > 0");
        address acct = _msgSender();
        uint256 avail = available[acct][token];
        require(avail >= amount, "Insufficient available");
        available[acct][token] = avail - amount;
        accountedBalance[token] -= amount;
        IERC20(token).safeTransfer(acct, amount);
        emit Withdrawn(acct, token, amount);
    }

    function balanceOf(address seller, address token) external view returns (uint256 _available, uint256 _locked) {
        return (available[seller][token], locked[seller][token]);
    }

    function sweepExcess(address token, uint256 amount) external onlyOwner {
        require(token != address(0), "Invalid token");
        uint256 contractBal = IERC20(token).balanceOf(address(this));
        uint256 accounted = accountedBalance[token];
        require(contractBal >= accounted, "Accounting corrupt");
        require(amount <= contractBal - accounted, "Exceeds excess");
        IERC20(token).safeTransfer(owner(), amount);
        emit ExcessSwept(token, amount);
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "deposit/withdraw"`
Expected: PASS (3 passing).

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): pooled deposit/withdraw + accountedBalance fence + sweepExcess"
```

---

## Task 3: Voucher hashing + cancelVoucher + a signing helper for tests

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write the failing test + EIP-712 signing helper**

Append to `test/P2PEscrowV2.test.js`:

```javascript
async function signVoucher(escrow, seller, v) {
  const domain = {
    name: "TSAConnectP2PEscrow",
    version: "2",
    chainId: (await ethers.provider.getNetwork()).chainId,
    verifyingContract: await escrow.getAddress(),
  };
  const types = { AdVoucher: [
    { name: "seller", type: "address" }, { name: "token", type: "address" },
    { name: "maxAmount", type: "uint256" }, { name: "minPerTrade", type: "uint256" },
    { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" },
    { name: "nonce", type: "bytes32" },
  ]};
  const sig = await seller.signTypedData(domain, types, v);
  return sig;
}

function makeVoucher(seller, token, overrides = {}) {
  return {
    seller, token,
    maxAmount: ethers.parseUnits("1000", 18),
    minPerTrade: 0,
    maxPerTrade: 0,
    deadline: Math.floor(Date.now() / 1000) + 3600,
    nonce: ethers.hexlify(ethers.randomBytes(32)),
    ...overrides,
  };
}

module.exports.signVoucher = signVoucher;
module.exports.makeVoucher = makeVoucher;

describe("P2PEscrowV2 — vouchers", () => {
  it("hashVoucher matches the off-chain typed-data hash and cancelVoucher works", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const v = makeVoucher(alice.address, await token.getAddress());
    const h = await escrow.hashVoucher(v);
    expect(h.length).to.equal(66); // 0x + 32 bytes
    await expect(escrow.connect(alice).cancelVoucher(v)).to.emit(escrow, "VoucherCancelled").withArgs(h, alice.address);
    expect(await escrow.voucherCancelled(h)).to.equal(true);
  });

  it("only the voucher seller can cancel", async () => {
    const { escrow, owner, alice, bob } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const v = makeVoucher(alice.address, await token.getAddress());
    await expect(escrow.connect(bob).cancelVoucher(v)).to.be.revertedWith("Only voucher seller");
  });
});
```

> Note: `properHex` is a custom matcher; if not present, replace the assertion with `expect(h.length).to.equal(66)`.

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "vouchers"`
Expected: FAIL — `hashVoucher` not a function.

- [ ] **Step 3: Implement hashVoucher + cancelVoucher**

Insert into `P2PEscrowV2.sol` (after the EIP-2771 overrides):

```solidity
    function hashVoucher(AdVoucher calldata v) public view returns (bytes32) {
        return _hashTypedDataV4(keccak256(abi.encode(
            AD_VOUCHER_TYPEHASH, v.seller, v.token, v.maxAmount, v.minPerTrade, v.maxPerTrade, v.deadline, v.nonce
        )));
    }

    function cancelVoucher(AdVoucher calldata v) external {
        require(_msgSender() == v.seller, "Only voucher seller");
        bytes32 h = hashVoucher(v);
        if (!voucherCancelled[h]) { voucherCancelled[h] = true; emit VoucherCancelled(h, v.seller); }
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "vouchers"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): voucher hashing + seller cancelVoucher"
```

---

## Task 4: `openTrade` (sell-ad, pooled) — full guard set + fee snapshot

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write failing tests**

Append:

```javascript
async function setup_pooled(amount = "1000") {
  const ctx = await deployV2();
  const token = await deployToken(ctx.escrow, ctx.owner, [ctx.alice, ctx.bob]);
  const dep = ethers.parseUnits(amount, 18);
  await token.connect(ctx.alice).approve(await ctx.escrow.getAddress(), dep);
  await ctx.escrow.connect(ctx.alice).deposit(await token.getAddress(), dep);
  return { ...ctx, token };
}

describe("P2PEscrowV2 — openTrade (sell-ad/pooled)", () => {
  it("opens, moves available→locked, snapshots feeBps, sets Pending", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("trade-1");
    const amt = ethers.parseUnits("100", 18);
    await expect(escrow.connect(bob).openTrade(tradeId, v, sig, amt)).to.emit(escrow, "TradeOpened");
    const t = await escrow.getTrade(tradeId);
    expect(t.seller).to.equal(alice.address);
    expect(t.buyer).to.equal(bob.address);
    expect(t.status).to.equal(1); // Pending
    expect(t.feeBps_).to.equal(50); // getTrade names the snapshot return `feeBps_`
    const [avail, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(amt);
    expect(avail).to.equal(ethers.parseUnits("900", 18));
  });

  it("rejects a bad signature", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, bob, v); // wrong signer
    await expect(escrow.connect(bob).openTrade(ethers.id("t"), v, sig, ethers.parseUnits("100",18)))
      .to.be.revertedWith("Bad voucher signature");
  });

  it("rejects seller self-buy", async () => {
    const { escrow, alice, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    await expect(escrow.connect(alice).openTrade(ethers.id("t"), v, sig, ethers.parseUnits("100",18)))
      .to.be.revertedWith("Seller cannot self-buy");
  });

  it("rejects exceeding voucher capacity", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress(), { maxAmount: ethers.parseUnits("50",18) });
    const sig = await signVoucher(escrow, alice, v);
    await expect(escrow.connect(bob).openTrade(ethers.id("t"), v, sig, ethers.parseUnits("100",18)))
      .to.be.revertedWith("Exceeds voucher capacity");
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "openTrade"`
Expected: FAIL — `openTrade` not a function.

- [ ] **Step 3: Implement openTrade + internal fee-snapshot helper + getTrade**

Insert into `P2PEscrowV2.sol`:

```solidity
    function _feeBpsForToken(address token) internal view returns (uint16) {
        if (mcgpToken != address(0) && token == mcgpToken) return 0;
        return feeBps;
    }

    function getTrade(bytes32 tradeId) external view returns (
        address seller, address buyer, address token, uint256 amount,
        uint256 createdAt, uint8 status, uint8 funding, uint16 feeBps_, uint256 autoReleaseAt, bytes32 voucherHash
    ) {
        Trade storage t = trades[tradeId];
        return (t.seller, t.buyer, t.token, t.amount, t.createdAt, uint8(t.status), uint8(t.funding), t.feeBps, t.autoReleaseAt, t.voucherHash);
    }

    function openTrade(bytes32 tradeId, AdVoucher calldata v, bytes calldata sig, uint256 amount)
        external nonReentrant whenNotPaused
    {
        require(tradeId != bytes32(0), "Invalid trade id");
        require(trades[tradeId].status == Status.None, "Trade already exists");
        require(amount > 0, "Amount must be > 0");
        require(amount <= type(uint96).max, "Amount overflow");
        address buyer = _msgSender();
        require(v.seller != address(0), "Invalid voucher seller");
        require(v.seller != buyer, "Seller cannot self-buy");
        require(v.token != address(0), "Voucher: token=0");
        require(acceptedTokens[v.token], "Token not accepted");
        require(block.timestamp <= v.deadline, "Voucher expired");
        require(v.maxAmount > 0, "Voucher maxAmount=0");
        require(v.maxPerTrade == 0 || v.maxPerTrade <= v.maxAmount, "Voucher: maxPerTrade > maxAmount");
        require(v.minPerTrade == 0 || v.maxPerTrade == 0 || v.minPerTrade <= v.maxPerTrade, "Voucher: minPerTrade > maxPerTrade");
        require(v.minPerTrade <= v.maxAmount, "Voucher: minPerTrade > maxAmount");
        require(v.minPerTrade == 0 || amount >= v.minPerTrade, "Below voucher min");
        require(v.maxPerTrade == 0 || amount <= v.maxPerTrade, "Above voucher max");

        bytes32 voucherHash = hashVoucher(v);
        require(!voucherCancelled[voucherHash], "Voucher cancelled");
        uint256 filled = voucherFilled[voucherHash];
        require(filled + amount <= v.maxAmount, "Exceeds voucher capacity");
        require(ECDSA.recover(voucherHash, sig) == v.seller, "Bad voucher signature");

        uint256 sellerAvail = available[v.seller][v.token];
        require(sellerAvail >= amount, "Seller balance insufficient");
        available[v.seller][v.token] = sellerAvail - amount;
        locked[v.seller][v.token] += amount;
        voucherFilled[voucherHash] = filled + amount;

        trades[tradeId] = Trade({
            seller: v.seller, buyer: buyer, token: v.token, amount: uint96(amount),
            createdAt: uint40(block.timestamp), autoReleaseAt: 0,
            status: Status.Pending, funding: Funding.PooledFromMaker,
            feeBps: _feeBpsForToken(v.token), voucherHash: voucherHash
        });
        emit TradeOpened(tradeId, voucherHash, buyer, v.seller, v.token, amount);
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "openTrade"`
Expected: PASS (4 passing).

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): pooled openTrade with full voucher guards + fee snapshot"
```

---

## Task 5: `openTradeWithDeposit` (buy-ad, JIT) — pull-from-signer, uint96 cap, self-trade

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write failing tests**

Append:

```javascript
describe("P2PEscrowV2 — openTradeWithDeposit (buy-ad/JIT)", () => {
  it("pulls the user's crypto, locks it under seller=user, buyer=merchant", async () => {
    const { escrow, owner, alice, carol } = await deployV2(); // alice=user(seller), carol=merchant(buyer)
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy-1");
    await expect(escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address))
      .to.emit(escrow, "TradeOpenedWithDeposit");
    const t = await escrow.getTrade(tradeId);
    expect(t.seller).to.equal(alice.address);
    expect(t.buyer).to.equal(carol.address);
    expect(t.funding).to.equal(1); // DepositedByTaker
    expect(t.voucherHash).to.equal(ethers.ZeroHash);
    const [, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(amt);
  });

  it("rejects amount over uint96 max", async () => {
    const { escrow, owner, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const tooBig = (2n ** 96n);
    await expect(escrow.connect(alice).openTradeWithDeposit(ethers.id("x"), await token.getAddress(), tooBig, carol.address))
      .to.be.revertedWith("Amount overflow");
  });

  it("rejects self-trade (merchant == caller)", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("10", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await expect(escrow.connect(alice).openTradeWithDeposit(ethers.id("x"), await token.getAddress(), amt, alice.address))
      .to.be.revertedWith("Self-trade");
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "openTradeWithDeposit"`
Expected: FAIL — function not defined.

- [ ] **Step 3: Implement openTradeWithDeposit**

Insert into `P2PEscrowV2.sol`:

```solidity
    function openTradeWithDeposit(bytes32 tradeId, address token, uint256 amount, address merchant)
        external nonReentrant whenNotPaused
    {
        require(tradeId != bytes32(0), "Invalid trade id");
        require(trades[tradeId].status == Status.None, "Trade already exists");
        require(amount > 0, "Amount must be > 0");
        require(amount <= type(uint96).max, "Amount overflow");
        require(token != address(0), "Invalid token");
        require(acceptedTokens[token], "Token not accepted");
        require(merchant != address(0), "Invalid merchant");
        address user = _msgSender();
        require(merchant != user, "Self-trade");

        IERC20(token).safeTransferFrom(user, address(this), amount); // pull source is always the signer
        locked[user][token] += amount;
        accountedBalance[token] += amount;

        trades[tradeId] = Trade({
            seller: user, buyer: merchant, token: token, amount: uint96(amount),
            createdAt: uint40(block.timestamp), autoReleaseAt: 0,
            status: Status.Pending, funding: Funding.DepositedByTaker,
            feeBps: _feeBpsForToken(token), voucherHash: bytes32(0)
        });
        emit TradeOpenedWithDeposit(tradeId, user, merchant, token, amount);
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "openTradeWithDeposit"`
Expected: PASS (3 passing).

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): JIT openTradeWithDeposit for buy-ads (pull-from-signer, uint96 cap, self-trade guard)"
```

---

## Task 6: `markPaid` (+ conditional autoReleaseAt)

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write failing tests**

Append:

```javascript
describe("P2PEscrowV2 — markPaid", () => {
  it("buyer marks paid; autoReleaseAt stays 0 when auto-release disabled", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await expect(escrow.connect(bob).markPaid(tradeId)).to.emit(escrow, "TradePaid");
    const t = await escrow.getTrade(tradeId);
    expect(t.status).to.equal(2); // Paid
    expect(t.autoReleaseAt).to.equal(0);
  });

  it("non-buyer cannot mark paid", async () => {
    const { escrow, alice, bob, carol, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await expect(escrow.connect(carol).markPaid(tradeId)).to.be.revertedWith("Only buyer");
  });

  it("sets autoReleaseAt when window>0 AND seller opted in", async () => {
    const { escrow, owner, alice, bob, token } = await setup_pooled();
    await escrow.connect(owner).setAutoReleaseWindow(2 * 3600);
    await escrow.connect(alice).setAutoReleaseOptIn(true);
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await escrow.connect(bob).markPaid(tradeId);
    const t = await escrow.getTrade(tradeId);
    expect(t.autoReleaseAt).to.be.greaterThan(0);
  });
});
```

> This test calls `setAutoReleaseWindow` / `setAutoReleaseOptIn` — implemented in Task 11. To keep tasks independently runnable, implement those two setters now (Step 3 includes them) and Task 11 will add the rest + cap tests.

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "markPaid"`
Expected: FAIL — `markPaid`/`setAutoReleaseWindow` not defined.

- [ ] **Step 3: Implement markPaid + the two auto-release setters**

Insert into `P2PEscrowV2.sol`:

```solidity
    function setAutoReleaseWindow(uint40 window) external onlyOwner {
        require(window <= MAX_AUTO_RELEASE_WINDOW, "Window too large");
        uint40 old = autoReleaseWindow; autoReleaseWindow = window;
        emit AutoReleaseWindowUpdated(old, window);
    }

    function setAutoReleaseOptIn(bool optedIn) external {
        autoReleaseOptIn[_msgSender()] = optedIn;
        emit AutoReleaseOptInUpdated(_msgSender(), optedIn);
    }

    function markPaid(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending, "Not pending");
        require(_msgSender() == t.buyer, "Only buyer");
        t.status = Status.Paid;
        if (autoReleaseWindow > 0 && autoReleaseOptIn[t.seller]) {
            t.autoReleaseAt = uint40(block.timestamp + autoReleaseWindow);
        }
        emit TradePaid(tradeId);
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "markPaid"`
Expected: PASS (3 passing).

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): markPaid with conditional optimistic-release arming"
```

---

## Task 7: `release` / `adminRelease` + `_settleRelease` + `_splitFee` (snapshotted)

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write failing tests**

Append:

```javascript
describe("P2PEscrowV2 — release", () => {
  it("seller releases: buyer gets amount-fee, system gets fee, accountedBalance drops", async () => {
    const { escrow, alice, bob, token, system } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await escrow.connect(bob).markPaid(tradeId);
    const fee = amt * 50n / 10000n;
    const before = await token.balanceOf(bob.address);
    await expect(escrow.connect(alice).release(tradeId)).to.emit(escrow, "TradeReleased");
    expect(await token.balanceOf(bob.address)).to.equal(before + (amt - fee));
    expect(await token.balanceOf(system.address)).to.equal(fee);
    const t = await escrow.getTrade(tradeId);
    expect(t.status).to.equal(3); // Released
  });

  it("buy-ad release pays the merchant (buyer) minus fee", async () => {
    const { escrow, owner, alice, carol, system } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    await escrow.connect(carol).markPaid(tradeId);
    const fee = amt * 50n / 10000n;
    await escrow.connect(alice).release(tradeId); // user (seller/fiat-receiver) releases
    expect(await token.balanceOf(carol.address)).to.equal(amt - fee);
    expect(await token.balanceOf(system.address)).to.equal(fee);
  });

  it("non-seller cannot release", async () => {
    const { escrow, alice, bob, carol, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await escrow.connect(bob).markPaid(tradeId);
    await expect(escrow.connect(carol).release(tradeId)).to.be.revertedWith("Only seller");
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "release"`
Expected: FAIL — `release` not defined.

- [ ] **Step 3: Implement release/adminRelease + internals**

Insert into `P2PEscrowV2.sol`:

```solidity
    function release(bytes32 tradeId) external nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending || t.status == Status.Paid, "Not releasable");
        require(_msgSender() == t.seller, "Only seller");
        _settleRelease(tradeId, t, false);
    }

    function adminRelease(bytes32 tradeId) external onlyAdmin nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending || t.status == Status.Paid, "Not releasable");
        _settleRelease(tradeId, t, false);
    }

    function _settleRelease(bytes32 tradeId, Trade storage t, bool auto_) internal {
        uint96 amt = t.amount;
        require(amt > 0, "Empty trade");
        require(locked[t.seller][t.token] >= amt, "Locked invariant");
        (uint256 toBuyer, uint256 fee) = _splitFee(amt, t.feeBps);
        locked[t.seller][t.token] -= amt;
        accountedBalance[t.token] -= amt;
        t.status = Status.Released;
        IERC20(t.token).safeTransfer(t.buyer, toBuyer);
        if (fee > 0) IERC20(t.token).safeTransfer(_systemRecipient(), fee);
        if (auto_) emit TradeAutoReleased(tradeId, t.buyer, toBuyer, fee);
        else emit TradeReleased(tradeId, t.buyer, toBuyer, fee);
    }

    function _splitFee(uint96 amount, uint16 bps) internal pure returns (uint256 toBuyer, uint256 fee) {
        fee = (uint256(amount) * bps) / 10000;
        toBuyer = uint256(amount) - fee;
    }

    function _systemRecipient() internal view returns (address) {
        address sw = systemWallet;
        require(sw != address(0), "System wallet not set");
        return sw;
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "release"`
Expected: PASS (3 passing).

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): release/adminRelease + snapshotted fee split (both flows)"
```

---

## Task 8: `cancelByTimeout` (funding-branched refund + voucher guard)

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write failing tests**

Append:

```javascript
const { time } = require("@nomicfoundation/hardhat-network-helpers");

describe("P2PEscrowV2 — cancelByTimeout", () => {
  it("pooled: returns locked→available and refunds voucher capacity", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await time.increase(31 * 60);
    await expect(escrow.connect(bob).cancelByTimeout(tradeId)).to.emit(escrow, "TradeCancelled");
    const [avail, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(0);
    expect(avail).to.equal(ethers.parseUnits("1000", 18));
    expect(await escrow.voucherFilled(await escrow.hashVoucher(v))).to.equal(0);
  });

  it("JIT: transfers crypto back to the user's wallet and drops accountedBalance", async () => {
    const { escrow, owner, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    const before = await token.balanceOf(alice.address);
    await time.increase(31 * 60);
    await escrow.connect(alice).cancelByTimeout(tradeId);
    expect(await token.balanceOf(alice.address)).to.equal(before + amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(0);
    const [, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(0);
  });

  it("reverts before the window elapses", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await expect(escrow.connect(bob).cancelByTimeout(tradeId)).to.be.revertedWith("Timeout not elapsed");
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "cancelByTimeout"`
Expected: FAIL — function not defined.

- [ ] **Step 3: Implement cancelByTimeout + the shared refund internal**

Insert into `P2PEscrowV2.sol`:

```solidity
    /// Shared refund: pool-credit for PooledFromMaker, transfer-out for DepositedByTaker.
    /// Guards the voucherFilled decrement so buy-ads (voucherHash==0) never underflow.
    function _refund(Trade storage t) internal {
        uint96 amt = t.amount;
        require(amt > 0, "Empty trade");
        require(locked[t.seller][t.token] >= amt, "Locked invariant");
        locked[t.seller][t.token] -= amt;
        if (t.funding == Funding.PooledFromMaker) {
            available[t.seller][t.token] += amt; // stays in-contract; accountedBalance unchanged
            if (t.voucherHash != bytes32(0)) voucherFilled[t.voucherHash] -= amt;
        } else {
            accountedBalance[t.token] -= amt;
            IERC20(t.token).safeTransfer(t.seller, amt); // user has no pool
        }
    }

    function cancelByTimeout(bytes32 tradeId) external nonReentrant {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending, "Not pending");
        require(block.timestamp >= uint256(t.createdAt) + paymentWindow, "Timeout not elapsed");
        t.status = Status.Cancelled;
        _refund(t);
        emit TradeCancelled(tradeId, t.amount);
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "cancelByTimeout"`
Expected: PASS (3 passing).

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): funding-branched cancelByTimeout + shared _refund (guards voucherFilled[0])"
```

---

## Task 9: `raiseDispute` + `adminResolve` (funding-branched + optional dispute fee)

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write failing tests**

Append:

```javascript
describe("P2PEscrowV2 — dispute/resolve", () => {
  it("resolve to buyer pays buyer minus fee (non-custodial)", async () => {
    const { escrow, admin, alice, bob, token, system } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await escrow.connect(bob).markPaid(tradeId);
    await escrow.connect(bob).raiseDispute(tradeId);
    const fee = amt * 50n / 10000n;
    await expect(escrow.connect(admin).adminResolve(tradeId, true)).to.emit(escrow, "TradeResolved");
    expect(await token.balanceOf(bob.address)).to.equal(amt - fee);
    expect(await token.balanceOf(system.address)).to.equal(fee);
  });

  it("resolve to seller (pooled) returns funds to available + refunds voucher capacity", async () => {
    const { escrow, admin, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await escrow.connect(alice).raiseDispute(tradeId);
    await escrow.connect(admin).adminResolve(tradeId, false);
    const [avail, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(0);
    expect(avail).to.equal(ethers.parseUnits("1000", 18));
  });

  it("resolve to seller (JIT) transfers crypto back to the user", async () => {
    const { escrow, owner, admin, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    await escrow.connect(carol).markPaid(tradeId);
    await escrow.connect(alice).raiseDispute(tradeId);
    const before = await token.balanceOf(alice.address);
    await escrow.connect(admin).adminResolve(tradeId, false);
    expect(await token.balanceOf(alice.address)).to.equal(before + amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(0);
  });

  it("non-party cannot raise dispute", async () => {
    const { escrow, alice, bob, carol, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await expect(escrow.connect(carol).raiseDispute(tradeId)).to.be.revertedWith("Not a party");
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "dispute/resolve"`
Expected: FAIL — functions not defined.

- [ ] **Step 3: Implement raiseDispute + adminResolve**

Insert into `P2PEscrowV2.sol`. The dispute fee is applied from the pot before settlement when `disputeFeeBps > 0` (default 0 = no-op; the loser-pays *policy*/amount is finalized in the dispute-v2 sub-project — see spec §6):

```solidity
    function raiseDispute(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending || t.status == Status.Paid, "Not disputable");
        address s = _msgSender();
        require(s == t.buyer || s == t.seller, "Not a party");
        t.status = Status.Disputed;
        emit TradeDisputed(tradeId, s);
    }

    function adminResolve(bytes32 tradeId, bool releaseToBuyer) external onlyAdmin nonReentrant {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Disputed, "Not disputed");
        uint96 amt = t.amount;
        require(amt > 0, "Empty trade");
        require(locked[t.seller][t.token] >= amt, "Locked invariant");

        if (releaseToBuyer) {
            (uint256 toBuyer, uint256 fee) = _splitFee(amt, t.feeBps);
            locked[t.seller][t.token] -= amt;
            accountedBalance[t.token] -= amt;
            t.status = Status.Resolved;
            IERC20(t.token).safeTransfer(t.buyer, toBuyer);
            if (fee > 0) IERC20(t.token).safeTransfer(_systemRecipient(), fee);
            emit TradeResolved(tradeId, true, toBuyer, fee);
        } else {
            t.status = Status.Resolved;
            _refund(t); // funding-branched: pool-credit or transfer-out
            emit TradeResolved(tradeId, false, 0, 0);
        }
    }
```

> Note: `disputeFeeBps` storage + setter exist (Task 11); the *application* of a loser-pays fee is deliberately deferred to the dispute-v2 sub-project per spec §6. This contract keeps `disputeFeeBps` at 0 so resolution behaves exactly as v1 until that policy lands. (If the audit prefers the hook be exercised here, add a pot-deduction in both branches gated on `disputeFeeBps > 0` and a corresponding test.)

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "dispute/resolve"`
Expected: PASS (4 passing).

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): raiseDispute + non-custodial adminResolve (funding-branched refund)"
```

---

## Task 10: `finalizeRelease` (optimistic auto-release)

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write failing tests**

Append:

```javascript
describe("P2PEscrowV2 — finalizeRelease (optimistic)", () => {
  async function armedTrade() {
    const ctx = await setup_pooled();
    await ctx.escrow.connect(ctx.owner).setAutoReleaseWindow(2 * 3600);
    await ctx.escrow.connect(ctx.alice).setAutoReleaseOptIn(true);
    const v = makeVoucher(ctx.alice.address, await ctx.token.getAddress());
    const sig = await signVoucher(ctx.escrow, ctx.alice, v);
    const tradeId = ethers.id("auto");
    await ctx.escrow.connect(ctx.bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await ctx.escrow.connect(ctx.bob).markPaid(tradeId);
    return { ...ctx, tradeId };
  }

  it("reverts when auto-release disabled (window 0 ⇒ autoReleaseAt 0)", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await escrow.connect(bob).markPaid(tradeId);
    await expect(escrow.connect(bob).finalizeRelease(tradeId)).to.be.revertedWith("Auto-release not armed");
  });

  it("reverts before the window, succeeds (to buyer) after", async () => {
    const { escrow, bob, token, system, tradeId } = await armedTrade();
    await expect(escrow.connect(bob).finalizeRelease(tradeId)).to.be.revertedWith("Window not elapsed");
    await time.increase(2 * 3600 + 1);
    const amt = ethers.parseUnits("100", 18); const fee = amt * 50n / 10000n;
    await expect(escrow.connect(bob).finalizeRelease(tradeId)).to.emit(escrow, "TradeAutoReleased");
    expect(await token.balanceOf(bob.address)).to.equal(amt - fee);
  });

  it("a dispute before finalize blocks auto-release", async () => {
    const { escrow, alice, bob, tradeId } = await armedTrade();
    await escrow.connect(alice).raiseDispute(tradeId);
    await time.increase(2 * 3600 + 1);
    await expect(escrow.connect(bob).finalizeRelease(tradeId)).to.be.revertedWith("Not paid");
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "finalizeRelease"`
Expected: FAIL — function not defined.

- [ ] **Step 3: Implement finalizeRelease**

Insert into `P2PEscrowV2.sol`:

```solidity
    /// Anyone may finalize after the window; destination is fixed (buyer), so caller is irrelevant.
    function finalizeRelease(bytes32 tradeId) external nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Paid, "Not paid");
        require(t.autoReleaseAt != 0, "Auto-release not armed");
        require(block.timestamp >= t.autoReleaseAt, "Window not elapsed");
        _settleRelease(tradeId, t, true);
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "finalizeRelease"`
Expected: PASS (3 passing).

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): finalizeRelease optimistic auto-release (armed-only, dispute-stoppable)"
```

---

## Task 11: Owner setters, caps, and admin management

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write failing tests**

Append:

```javascript
describe("P2PEscrowV2 — setters & caps", () => {
  it("setFeeBps enforces MAX_FEE_BPS and does not affect in-flight trades", async () => {
    const { escrow, owner, alice, bob, token } = await setup_pooled();
    await expect(escrow.connect(owner).setFeeBps(501)).to.be.revertedWith("Fee too high");
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt); // snapshots 50
    await escrow.connect(owner).setFeeBps(200);                // change after open
    await escrow.connect(bob).markPaid(tradeId);
    const fee = amt * 50n / 10000n;                            // still 0.5%
    const before = await token.balanceOf(bob.address);
    await escrow.connect(alice).release(tradeId);
    expect(await token.balanceOf(bob.address)).to.equal(before + (amt - fee));
  });

  it("setPaymentWindow enforces bounds", async () => {
    const { escrow, owner } = await deployV2();
    await expect(escrow.connect(owner).setPaymentWindow(60)).to.be.revertedWith("Window out of bounds");      // < 5m
    await expect(escrow.connect(owner).setPaymentWindow(25 * 3600)).to.be.revertedWith("Window out of bounds"); // > 24h
    await escrow.connect(owner).setPaymentWindow(3600);
    expect(await escrow.paymentWindow()).to.equal(3600);
  });

  it("MCGP trades snapshot a zero fee", async () => {
    const { escrow, owner, alice, bob } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    await escrow.connect(owner).setMcgpToken(await token.getAddress());
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await escrow.connect(alice).deposit(await token.getAddress(), amt);
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    const t = await escrow.getTrade(tradeId);
    expect(t.feeBps_).to.equal(0);
  });

  it("addAdmin/removeAdmin gated to owner", async () => {
    const { escrow, owner, carol } = await deployV2();
    await escrow.connect(owner).addAdmin(carol.address);
    expect(await escrow.isAdmin(carol.address)).to.equal(true);
    await escrow.connect(owner).removeAdmin(carol.address);
    expect(await escrow.isAdmin(carol.address)).to.equal(false);
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "setters & caps"`
Expected: FAIL — `setFeeBps`/`setPaymentWindow`/`setMcgpToken`/`addAdmin` not defined.

- [ ] **Step 3: Implement remaining setters + admin management**

Insert into `P2PEscrowV2.sol`:

```solidity
    function setFeeBps(uint16 bps) external onlyOwner {
        require(bps <= MAX_FEE_BPS, "Fee too high");
        uint16 old = feeBps; feeBps = bps; emit FeeBpsUpdated(old, bps);
    }

    function setDisputeFeeBps(uint16 bps) external onlyOwner {
        require(bps <= MAX_DISPUTE_FEE_BPS, "Fee too high");
        uint16 old = disputeFeeBps; disputeFeeBps = bps; emit DisputeFeeBpsUpdated(old, bps);
    }

    function setPaymentWindow(uint40 window) external onlyOwner {
        require(window >= MIN_PAYMENT_WINDOW && window <= MAX_PAYMENT_WINDOW, "Window out of bounds");
        uint40 old = paymentWindow; paymentWindow = window; emit PaymentWindowUpdated(old, window);
    }

    function setTrustedForwarder(address fwd) external onlyOwner {
        address old = trustedForwarder; trustedForwarder = fwd; emit TrustedForwarderUpdated(old, fwd);
    }

    function setPermit2(address p) external onlyOwner {
        address old = permit2; permit2 = p; emit Permit2Updated(old, p);
    }

    function setMcgpToken(address _mcgpToken) external onlyOwner {
        require(_mcgpToken != address(0), "Invalid MCGP address");
        require(acceptedTokens[_mcgpToken], "MCGP must be accepted first");
        address old = mcgpToken; mcgpToken = _mcgpToken; emit McgpTokenUpdated(old, _mcgpToken);
    }

    function addAdmin(address account) external onlyOwner {
        require(account != address(0), "Zero address");
        require(!admins[account], "Already admin");
        admins[account] = true; emit AdminAdded(account);
    }

    function removeAdmin(address account) external onlyOwner {
        require(admins[account], "Not admin");
        admins[account] = false; emit AdminRemoved(account);
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "setters & caps"`
Expected: PASS (4 passing).

- [ ] **Step 5: Commit**

```bash
git add contracts/P2PEscrowV2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): capped owner setters (fee/window/forwarder/permit2/mcgp) + admin mgmt"
```

---

## Task 12: EIP-2771 forwarded `_msgSender` + Permit2 deposit/open variants

**Files:**
- Modify: `contracts/P2PEscrowV2.sol`
- Create: `contracts/test/MockForwarder.sol`, `contracts/test/MockPermit2.sol`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write the mocks**

Create `contracts/test/MockForwarder.sol`:

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// Minimal EIP-2771 forwarder: appends `from` (20 bytes) to calldata and relays.
contract MockForwarder {
    function relay(address target, bytes calldata data, address from) external returns (bool ok, bytes memory ret) {
        (ok, ret) = target.call(abi.encodePacked(data, from));
        require(ok, "relay failed");
    }
}
```

Create `contracts/test/MockPermit2.sol`:

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";

/// Minimal Permit2 stand-in: ignores the signature and just does a transferFrom
/// (the user must have approved THIS mock). Exercises the contract's Permit2 path.
contract MockPermit2 {
    struct TokenPermissions { address token; uint256 amount; }
    struct PermitTransferFrom { TokenPermissions permitted; uint256 nonce; uint256 deadline; }
    struct SignatureTransferDetails { address to; uint256 requestedAmount; }
    function permitTransferFrom(
        PermitTransferFrom calldata permit,
        SignatureTransferDetails calldata d,
        address owner,
        bytes calldata
    ) external {
        IERC20(permit.permitted.token).transferFrom(owner, d.to, d.requestedAmount);
    }
}
```

- [ ] **Step 2: Write failing tests**

Append:

```javascript
describe("P2PEscrowV2 — EIP-2771 + Permit2", () => {
  it("a forwarded deposit credits the appended signer, not the forwarder", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const Fwd = await ethers.getContractFactory("MockForwarder");
    const fwd = await Fwd.deploy(); await fwd.waitForDeployment();
    await escrow.connect(owner).setTrustedForwarder(await fwd.getAddress());
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const data = escrow.interface.encodeFunctionData("deposit", [await token.getAddress(), amt]);
    await fwd.relay(await escrow.getAddress(), data, alice.address);
    const [avail] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(avail).to.equal(amt); // credited alice (the appended signer), not the forwarder
  });

  it("depositWithPermit2 pulls from the signer via Permit2", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const P2 = await ethers.getContractFactory("MockPermit2");
    const p2 = await P2.deploy(); await p2.waitForDeployment();
    await escrow.connect(owner).setPermit2(await p2.getAddress());
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await p2.getAddress(), amt); // approve Permit2, not escrow
    const permit = { permitted: { token: await token.getAddress(), amount: amt }, nonce: 1, deadline: Math.floor(Date.now()/1000)+3600 };
    await escrow.connect(alice).depositWithPermit2(permit, "0x");
    const [avail] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(avail).to.equal(amt);
  });
});
```

- [ ] **Step 3: Run to verify failure**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "EIP-2771 \\+ Permit2"`
Expected: FAIL — `depositWithPermit2` not defined (forwarded deposit may already pass from Task 1's override — that's fine).

- [ ] **Step 4: Implement the Permit2 variants**

Insert into `P2PEscrowV2.sol`:

```solidity
    function depositWithPermit2(IPermit2.PermitTransferFrom calldata permit, bytes calldata signature)
        external nonReentrant whenNotPaused
    {
        require(permit2 != address(0), "Permit2 not set");
        address token = permit.permitted.token;
        require(acceptedTokens[token], "Token not accepted");
        uint256 amount = permit.permitted.amount;
        require(amount > 0, "Amount must be > 0");
        address from = _msgSender();
        IPermit2(permit2).permitTransferFrom(
            permit,
            IPermit2.SignatureTransferDetails({ to: address(this), requestedAmount: amount }),
            from, signature
        );
        available[from][token] += amount;
        accountedBalance[token] += amount;
        emit Deposited(from, token, amount);
    }

    function openTradeWithDepositPermit2(
        bytes32 tradeId, address merchant,
        IPermit2.PermitTransferFrom calldata permit, bytes calldata signature
    ) external nonReentrant whenNotPaused {
        require(permit2 != address(0), "Permit2 not set");
        require(tradeId != bytes32(0), "Invalid trade id");
        require(trades[tradeId].status == Status.None, "Trade already exists");
        address token = permit.permitted.token;
        uint256 amount = permit.permitted.amount;
        require(amount > 0 && amount <= type(uint96).max, "Bad amount");
        require(acceptedTokens[token], "Token not accepted");
        require(merchant != address(0), "Invalid merchant");
        address user = _msgSender();
        require(merchant != user, "Self-trade");

        IPermit2(permit2).permitTransferFrom(
            permit,
            IPermit2.SignatureTransferDetails({ to: address(this), requestedAmount: amount }),
            user, signature
        );
        locked[user][token] += amount;
        accountedBalance[token] += amount;
        trades[tradeId] = Trade({
            seller: user, buyer: merchant, token: token, amount: uint96(amount),
            createdAt: uint40(block.timestamp), autoReleaseAt: 0,
            status: Status.Pending, funding: Funding.DepositedByTaker,
            feeBps: _feeBpsForToken(token), voucherHash: bytes32(0)
        });
        emit TradeOpenedWithDeposit(tradeId, user, merchant, token, amount);
    }
```

- [ ] **Step 5: Run to verify pass**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "EIP-2771 \\+ Permit2"`
Expected: PASS (2 passing).

- [ ] **Step 6: Commit**

```bash
git add contracts/P2PEscrowV2.sol contracts/test/MockForwarder.sol contracts/test/MockPermit2.sol test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): Permit2 deposit/open variants + EIP-2771 forwarded-sender coverage"
```

---

## Task 13: Pause-guard coverage + full-suite + deploy script

**Files:**
- Create: `scripts/deploy-p2p-escrow-v2.js`
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write pause-guard tests**

Append:

```javascript
describe("P2PEscrowV2 — pause semantics", () => {
  it("blocks settlement entrypoints when paused, allows recovery", async () => {
    const { escrow, owner, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await escrow.connect(owner).pause();
    await expect(escrow.connect(bob).markPaid(tradeId)).to.be.revertedWithCustomError(escrow, "EnforcedPause");
    // withdraw + cancelByTimeout remain available during pause:
    await time.increase(31 * 60);
    await expect(escrow.connect(bob).cancelByTimeout(tradeId)).to.emit(escrow, "TradeCancelled");
    await expect(escrow.connect(alice).withdraw(await token.getAddress(), ethers.parseUnits("1",18))).to.emit(escrow, "Withdrawn");
  });
});
```

- [ ] **Step 2: Run to verify it passes (guards already in place from earlier tasks)**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "pause semantics"`
Expected: PASS. If `markPaid` does NOT revert when paused, add `whenNotPaused` to it (Task 6) and re-run.

- [ ] **Step 3: Write the deploy script**

Create `scripts/deploy-p2p-escrow-v2.js`:

```javascript
const { ethers, upgrades } = require("hardhat");

async function main() {
  const owner = process.env.OWNER_ADDRESS;
  const admins = (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean);
  const systemWallet = process.env.SYSTEM_WALLET_ADDRESS;
  if (!owner || !systemWallet) throw new Error("Set OWNER_ADDRESS and SYSTEM_WALLET_ADDRESS");

  const Factory = await ethers.getContractFactory("P2PEscrowV2");
  const escrow = await upgrades.deployProxy(Factory, [owner, admins], { kind: "uups" });
  await escrow.waitForDeployment();
  const addr = await escrow.getAddress();
  console.log("P2PEscrowV2 proxy:", addr);

  if (process.env.CHECK_ONLY === "1") { console.log("CHECK_ONLY — skipping config txs"); return; }
  await (await escrow.setSystemWallet(systemWallet)).wait();
  for (const t of (process.env.ACCEPTED_TOKENS || "").split(",").filter(Boolean)) {
    await (await escrow.setTokenAcceptance(t, true)).wait();
    console.log("accepted:", t);
  }
  if (process.env.MCGP_TOKEN_ADDRESS) await (await escrow.setMcgpToken(process.env.MCGP_TOKEN_ADDRESS)).wait();
  console.log("Done. Repoint sonicPresets[*].p2pEscrow to", addr, "in the migration sub-project.");
}

main().catch((e) => { console.error(e); process.exit(1); });
```

- [ ] **Step 4: Dry-run the deploy script against the local network**

Run: `CHECK_ONLY=1 OWNER_ADDRESS=0x0000000000000000000000000000000000000001 SYSTEM_WALLET_ADDRESS=0x0000000000000000000000000000000000000002 npx hardhat run scripts/deploy-p2p-escrow-v2.js`
Expected: prints a proxy address and "CHECK_ONLY — skipping config txs" with no revert.

- [ ] **Step 5: Run the FULL suite + compile clean**

Run: `npx hardhat compile && npx hardhat test test/P2PEscrowV2.test.js`
Expected: all describe-blocks PASS; no compiler warnings about the contract (storage-gap/initializer warnings investigated).

- [ ] **Step 6: Commit**

```bash
git add scripts/deploy-p2p-escrow-v2.js test/P2PEscrowV2.test.js
git commit -m "feat(p2p-v2): pause-guard coverage, full suite green, UUPS deploy script (CHECK_ONLY dry-run)"
```

---

## Task 14: UUPS upgrade-safety test + handoff notes

**Files:**
- Test: `test/P2PEscrowV2.test.js`

- [ ] **Step 1: Write an upgrade-safety test**

Append:

```javascript
describe("P2PEscrowV2 — upgradeability", () => {
  it("upgrades behind the proxy preserving balances (storage-layout safe)", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await escrow.connect(alice).deposit(await token.getAddress(), amt);

    const V2b = await ethers.getContractFactory("P2PEscrowV2"); // same impl re-validates layout
    const upgraded = await upgrades.upgradeProxy(await escrow.getAddress(), V2b);
    const [avail] = await upgraded.balanceOf(alice.address, await token.getAddress());
    expect(avail).to.equal(amt); // balance survived the upgrade
  });
});
```

- [ ] **Step 2: Run it**

Run: `npx hardhat test test/P2PEscrowV2.test.js --grep "upgradeability"`
Expected: PASS. The OZ upgrades plugin also validates the storage layout / `__gap` and will throw if the layout is unsafe.

- [ ] **Step 3: Commit**

```bash
git add test/P2PEscrowV2.test.js
git commit -m "test(p2p-v2): UUPS upgrade-safety (balances preserved, layout validated)"
```

- [ ] **Step 4: Update the spec status + leave handoff notes**

Add a short note to the spec's status line that the contract is implemented + suite green, and that the **re-audit** and the follow-on sub-projects (relayer, keeper, dispute-v2 off-chain, backend, app, cutover) remain. (Do not run the audit — that's an external step.)

```bash
git add docs/superpowers/specs/2026-05-29-p2p-escrow-v2-two-sided-design.md
git commit -m "docs(p2p-v2): mark contract implemented; flag re-audit + follow-on sub-projects"
```

---

## Out of scope (follow-on sub-projects — do NOT build here)
1. Relayer service + Sonic FeeM registration + Permit2 client signing flows.
2. Auto-release keeper job + per-merchant ⚡ opt-in UX.
3. Dispute-v2 off-chain (evidence, wizard, tsa-admin signing, finalized dispute-fee policy/amount).
4. Backend integration (role-based lifecycle, buy-ad model + `Funding` column on `p2p_trades`, tx-building, reconciler updates).
5. App (sell-side UI, two-sided browse, Permit2/meta-tx signing).
6. Migration/cutover (repoint `sonicPresets[*].p2pEscrow`, deprecate old proxy).

## Open prerequisites to confirm during/after build
- Permit2 address on Sonic (Permit2 paths stay dormant until `setPermit2`; `transferFrom`/direct paths work regardless).
- EIP-2771 forwarder choice (dormant until `setTrustedForwarder`).
- Re-audit the full contract before mainnet.
- tsa-admin wallet signing before disputes can resolve end-to-end on mainnet.
