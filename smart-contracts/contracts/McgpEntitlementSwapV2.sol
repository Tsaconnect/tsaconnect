// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";

/**
 * @title McgpEntitlementSwapV2  ("Swap Contract B", gasless successor to V1)
 * @notice MCGP⇄USDC swap that differentiates holders by a per-wallet ENTITLEMENT
 *         LEDGER rather than by the (fungible, immutable) token. Premium is split
 *         by PROVENANCE so the NGN "Instant Transfer" (IT) path can credit
 *         spendable MCGP without ever creating a treasury-drain.
 *
 *   LEGACY  — pre-launch holders; `legacyEntitlement` seeded from a snapshot.
 *             REDEEM-ONLY via `sellLegacy` (USDC at the legacy price, capped).
 *
 *   PREMIUM (two provenance buckets, both spendable via `spend`):
 *     • premiumBought   — minted by user-signed `buyPremium` (USDC in). SELF-
 *                         EXITABLE: `withdrawPremiumToWallet` / `sellPremium`.
 *     • premiumCredited — minted by operator `creditPremium` after an off-chain
 *                         NGN payment (IT). SPEND-ONLY: it has NO on-chain cash-
 *                         out; its only exit is an operator-driven NGN payout
 *                         (`debitPremium` → `settleDebit`).
 *
 * @dev Invariants (unchanged from V1):
 *   - accountedMcgp == Σ(premiumBought + premiumCredited + pendingSettle), and
 *     mcgpToken.balanceOf(this) >= accountedMcgp at all times.
 *   - premiumBought is the ONLY bucket `withdrawPremiumToWallet`/`sellPremium`
 *     can draw from.
 *   - CHANGED IN V2: an operator CAN reach premiumBought, but only via
 *     `debitSpendable`, which takes an explicit per-call `maxFromBought`.
 *     `debitPremium` stays credited-only and is byte-for-byte V1 in effect, so
 *     existing callers (IT sell) cannot touch self-exitable value even if their
 *     own balance check is removed. Bills opt in; nothing else does.
 *     The residual cost is real and should not be glossed: a COMPROMISED operator
 *     simply passes a large maxFromBought, so the opt-in bounds our own code, not
 *     a stolen key. What bounds a stolen key is the per-tx cap, the rolling debit
 *     window, and a guardian pause (settleDebit is whenNotPaused; reverseDebit
 *     stays open so users can still be made whole).
 *   - `spend` debits credited-first then bought and credits the recipient the
 *     SAME bucket type, so credited value can never become self-exitable.
 *
 * @dev CHANGES FROM V1 — the security model differs, read this before assuming
 *      V1's guarantees carry over:
 *
 *   1. UPGRADEABLE (UUPS). V1's header advertised "Non-upgradeable", and that was
 *      load-bearing: it meant no code path laundering credited→bought could ever
 *      be ADDED. Here `_authorizeUpgrade` is `onlyOwner`, so that guarantee is
 *      only as strong as the owner key. This was an explicit product decision
 *      (2026-07-30) — the owner is a single wallet, not a multisig. Revisit when
 *      backed balances get material.
 *   2. ERC-2771 META-TRANSACTIONS. User-facing entry points resolve the caller via
 *      `_msgSender()` so a relayer can pay gas. Owner, guardian and operator
 *      authorization deliberately keep RAW `msg.sender` — a trusted forwarder must
 *      never be able to impersonate a privileged role.
 *   3. SPLIT REF NAMESPACES. V1 shared one `usedRef` mapping between `spend` and
 *      `creditPremium`, so anyone able to predict a pending credit ref could burn
 *      it with a 1-wei spend and permanently block that payment. Gasless relaying
 *      would have made that free. Now `usedSpendRef` and `usedCreditRef` are
 *      separate.
 *   4. CREDIT CAPS ARE MANDATORY. In V1 a zero cap meant "unlimited", so a fresh
 *      deploy silently shipped with the operator rate-limits OFF. Here
 *      `maxCreditPerTx`, `creditWindow` and `creditWindowCap` must all be non-zero,
 *      at `initialize` and at every `setCreditCaps`.
 *   5. `renounceOwnership` DISABLED — on a UUPS proxy it would permanently brick
 *      upgradeability.
 */
