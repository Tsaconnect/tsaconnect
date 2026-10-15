// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts/utils/math/Math.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";

/**
 * @title TsaLiquidityPool (V1, as deployed 2026-10-13: test copy for the upgrade test)  ("Provide liquidity & earn", shared pool)
 * @notice One pool of a single stablecoin. Liquidity providers (LPs) deposit
 *         principal; every buyer fill credits its spread to ALL LPs pro-rata to
 *         principal. The company (the collateral provider) posts its own tokens
 *         as collateral, and a fill is refused unless every unrefilled fill is
 *         covered by that collateral (spec 2026-10-14 D3, 100% coverage).
 *
 * @dev Invariant (spec §2.2):
 *        token.balanceOf(this) == totalPrincipal + unclaimed + collateral − outstanding
 *        outstanding <= collateral
 *      hence the pool always holds every LP's principal and earnings. The
 *      accumulator rounds every LP's credit DOWN (reward debt is set rounded
 *      up), so "==" is really ">=" by a few wei of dust: LPs are never credited
 *      more than the spread that was paid in.
 *
 *   - withdraw / claim are never paused and never gated by the depositor
 *     allow-list. That is the custody promise; do not add a guard to them.
 *     The one bound: principal deposited in the last withdrawLock seconds
 *     (24 h by default) cannot be withdrawn yet, so a depositor cannot
 *     deposit just before a fill and leave right after with its spread
 *     (owner, 2026-10-14). New principal earns from the deposit on.
 *   - Operators can only send tokens to owner-approved recipients (the
 *     treasury), capped per tx and per fixed window (amount + spread both
 *     count), with the spread at most maxSpreadBps of the amount, and only
 *     within free collateral. They cannot reach principal or earnings. A
 *     leaked operator key that also allow-lists itself as a depositor earns
 *     at most its pro-rata share of those capped spreads.
 *   - Privileged roles use raw msg.sender, never the ERC-2771 forwarder
 *     (same rule as McgpEntitlementSwapV2).
 *   - UUPS while in development, owned by the owner's wallet (owner 2026-10-14:
 *     no timelock now; the market-ready contract will not be upgradeable). A
 *     TimelockController can own it unchanged, so LPs can
 *     exit before an upgrade lands.
 */