contract McgpEntitlementSwapV2 is
    Initializable,
    OwnableUpgradeable,
    PausableUpgradeable,
    UUPSUpgradeable
{
    using SafeERC20 for IERC20;

    IERC20 public mcgpToken;
    IERC20 public usdcToken;

    // OZ v5 removed ReentrancyGuardUpgradeable; implement the slot directly
    // (same approach as P2PEscrowV3 in this repo).
    error Reentrant();
    uint256 private constant _NOT_ENTERED = 1;
    uint256 private constant _ENTERED = 2;
    uint256 private _reentrancyStatus;

    modifier nonReentrant() {
        if (_reentrancyStatus == _ENTERED) revert Reentrant();
        _reentrancyStatus = _ENTERED;
        _;
        _reentrancyStatus = _NOT_ENTERED;
    }

    // ───────────────────────────── Pricing ─────────────────────────────
    // Price is USDC (6 decimals) per 1 MCGP (18 decimals): usdc = mcgp * price / 1e18.
    //
    // NUMBERING IS FROZEN. The backend passes a raw uint8 (`MCGPSwapService.Quote(kind uint8,…)`),
    // so renumbering silently returns the wrong price. Append only.
    enum PriceKind { PremiumBuy, PremiumSell, LegacySell }

    struct PricePhase {
        uint256 price; // USDC (6dp) per MCGP (18dp)
        string name;
    }

    mapping(uint8 => PricePhase[]) private _phases;
    mapping(uint8 => uint256) public activePhase;
    uint256 public constant MAX_PRICE = 1e12; // $1,000,000 per MCGP (6dp)

    // ──────────────────────── Entitlement ledger ───────────────────────
    mapping(address => uint256) public legacyEntitlement;
    mapping(address => uint256) public legacyRedeemed;

    mapping(address => uint256) public premiumBought;    // USDC-bought; self-exitable
    mapping(address => uint256) public premiumCredited;  // operator/NGN; spend-only
    mapping(address => uint256) public pendingSettle;    // debited, awaiting NGN payout

    uint256 public accountedMcgp;                 // == Σ(bought + credited + pendingSettle)
    mapping(bytes32 => bool) public usedSpendRef; // idempotency for `spend`
    mapping(bytes32 => bool) public usedCreditRef; // idempotency for `creditPremium`

    enum DebitState { None, Pending, Resolved }
    struct Debit { address user; uint256 amount; DebitState state; }
    mapping(bytes32 => Debit) public debits; // debitRef => Debit

    // ──────────────────────── Operator / guardian ──────────────────────
    mapping(address => bool) public operators;
    mapping(address => bool) public guardians;

    // Operator-credit safety caps. Unlike V1, zero is NOT "disabled" — see change 4.
    uint256 public minCredit;        // reject dust credits (0 allowed: no dust floor)
    uint256 public maxCreditPerTx;   // per-call ceiling  (must be > 0)
    uint256 public creditWindow;     // rolling window length in seconds (must be > 0)
    uint256 public creditWindowCap;  // max credited per window (must be > 0)
    uint256 public windowStart;
    uint256 public creditedInWindow;

    // ─────────────────────── Gasless / fees (V2) ───────────────────────
    /// @notice EIP-2771 forwarder. Zero disables meta-transactions entirely.
    address public trustedForwarder;
    /// @notice FeeManager for live on-chain rate reads. Zero = fees stay off-chain
    ///         (deployed inert, so day-one behaviour matches V1 byte-for-byte).
    address public feeManager;

    // ───────────────── Operator debit (bills / NGN sell) ──────────────
    /// @notice Per-call ceiling on `debitPremium`. Bounds a compromised operator
    ///         key the way `maxCreditPerTx` bounds crediting. Must be non-zero —
    ///         see the note on the credit caps.
    uint256 public maxDebitPerTx;
    /// @notice Rolling-window ceiling on debits, mirroring the credit window.
    ///         A per-tx cap alone bounds nothing: a compromised operator just
    ///         issues many debits under it. This is what actually limits how much
    ///         a stolen key can move before a guardian pauses.
    uint256 public debitWindow;     // seconds (must be > 0)
    uint256 public debitWindowCap;  // max debited per window (must be > 0)
    uint256 public debitWindowStart;
    uint256 public debitedInWindow;

    /// @dev How much of a debit came from `premiumBought` rather than
    ///      `premiumCredited`. Recorded because `reverseDebit` MUST return value
    ///      to the bucket it came from: restoring a bought-funded debit as
    ///      credited would silently strip the user's ability to withdraw or sell
    ///      it, turning a failed payment into a downgrade.
    mapping(bytes32 => uint256) private _debitFromBought;

    // ──────────────── One-time migration from the V1 proxy ─────────────
    /// @dev V2 is a SEPARATE proxy from V1 (0xF27F…8E52), so it starts with an
    ///      empty ledger. `buyPremium` is the only writer of `premiumBought`, and
    ///      it charges the user USDC — so without an admin path, moving a user's
    ///      already-paid-for bought premium across would mean charging them twice.
    ///      `migratePremium` is that path, and it is deliberately sealable:
    ///      `closeMigration` permanently revokes the owner's ability to conjure
    ///      entitlement, restoring the invariant that bought premium can only
    ///      come from a real USDC payment.
    mapping(address => bool) public premiumMigrated;
    bool public migrationClosed;

    /// @dev Storage headroom for future upgrades. Shrunk by exactly the number of
    ///      slots appended above it (50 → 44: maxDebitPerTx, debitWindow,
    ///      debitWindowCap, debitWindowStart, debitedInWindow, _debitFromBought;
    ///      44 → 42: premiumMigrated, migrationClosed).
    uint256[42] private __gap;

    // ───────────────────────────── Events ──────────────────────────────
    // Signatures are IDENTICAL to V1 — the backend decodes these by topic0.
    event LegacySeeded(address indexed user, uint256 entitlement);
    event LegacyRedeemed(address indexed user, uint256 mcgpAmount, uint256 usdcAmount);
    event PremiumBought(address indexed buyer, uint256 mcgpAmount, uint256 usdcAmount);
    event PremiumSold(address indexed seller, uint256 mcgpAmount, uint256 usdcAmount);
    event PremiumSpent(address indexed from, address indexed to, uint256 mcgpAmount, bytes32 indexed ref);
    event PremiumWithdrawn(address indexed user, uint256 mcgpAmount);
    event PremiumCredited(address indexed user, uint256 mcgpAmount, bytes32 indexed ref);
    event PremiumDebitPending(address indexed user, uint256 mcgpAmount, bytes32 indexed ref);
    event PremiumDebitSettled(address indexed user, uint256 mcgpAmount, bytes32 indexed ref);
    event PremiumDebitReversed(address indexed user, uint256 mcgpAmount, bytes32 indexed ref);
    event OperatorAdded(address indexed operator);
    event OperatorRemoved(address indexed operator);
    event GuardianAdded(address indexed guardian);
    event GuardianRemoved(address indexed guardian);
    event CreditCapsUpdated(uint256 minCredit, uint256 maxCreditPerTx, uint256 creditWindow, uint256 creditWindowCap);
    event DebitCapsUpdated(uint256 maxDebitPerTx, uint256 debitWindow, uint256 debitWindowCap);
    event PhaseAdded(uint8 indexed kind, uint256 indexed index, uint256 price, string name);
    event PhaseActivated(uint8 indexed kind, uint256 indexed index);
    event PhasePriceUpdated(uint8 indexed kind, uint256 indexed index, uint256 newPrice);
    event Funded(address indexed token, uint256 amount);
    event Withdrawn(address indexed token, address indexed to, uint256 amount);
    // V2-only
    event TrustedForwarderUpdated(address indexed previous, address indexed current);
    event FeeManagerUpdated(address indexed previous, address indexed current);
    event PremiumMigrated(address indexed user, uint256 bought, uint256 credited);
    event MigrationClosed();

    // Operator/guardian checks use RAW msg.sender: a relayer must never assume a role.
    modifier onlyOperator() {
        require(operators[msg.sender], "Not operator");
        _;
    }
    modifier onlyOwnerOrGuardian() {
        require(msg.sender == owner() || guardians[msg.sender], "Not owner/guardian");
        _;
    }

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(
        address initialOwner,
        address _mcgpToken,
        address _usdcToken,
        uint256 _minCredit,
        uint256 _maxCreditPerTx,
        uint256 _creditWindow,
        uint256 _creditWindowCap,
        uint256 _maxDebitPerTx,
        uint256 _debitWindow,
        uint256 _debitWindowCap
    ) external initializer {
        require(_mcgpToken != address(0), "Invalid MCGP address");
        require(_usdcToken != address(0), "Invalid USDC address");

        // OZ v5 removed __UUPSUpgradeable_init (it was a no-op).
        __Ownable_init(initialOwner);
        __Pausable_init();
        _reentrancyStatus = _NOT_ENTERED;

        mcgpToken = IERC20(_mcgpToken);
        usdcToken = IERC20(_usdcToken);

        _setCreditCaps(_minCredit, _maxCreditPerTx, _creditWindow, _creditWindowCap);
        _setDebitCaps(_maxDebitPerTx, _debitWindow, _debitWindowCap);
        // trustedForwarder and feeManager stay zero: deployed dark, flipped later.
    }

    /// @notice One-shot re-initializer for proxies deployed before the operator
    ///         debit path existed. `upgradeProxy` does NOT re-run `initialize`, so
    ///         the freshly-added `maxDebitPerTx` slot would otherwise read zero —
    ///         and zero would make every `debitPremium` revert on the cap check,
    ///         which fails closed but is still wrong.
    function initializeDebitCaps(uint256 perTx, uint256 window, uint256 windowCap)
        external
        reinitializer(2)
    {
        _setDebitCaps(perTx, window, windowCap);
    }

    function _authorizeUpgrade(address) internal override onlyOwner {}

    /// @dev Owner authorization must NOT honor the EIP-2771 forwarder — a trusted
    ///      forwarder must never be able to impersonate the owner. Use the real
    ///      msg.sender instead of _msgSender().
    function _checkOwner() internal view override {
        if (owner() != msg.sender) {
            revert OwnableUnauthorizedAccount(msg.sender);
        }
    }

    /// @notice Disabled: on a UUPS proxy this would permanently brick upgrades.
    function renounceOwnership() public view override onlyOwner {
        revert("Ownership cannot be renounced");
    }

    // ── EIP-2771 settable forwarder ────────────────────────────────────
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
        // Mirror the exact guard used by _msgSender/_msgData so the reported
        // suffix length can never disagree with what those actually strip.
        return (msg.sender == trustedForwarder && msg.data.length >= 20)
            ? 20
            : super._contextSuffixLength();
    }

    /// @notice Required by `ERC2771Forwarder`, which staticcalls this before it will
    ///         relay anything (`_isTrustedByTarget`). Without it every request fails
    ///         with `ERC2771UntrustfulTarget` — the settable-forwarder pattern copied
    ///         from P2PEscrowV3 does NOT include this, which is why gasless was never
    ///         exercised end to end there.
    function isTrustedForwarder(address forwarder) public view returns (bool) {
        return forwarder != address(0) && forwarder == trustedForwarder;
    }

    function setTrustedForwarder(address fwd) external onlyOwner {
        address old = trustedForwarder;
        trustedForwarder = fwd;
        emit TrustedForwarderUpdated(old, fwd);
    }

    function setFeeManager(address fm) external onlyOwner {
        address old = feeManager;
        feeManager = fm;
        emit FeeManagerUpdated(old, fm);
    }

    // ════════════════════════════ Pricing math ═════════════════════════
    function _activePrice(PriceKind kind) internal view returns (uint256) {
        PricePhase[] storage ph = _phases[uint8(kind)];
        require(ph.length > 0, "No phase configured");
        uint256 price = ph[activePhase[uint8(kind)]].price;
        require(price > 0, "Price not set");
        return price;
    }

    /// @notice USDC (6dp) for `mcgpAmount` (18dp) at the active phase of `kind`.
    function quote(PriceKind kind, uint256 mcgpAmount) public view returns (uint256) {
        return (mcgpAmount * _activePrice(kind)) / 1e18;
    }

    // ═══════════════════════════ Legacy (redeem-only) ══════════════════
    function sellLegacy(uint256 mcgpAmount, uint256 minUsdc) external nonReentrant whenNotPaused {
        address user = _msgSender();
        require(mcgpAmount > 0, "Amount must be > 0");
        require(
            legacyRedeemed[user] + mcgpAmount <= legacyEntitlement[user],
            "Exceeds legacy entitlement"
        );

        uint256 usdcAmount = quote(PriceKind.LegacySell, mcgpAmount);
        require(usdcAmount > 0, "USDC amount must be > 0");
        require(usdcAmount >= minUsdc, "Slippage exceeded");
        require(usdcToken.balanceOf(address(this)) >= usdcAmount, "Insufficient USDC");

        legacyRedeemed[user] += mcgpAmount;
        // Legacy MCGP in (becomes inventory), USDC out. Untouched: accountedMcgp.
        mcgpToken.safeTransferFrom(user, address(this), mcgpAmount);
        usdcToken.safeTransfer(user, usdcAmount);

        emit LegacyRedeemed(user, mcgpAmount, usdcAmount);
    }

    function legacyRemaining(address user) external view returns (uint256) {
        uint256 cap = legacyEntitlement[user];
        uint256 used = legacyRedeemed[user];
        return cap > used ? cap - used : 0;
    }

    // ═══════════════════════ Premium — user-signed ═════════════════════
    /// @notice Buy self-exitable premium (premiumBought) with USDC.
    function buyPremium(uint256 mcgpAmount, uint256 maxUsdc) external nonReentrant whenNotPaused {
        address user = _msgSender();
        require(mcgpAmount > 0, "Amount must be > 0");

        uint256 usdcAmount = quote(PriceKind.PremiumBuy, mcgpAmount);
        require(usdcAmount > 0, "USDC amount must be > 0");
        require(usdcAmount <= maxUsdc, "Slippage exceeded");

        usdcToken.safeTransferFrom(user, address(this), usdcAmount);
        premiumBought[user] += mcgpAmount;
        accountedMcgp += mcgpAmount;
        require(mcgpToken.balanceOf(address(this)) >= accountedMcgp, "Underbacked: fund MCGP first");

        emit PremiumBought(user, mcgpAmount, usdcAmount);
    }

    /// @notice Total spendable premium (both buckets).
    function premiumBalanceOf(address user) public view returns (uint256) {
        return premiumBought[user] + premiumCredited[user];
    }

    /**
     * @notice Spend premium to another wallet (marketplace/peer). Debits
     *         credited-first then bought, and credits the recipient the SAME
     *         bucket type — so operator-credited (spend-only) value can never
     *         become self-exitable downstream.
     */
    function spend(address to, uint256 amount, bytes32 ref) external nonReentrant whenNotPaused {
        address from = _msgSender();
        require(to != address(0), "Invalid recipient");
        require(to != from, "Cannot spend to self");
        require(amount > 0, "Amount must be > 0");
        require(ref != bytes32(0), "Invalid ref");
        require(!usedSpendRef[ref], "Ref already used");
        require(premiumBalanceOf(from) >= amount, "Insufficient premium balance");

        usedSpendRef[ref] = true;
        uint256 fromCredited = premiumCredited[from] >= amount ? amount : premiumCredited[from];
        uint256 fromBought = amount - fromCredited;
        if (fromCredited > 0) {
            premiumCredited[from] -= fromCredited;
            premiumCredited[to] += fromCredited;
        }
        if (fromBought > 0) {
            premiumBought[from] -= fromBought;
            premiumBought[to] += fromBought;
        }
        // accountedMcgp unchanged: still owed, to a different holder.

        emit PremiumSpent(from, to, amount, ref);
    }

    /// @notice Sell SELF-EXITABLE premium (premiumBought only) back for USDC.
    function sellPremium(uint256 mcgpAmount, uint256 minUsdc) external nonReentrant whenNotPaused {
        address user = _msgSender();
        require(mcgpAmount > 0, "Amount must be > 0");
        require(premiumBought[user] >= mcgpAmount, "Insufficient bought premium");

        uint256 usdcAmount = quote(PriceKind.PremiumSell, mcgpAmount);
        require(usdcAmount > 0, "USDC amount must be > 0");
        require(usdcAmount >= minUsdc, "Slippage exceeded");
        require(usdcToken.balanceOf(address(this)) >= usdcAmount, "Insufficient USDC");

        premiumBought[user] -= mcgpAmount;
        accountedMcgp -= mcgpAmount;
        usdcToken.safeTransfer(user, usdcAmount);

        emit PremiumSold(user, mcgpAmount, usdcAmount);
    }

    /// @notice Trustless exit (premiumBought only). NOT pausable.
    function withdrawPremiumToWallet(uint256 mcgpAmount) external nonReentrant {
        address user = _msgSender();
        require(mcgpAmount > 0, "Amount must be > 0");
        require(premiumBought[user] >= mcgpAmount, "Insufficient bought premium");

        premiumBought[user] -= mcgpAmount;
        accountedMcgp -= mcgpAmount;
        mcgpToken.safeTransfer(user, mcgpAmount);

        emit PremiumWithdrawn(user, mcgpAmount);
    }

    // ═══════════════════ Premium — operator (IT / NGN) ═════════════════
    /**
     * @notice Credit SPEND-ONLY premium after an off-chain NGN payment settles.
     *         Cannot mint unbacked premium; bounded by per-tx + rolling-window
     *         caps. `ref` must be domain-separated and single-use.
     */
    function creditPremium(address user, uint256 amount, bytes32 ref)
        external
        onlyOperator
        nonReentrant
        whenNotPaused
    {
        require(user != address(0), "Invalid user");
        require(amount > 0, "Amount must be > 0");
        require(amount >= minCredit, "Below min credit");
        require(amount <= maxCreditPerTx, "Over per-tx cap");
        require(ref != bytes32(0), "Invalid ref");
        require(!usedCreditRef[ref], "Ref already used");

        _rollWindow();
        require(creditedInWindow + amount <= creditWindowCap, "Over window cap");
        creditedInWindow += amount;

        usedCreditRef[ref] = true;
        premiumCredited[user] += amount;
        accountedMcgp += amount;
        require(mcgpToken.balanceOf(address(this)) >= accountedMcgp, "Underbacked: fund MCGP first");

        emit PremiumCredited(user, amount, ref);
    }

    /// @notice Begin an operator-settled debit against CREDITED premium only.
    ///         Identical to V1 — existing callers (IT sell) keep exact V1
    ///         semantics and cannot touch a user's self-exitable balance.
    function debitPremium(address user, uint256 amount, bytes32 debitRef)
        external
        onlyOperator
        nonReentrant
        whenNotPaused
    {
        _debit(user, amount, 0, debitRef);
    }

    /// @notice Begin an operator-settled debit that MAY spill into bought premium,
    ///         up to `maxFromBought`. This is what lets a user who bought premium
    ///         with USDC pay a utility bill.
    ///
    /// @dev Reaching bought premium is opt-in PER CALL rather than global. IT sell
    ///      and bills both debit, but only bills should be able to touch
    ///      self-exitable value — and relying on each caller to check the credited
    ///      balance first leaves that distinction one deleted `if` away from
    ///      silently selling a user's bought premium at the wrong exit rate.
    ///      Note this bounds OUR OWN code, not a compromised operator: a stolen key
    ///      simply passes a large maxFromBought. The per-tx and window caps plus a
    ///      guardian pause are what bound that.
    function debitSpendable(address user, uint256 amount, uint256 maxFromBought, bytes32 debitRef)
        external
        onlyOperator
        nonReentrant
        whenNotPaused
    {
        _debit(user, amount, maxFromBought, debitRef);
    }

    function _debit(address user, uint256 amount, uint256 maxFromBought, bytes32 debitRef) internal {
        require(amount > 0, "Amount must be > 0");
        require(amount <= maxDebitPerTx, "Over per-tx debit cap");
        require(debitRef != bytes32(0), "Invalid ref");
        require(debits[debitRef].state == DebitState.None, "Debit exists");

        uint256 credited = premiumCredited[user];
        uint256 fromCredited = credited >= amount ? amount : credited;
        uint256 fromBought = amount - fromCredited;
        // Keep V1's wording for the credited-only case: a `debitPremium` caller
        // that over-draws is short of CREDITED premium, and telling it about a
        // "bought draw" it never asked for is just confusing.
        if (fromBought > maxFromBought) {
            require(maxFromBought > 0, "Insufficient credited premium");
            revert("Exceeds allowed bought draw");
        }
        require(premiumBought[user] >= fromBought, "Insufficient premium balance");

        _rollDebitWindow();
        require(debitedInWindow + amount <= debitWindowCap, "Over debit window cap");
        debitedInWindow += amount;

        if (fromCredited > 0) premiumCredited[user] -= fromCredited;
        if (fromBought > 0) {
            premiumBought[user] -= fromBought;
            _debitFromBought[debitRef] = fromBought;
        }
        pendingSettle[user] += amount;
        debits[debitRef] = Debit({ user: user, amount: amount, state: DebitState.Pending });

        emit PremiumDebitPending(user, amount, debitRef);
    }

    /// @notice How much of a pending debit was drawn from `premiumBought`.
    function debitFromBought(bytes32 debitRef) external view returns (uint256) {
        return _debitFromBought[debitRef];
    }

    function setDebitCaps(uint256 perTx, uint256 window, uint256 windowCap) external onlyOwner {
        _setDebitCaps(perTx, window, windowCap);
    }

    function _setDebitCaps(uint256 perTx, uint256 window, uint256 windowCap) internal {
        require(perTx > 0, "maxDebitPerTx must be > 0");
        require(window > 0, "debitWindow must be > 0");
        require(windowCap > 0, "debitWindowCap must be > 0");
        maxDebitPerTx = perTx;
        debitWindow = window;
        debitWindowCap = windowCap;
        debitWindowStart = block.timestamp;
        debitedInWindow = 0;
        emit DebitCapsUpdated(perTx, window, windowCap);
    }

    /// @notice Finalize an NGN sell after a confirmed payout: free the backing
    ///         (the MCGP becomes withdrawable TSA excess). `whenNotPaused` so a
    ///         compromised operator cannot free backing during incident response.
    function settleDebit(bytes32 debitRef) external onlyOperator nonReentrant whenNotPaused {
        Debit storage d = debits[debitRef];
        require(d.state == DebitState.Pending, "Not pending");
        d.state = DebitState.Resolved;
        pendingSettle[d.user] -= d.amount;
        accountedMcgp -= d.amount;
        emit PremiumDebitSettled(d.user, d.amount, debitRef);
    }

    /// @notice Reverse a debit whose off-chain leg failed — a rejected bill vend
    ///         or a failed NGN payout. Backing unchanged. Deliberately callable
    ///         while paused: it only ever makes a user whole, never frees backing.
    ///
    /// @dev Restores each bucket EXACTLY as it was drawn. Returning a
    ///      bought-funded debit as credited would quietly convert self-exitable
    ///      MCGP into spend-only, so a failed payment would cost the user their
    ///      withdrawal rights.
    function reverseDebit(bytes32 debitRef) external onlyOperator nonReentrant {
        Debit storage d = debits[debitRef];
        require(d.state == DebitState.Pending, "Not pending");
        d.state = DebitState.Resolved;

        uint256 fromBought = _debitFromBought[debitRef];
        uint256 fromCredited = d.amount - fromBought;

        pendingSettle[d.user] -= d.amount;
        if (fromCredited > 0) premiumCredited[d.user] += fromCredited;
        if (fromBought > 0) {
            premiumBought[d.user] += fromBought;
            delete _debitFromBought[debitRef];
        }

        emit PremiumDebitReversed(d.user, d.amount, debitRef);
    }

    function _rollDebitWindow() internal {
        if (block.timestamp >= debitWindowStart + debitWindow) {
            debitWindowStart = block.timestamp;
            debitedInWindow = 0;
        }
    }

    function _rollWindow() internal {
        if (block.timestamp >= windowStart + creditWindow) {
            windowStart = block.timestamp;
            creditedInWindow = 0;
        }
    }

    // ═════════════════════════════ Views ═══════════════════════════════
    /// @notice (MCGP held by contract, MCGP owed = accountedMcgp).
    function backing() external view returns (uint256 held, uint256 owed) {
        held = mcgpToken.balanceOf(address(this));
        owed = accountedMcgp;
    }

    function getPhases(PriceKind kind) external view returns (PricePhase[] memory) {
        return _phases[uint8(kind)];
    }

    // ════════════════════════════ Admin ════════════════════════════════
    function seedLegacy(address[] calldata users, uint256[] calldata amounts) external onlyOwner {
        require(users.length == amounts.length, "Length mismatch");
        for (uint256 i = 0; i < users.length; i++) {
            // Seeding below what a user already redeemed would underflow
            // `legacyRemaining` semantics and permanently wedge redemption.
            require(amounts[i] >= legacyRedeemed[users[i]], "Below redeemed");
            legacyEntitlement[users[i]] = amounts[i];
            emit LegacySeeded(users[i], amounts[i]);
        }
    }

    /// @notice One-time import of premium balances from the V1 proxy.
    /// @dev Callable in batches; each user may be migrated exactly once, because a
    ///      repeat would inflate `accountedMcgp` and silently underback the
    ///      contract. The backing assertion runs once at the end of the batch:
    ///      the intermediate state during a loop is not observable, only the
    ///      final one has to be solvent. Fund BEFORE migrating.
    ///
    ///      `pendingSettle` is intentionally NOT migratable. An in-flight debit
    ///      belongs to whichever contract holds its `debits[ref]` entry; moving
    ///      the balance without the debit record would strand the settle/reverse.
    ///      Drain V1's pending debits first (they were zero at cutover).
    function migratePremium(
        address[] calldata users,
        uint256[] calldata bought,
        uint256[] calldata credited
    ) external onlyOwner {
        require(!migrationClosed, "Migration closed");
        require(users.length == bought.length && users.length == credited.length, "Length mismatch");
        require(users.length > 0, "Empty batch");

        for (uint256 i = 0; i < users.length; i++) {
            address user = users[i];
            require(user != address(0), "Invalid user");
            require(!premiumMigrated[user], "Already migrated");
            uint256 total = bought[i] + credited[i];
            require(total > 0, "Nothing to migrate");

            premiumMigrated[user] = true;
            premiumBought[user] += bought[i];
            premiumCredited[user] += credited[i];
            accountedMcgp += total;

            emit PremiumMigrated(user, bought[i], credited[i]);
        }

        require(mcgpToken.balanceOf(address(this)) >= accountedMcgp, "Underbacked: fund MCGP first");
    }

    /// @notice Seal migration in THIS implementation. While it is open the owner
    ///         can mint entitlement out of nothing, which is exactly the power
    ///         `buyPremium` exists to prevent — so seal the moment the import is
    ///         verified.
    /// @dev    Not irreversible in an absolute sense: this is a UUPS proxy, so
    ///         the owner can always upgrade to an implementation that resets the
    ///         flag or reintroduces a mint path. Sealing removes the capability
    ///         from the current ABI; the owner's upgrade right remains the
    ///         underlying trust assumption, as it does for every other invariant
    ///         here.
    function closeMigration() external onlyOwner {
        require(!migrationClosed, "Already closed");
        migrationClosed = true;
        emit MigrationClosed();
    }

    function addOperator(address op) external onlyOwner {
        require(op != address(0), "Invalid operator");
        operators[op] = true;
        emit OperatorAdded(op);
    }

    /// @notice Removable by owner OR a guardian (fast incident response).
    function removeOperator(address op) external onlyOwnerOrGuardian {
        operators[op] = false;
        emit OperatorRemoved(op);
    }

    function addGuardian(address g) external onlyOwner {
        require(g != address(0), "Invalid guardian");
        guardians[g] = true;
        emit GuardianAdded(g);
    }

    function removeGuardian(address g) external onlyOwner {
        guardians[g] = false;
        emit GuardianRemoved(g);
    }

    function setCreditCaps(
        uint256 _minCredit,
        uint256 _maxCreditPerTx,
        uint256 _creditWindow,
        uint256 _creditWindowCap
    ) external onlyOwner {
        _setCreditCaps(_minCredit, _maxCreditPerTx, _creditWindow, _creditWindowCap);
    }

    /// @dev Zero is rejected for the three ceilings: in V1 zero meant "unlimited",
    ///      which turned the operator rate-limits off by default.
    function _setCreditCaps(
        uint256 _minCredit,
        uint256 _maxCreditPerTx,
        uint256 _creditWindow,
        uint256 _creditWindowCap
    ) internal {
        require(_maxCreditPerTx > 0, "maxCreditPerTx must be > 0");
        require(_creditWindow > 0, "creditWindow must be > 0");
        require(_creditWindowCap > 0, "creditWindowCap must be > 0");
        require(_minCredit <= _maxCreditPerTx, "minCredit exceeds per-tx cap");

        minCredit = _minCredit;
        maxCreditPerTx = _maxCreditPerTx;
        creditWindow = _creditWindow;
        creditWindowCap = _creditWindowCap;
        windowStart = block.timestamp;
        creditedInWindow = 0;
        emit CreditCapsUpdated(_minCredit, _maxCreditPerTx, _creditWindow, _creditWindowCap);
    }

    function addPhase(PriceKind kind, uint256 price, string calldata name) external onlyOwner {
        require(price > 0 && price <= MAX_PRICE, "Invalid price");
        _phases[uint8(kind)].push(PricePhase({ price: price, name: name }));
        emit PhaseAdded(uint8(kind), _phases[uint8(kind)].length - 1, price, name);
    }

    function setActivePhase(PriceKind kind, uint256 index) external onlyOwner {
        require(index < _phases[uint8(kind)].length, "Invalid phase index");
        activePhase[uint8(kind)] = index;
        emit PhaseActivated(uint8(kind), index);
    }

    function updatePhasePrice(PriceKind kind, uint256 index, uint256 newPrice) external onlyOwner {
        require(newPrice > 0 && newPrice <= MAX_PRICE, "Invalid price");
        require(index < _phases[uint8(kind)].length, "Invalid phase index");
        _phases[uint8(kind)][index].price = newPrice;
        emit PhasePriceUpdated(uint8(kind), index, newPrice);
    }

    function fund(address token, uint256 amount) external onlyOwner {
        require(token == address(mcgpToken) || token == address(usdcToken), "Invalid token");
        require(amount > 0, "Amount must be > 0");
        IERC20(token).safeTransferFrom(msg.sender, address(this), amount);
        emit Funded(token, amount);
    }

    /// @notice Withdraw inventory. MCGP only up to TRUE EXCESS (held - accountedMcgp),
    ///         so user-owed premium (incl. pendingSettle) can never be removed.
    function withdraw(address token, address to, uint256 amount) external onlyOwner {
        require(to != address(0), "Invalid recipient");
        require(amount > 0, "Amount must be > 0");
        if (token == address(mcgpToken)) {
            uint256 held = mcgpToken.balanceOf(address(this));
            uint256 excess = held > accountedMcgp ? held - accountedMcgp : 0;
            require(amount <= excess, "Exceeds withdrawable MCGP excess");
            mcgpToken.safeTransfer(to, amount);
        } else if (token == address(usdcToken)) {
            require(usdcToken.balanceOf(address(this)) >= amount, "Insufficient USDC");
            usdcToken.safeTransfer(to, amount);
        } else {
            revert("Invalid token");
        }
        emit Withdrawn(token, to, amount);
    }

    /// @notice Pausable by owner or guardian (incident response).
    function pause() external onlyOwnerOrGuardian { _pause(); }
    function unpause() external onlyOwner { _unpause(); }
}