contract TsaLiquidityPoolV1 is Initializable, OwnableUpgradeable, PausableUpgradeable, UUPSUpgradeable {
    using SafeERC20 for IERC20;

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

    uint256 private constant ACC_PRECISION = 1e18;

    IERC20 public token;

    address public collateralProvider;
    mapping(address => bool) public operators;
    address public guardian;
    address public trustedForwarder;

    mapping(address => bool) public depositor;
    mapping(address => bool) public recipient;

    uint256 public totalPrincipal;
    uint256 public collateral;
    uint256 public outstanding;
    uint256 public accEarningsPerShare;

    mapping(address => uint256) public principal;
    mapping(address => uint256) public rewardDebt;
    mapping(address => uint256) public pending;

    uint256 public minDeposit;
    uint256 public maxPerLp;
    uint256 public totalCap;

    uint256 public maxFillPerTx;
    uint256 public fillWindow;
    uint256 public fillWindowCap;
    uint256 public fillWindowStart;
    uint256 public filledInWindow;

    mapping(bytes32 => bool) public usedFillRef;
    mapping(bytes32 => bool) public usedSettleRef;
    /// @notice A fill's spread may be at most this share of its amount (bps).
    uint256 public maxSpreadBps;
    /// @notice Refill refs are the provider's own: an operator cannot burn one.
    mapping(bytes32 => bool) public usedRefillRef;
    /// @notice Seconds a deposit stays unwithdrawable (owner-set, at most 7 days).
    uint256 public withdrawLock;
    /// @notice Principal still inside its lock, and when that lock ends. A
    ///         deposit made while an earlier one is locked joins it, and the
    ///         joined amount unlocks withdrawLock after the newest deposit.
    mapping(address => uint256) public lockedPrincipal;
    mapping(address => uint256) public lockedUntil;

    uint256[25] private __gap;

    /// @notice The longest withdrawLock may be set to.
    uint256 public constant MAX_WITHDRAW_LOCK = 7 days;

    /// @notice The ceiling setFillCaps may raise maxSpreadBps to: 10%.
    uint256 public constant SPREAD_BPS_CEILING = 1_000;

    event Deposited(address indexed lp, uint256 amount);
    event Withdrawn(address indexed lp, uint256 amount);
    event Claimed(address indexed lp, address indexed to, uint256 amount);
    event Filled(bytes32 indexed ref, address indexed to, uint256 amount, uint256 spread);
    event Refilled(bytes32 indexed ref, uint256 amount);
    event SettledFromCollateral(bytes32 indexed ref, uint256 amount);
    event CollateralPosted(uint256 amount);
    event CollateralWithdrawn(uint256 amount);
    event DepositorSet(address indexed account, bool allowed);
    event RecipientSet(address indexed account, bool allowed);
    event RolesUpdated(address collateralProvider, address guardian);
    event OperatorSet(address indexed account, bool allowed);
    event LimitsUpdated(uint256 minDeposit, uint256 maxPerLp, uint256 totalCap);
    event FillCapsUpdated(uint256 maxFillPerTx, uint256 fillWindow, uint256 fillWindowCap, uint256 maxSpreadBps);
    event TrustedForwarderUpdated(address oldForwarder, address newForwarder);
    event WithdrawLockUpdated(uint256 withdrawLock);

    modifier onlyOperator() {
        require(operators[msg.sender], "Not operator");
        _;
    }

    modifier onlyCollateralProvider() {
        require(msg.sender == collateralProvider, "Not collateral provider");
        _;
    }

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    struct InitParams {
        address token;
        address owner;
        address collateralProvider;
        address operator;
        address guardian;
        address recipient;
        uint256 minDeposit;
        uint256 maxPerLp;
        uint256 totalCap;
        uint256 maxFillPerTx;
        uint256 fillWindow;
        uint256 fillWindowCap;
        uint256 maxSpreadBps;
        uint256 withdrawLock;
    }

    function initialize(InitParams calldata p) public initializer {
        require(p.token != address(0), "token is zero");
        __Ownable_init(p.owner);
        __Pausable_init();
        _reentrancyStatus = _NOT_ENTERED;
        token = IERC20(p.token);
        _setRoles(p.collateralProvider, p.guardian);
        _setOperator(p.operator, true);
        _setRecipient(p.recipient, true);
        _setLimits(p.minDeposit, p.maxPerLp, p.totalCap);
        _setFillCaps(p.maxFillPerTx, p.fillWindow, p.fillWindowCap, p.maxSpreadBps);
        _setWithdrawLock(p.withdrawLock);
    }

    // ───────────────────────────── LPs ─────────────────────────────

    function deposit(uint256 amount) external nonReentrant whenNotPaused {
        address lp = _msgSender();
        require(depositor[lp], "Not an allowed depositor");
        require(amount > 0 && amount >= minDeposit, "Below minimum deposit");
        require(principal[lp] + amount <= maxPerLp, "Over per-LP maximum");
        require(totalPrincipal + amount <= totalCap, "Over pool cap");
        _harvest(lp);
        principal[lp] += amount;
        totalPrincipal += amount;
        rewardDebt[lp] = _debt(principal[lp]);
        lockedPrincipal[lp] = (block.timestamp < lockedUntil[lp] ? lockedPrincipal[lp] : 0) + amount;
        lockedUntil[lp] = block.timestamp + withdrawLock;
        _pullExact(lp, amount);
        emit Deposited(lp, amount);
    }

    /// @notice Never paused, never allow-list gated: the custody promise.
    ///         Only principal past its deposit lock can leave.
    function withdraw(uint256 amount) external nonReentrant {
        address lp = _msgSender();
        require(amount > 0 && amount <= principal[lp], "Bad amount");
        require(amount <= withdrawable(lp), "Deposit still locked");
        _harvest(lp);
        principal[lp] -= amount;
        totalPrincipal -= amount;
        rewardDebt[lp] = _debt(principal[lp]);
        token.safeTransfer(lp, amount);
        emit Withdrawn(lp, amount);
    }

    function claim() external nonReentrant returns (uint256) {
        address lp = _msgSender();
        return _claim(lp, lp);
    }

    /// @notice Lets an LP send earnings straight to an off-ramp address.
    function claimTo(address to) external nonReentrant returns (uint256) {
        require(to != address(0), "to is zero");
        return _claim(_msgSender(), to);
    }

    /// @notice Anyone may pay the gas; earnings only ever go to the LP itself.
    function claimFor(address lp) external nonReentrant returns (uint256) {
        return _claim(lp, lp);
    }

    /// @notice Principal that may leave now: all of it once the lock ends.
    function withdrawable(address lp) public view returns (uint256) {
        uint256 locked = block.timestamp < lockedUntil[lp] ? lockedPrincipal[lp] : 0;
        return principal[lp] > locked ? principal[lp] - locked : 0;
    }

    function claimable(address lp) public view returns (uint256) {
        return pending[lp] + _unharvested(lp);
    }

    // ─────────────────────────── Operator ───────────────────────────

    /// @notice Pays `amount` to an approved recipient for a buyer's order and
    ///         credits `spread` to every LP. Refused unless outstanding stays
    ///         within collateral (D3).
    function fill(address to, uint256 amount, uint256 spread, bytes32 ref)
        external
        onlyOperator
        nonReentrant
        whenNotPaused
    {
        require(recipient[to], "Not an approved recipient");
        require(ref != bytes32(0) && !usedFillRef[ref], "Bad or used ref");
        require(amount > 0 && amount + spread <= maxFillPerTx, "Bad fill amount");
        require(spread * 10_000 <= amount * maxSpreadBps, "Spread over maximum");
        require(outstanding + amount + spread <= collateral, "Not covered by collateral");
        _rollFillWindow();
        require(filledInWindow + amount + spread <= fillWindowCap, "Over fill window cap");
        if (spread > 0) {
            require(totalPrincipal > 0, "No LPs to credit");
            accEarningsPerShare += (spread * ACC_PRECISION) / totalPrincipal;
        }
        usedFillRef[ref] = true;
        filledInWindow += amount + spread;
        outstanding += amount + spread;
        token.safeTransfer(to, amount);
        emit Filled(ref, to, amount, spread);
    }

    /// @notice Closes `amount` of outstanding against collateral: the company
    ///         keeps the fiat instead of refilling. No tokens move.
    function settleFromCollateral(uint256 amount, bytes32 ref) external nonReentrant {
        require(operators[msg.sender] || msg.sender == collateralProvider, "Not operator or provider");
        _useSettleRef(ref);
        require(amount > 0 && amount <= outstanding, "Bad amount");
        outstanding -= amount;
        collateral -= amount;
        emit SettledFromCollateral(ref, amount);
    }

    function setDepositor(address account, bool allowed) external {
        require(operators[msg.sender] || msg.sender == owner(), "Not operator or owner");
        require(account != address(0), "account is zero");
        depositor[account] = allowed;
        emit DepositorSet(account, allowed);
    }

    // ─────────────────────── Collateral provider ───────────────────────

    function postCollateral(uint256 amount) external onlyCollateralProvider nonReentrant {
        require(amount > 0, "Bad amount");
        collateral += amount;
        _pullExact(msg.sender, amount);
        emit CollateralPosted(amount);
    }

    function refill(uint256 amount, bytes32 ref) external onlyCollateralProvider nonReentrant {
        require(ref != bytes32(0) && !usedRefillRef[ref], "Bad or used ref");
        usedRefillRef[ref] = true;
        require(amount > 0 && amount <= outstanding, "Bad amount");
        outstanding -= amount;
        _pullExact(msg.sender, amount);
        emit Refilled(ref, amount);
    }

    /// @notice Only the part not backing outstanding fills.
    function withdrawCollateral(uint256 amount) external onlyCollateralProvider nonReentrant {
        require(amount > 0 && amount <= collateral - outstanding, "Collateral is backing fills");
        collateral -= amount;
        token.safeTransfer(msg.sender, amount);
        emit CollateralWithdrawn(amount);
    }

    function freeCapacity() external view returns (uint256) {
        return collateral - outstanding;
    }

    // ───────────────────────────── Admin ─────────────────────────────

    function pause() external {
        require(msg.sender == owner() || msg.sender == guardian, "Not owner or guardian");
        _pause();
    }

    function unpause() external onlyOwner {
        _unpause();
    }

    /// @dev A new collateral provider would own the old one's collateral, so the
    ///      provider can only change once its collateral is fully withdrawn.
    function setRoles(address _collateralProvider, address _guardian) external onlyOwner {
        if (_collateralProvider != collateralProvider) require(collateral == 0, "Collateral still posted");
        require(!operators[_collateralProvider], "provider must not be an operator");
        _setRoles(_collateralProvider, _guardian);
    }

    /// @notice An operator can only ever spend collateral (fills to approved
    ///         recipients, within the caps), never LP principal or earnings —
    ///         so the collateral provider, whose money that is, manages them
    ///         directly, without going through the owner.
    function addOperator(address account) external {
        require(msg.sender == collateralProvider || msg.sender == owner(), "Not provider or owner");
        _setOperator(account, true);
    }

    /// @notice Removing is also open to the guardian: a leaked key is cut off at once.
    function removeOperator(address account) external {
        require(msg.sender == collateralProvider || msg.sender == owner() || msg.sender == guardian, "Not provider, owner or guardian");
        _setOperator(account, false);
    }

    function setRecipient(address account, bool allowed) external onlyOwner {
        _setRecipient(account, allowed);
    }

    function setLimits(uint256 _minDeposit, uint256 _maxPerLp, uint256 _totalCap) external onlyOwner {
        _setLimits(_minDeposit, _maxPerLp, _totalCap);
    }

    function setFillCaps(uint256 perTx, uint256 window, uint256 windowCap, uint256 spreadBps) external onlyOwner {
        _setFillCaps(perTx, window, windowCap, spreadBps);
    }

    /// @dev Applies to deposits made after the change; existing locks keep their end.
    function setWithdrawLock(uint256 seconds_) external onlyOwner {
        _setWithdrawLock(seconds_);
    }

    function setTrustedForwarder(address fwd) external onlyOwner {
        emit TrustedForwarderUpdated(trustedForwarder, fwd);
        trustedForwarder = fwd;
    }

    function isTrustedForwarder(address forwarder) public view returns (bool) {
        return forwarder != address(0) && forwarder == trustedForwarder;
    }

    // ─────────────────────────── Internals ───────────────────────────

    function _accrued(uint256 shares) internal view returns (uint256) {
        return (shares * accEarningsPerShare) / ACC_PRECISION;
    }

    /// @dev Reward debt rounds UP and accrual DOWN, so no LP is ever credited
    ///      more than its exact share; the shortfall is dust left in the pool.
    function _debt(uint256 shares) internal view returns (uint256) {
        return Math.mulDiv(shares, accEarningsPerShare, ACC_PRECISION, Math.Rounding.Ceil);
    }

    function _unharvested(address lp) internal view returns (uint256) {
        uint256 accrued = _accrued(principal[lp]);
        uint256 debt = rewardDebt[lp];
        return accrued > debt ? accrued - debt : 0;
    }

    function _harvest(address lp) internal {
        pending[lp] += _unharvested(lp);
        rewardDebt[lp] = _debt(principal[lp]);
    }

    function _claim(address lp, address to) internal returns (uint256 amount) {
        _harvest(lp);
        amount = pending[lp];
        require(amount > 0, "Nothing to claim");
        pending[lp] = 0;
        token.safeTransfer(to, amount);
        emit Claimed(lp, to, amount);
    }

    /// @dev Refuses fee-on-transfer behaviour: the books must match the balance.
    function _pullExact(address from, uint256 amount) internal {
        uint256 before = token.balanceOf(address(this));
        token.safeTransferFrom(from, address(this), amount);
        require(token.balanceOf(address(this)) - before == amount, "Inexact transfer");
    }

    function _useSettleRef(bytes32 ref) internal {
        require(ref != bytes32(0) && !usedSettleRef[ref], "Bad or used ref");
        usedSettleRef[ref] = true;
    }

    /// @dev A fixed window (it restarts at the first fill after it ends), not a
    ///      sliding one: up to 2 × fillWindowCap can leave across a boundary.
    function _rollFillWindow() internal {
        if (block.timestamp >= fillWindowStart + fillWindow) {
            fillWindowStart = block.timestamp;
            filledInWindow = 0;
        }
    }

    function _setRoles(address _collateralProvider, address _guardian) internal {
        require(_collateralProvider != address(0) && _guardian != address(0), "role is zero");
        collateralProvider = _collateralProvider;
        guardian = _guardian;
        emit RolesUpdated(_collateralProvider, _guardian);
    }

    function _setOperator(address account, bool allowed) internal {
        require(account != address(0), "operator is zero");
        require(!allowed || account != collateralProvider, "operator must not be the provider");
        operators[account] = allowed;
        emit OperatorSet(account, allowed);
    }

    function _setRecipient(address account, bool allowed) internal {
        require(account != address(0), "recipient is zero");
        recipient[account] = allowed;
        emit RecipientSet(account, allowed);
    }

    function _setLimits(uint256 _minDeposit, uint256 _maxPerLp, uint256 _totalCap) internal {
        require(_maxPerLp > 0 && _totalCap > 0, "limits must be > 0");
        require(_minDeposit <= _maxPerLp && _maxPerLp <= _totalCap, "limits out of order");
        minDeposit = _minDeposit;
        maxPerLp = _maxPerLp;
        totalCap = _totalCap;
        emit LimitsUpdated(_minDeposit, _maxPerLp, _totalCap);
    }

    /// @dev Mandatory non-zero caps (the V2 lesson: zero must never mean unlimited).
    function _setFillCaps(uint256 perTx, uint256 window, uint256 windowCap, uint256 spreadBps) internal {
        require(perTx > 0 && window > 0 && windowCap > 0 && spreadBps > 0, "fill caps must be > 0");
        require(perTx <= windowCap, "perTx over window cap");
        require(spreadBps <= SPREAD_BPS_CEILING, "spread cap over ceiling");
        maxFillPerTx = perTx;
        fillWindow = window;
        fillWindowCap = windowCap;
        maxSpreadBps = spreadBps;
        fillWindowStart = block.timestamp;
        filledInWindow = 0;
        emit FillCapsUpdated(perTx, window, windowCap, spreadBps);
    }

    function _setWithdrawLock(uint256 seconds_) internal {
        require(seconds_ <= MAX_WITHDRAW_LOCK, "withdraw lock over maximum");
        withdrawLock = seconds_;
        emit WithdrawLockUpdated(seconds_);
    }

    function _authorizeUpgrade(address) internal override onlyOwner {}

    function _checkOwner() internal view override {
        if (owner() != msg.sender) revert OwnableUnauthorizedAccount(msg.sender);
    }

    function renounceOwnership() public view override onlyOwner {
        revert("Ownership cannot be renounced");
    }

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
        return (msg.sender == trustedForwarder && msg.data.length >= 20) ? 20 : super._contextSuffixLength();
    }
}
