// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/cryptography/EIP712Upgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/ContextUpgradeable.sol";
import "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

import "./interfaces/IFeeManager.sol";

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

/// @title P2PEscrowV3
/// @notice Two-sided pool/JIT escrow. UUPS implementation upgrade of P2PEscrowV2 that adds the
///         A1 live-read fee model (see docs/superpowers/specs/2026-06-08-configurable-feemanager-design.md
///         §3.1 / §7.1). The proxy address and full storage layout from V2 are preserved; the new
///         `feeManager` / `legacySplitEnabled` / `tokenSymbol` slots are APPENDED before `__gap`
///         (which shrinks 50 → 47), and the `Trade` struct gains two `uint16` snapshot fields that
///         pack into the existing slot-D free bytes (no per-trade slot shift, `voucherHash` stays put).
///
///         openTrade/openTradeWithDeposit/permit2 variants LIVE-READ the platform + gas bps from the
///         FeeManager at open and SNAPSHOT both into the Trade, so an in-flight trade is unaffected by
///         a later `setRate`. The legacy `feeBps` field is KEPT for back-compat: trades opened by V2
///         (whose snapshot bytes are zero) release at `t.feeBps`; new V3 trades set `feeBps = 0` and
///         release at `platformFeeBpsSnapshot + gasFeeBpsSnapshot`.
contract P2PEscrowV3 is
    Initializable,
    UUPSUpgradeable,
    OwnableUpgradeable,
    PausableUpgradeable,
    EIP712Upgradeable
{
    using SafeERC20 for IERC20;

    // ── Custom errors ──────────────────────────────────────────────────────
    // Replaces the long ASCII revert strings to reclaim bytecode headroom under the
    // EIP-170 24576-byte limit (the runs=1 override only just fit them). Selectors are
    // 4 bytes vs. the multi-word string blobs, leaving room for future audit fixes.
    error Reentrant();
    error NotAdmin();
    error ZeroAddress();
    error AlreadyAdmin();
    error InvalidToken();
    error TokenNotAccepted();
    error BadAmount();
    error InsufficientAvailable();
    error AccountingCorrupt();
    error ExceedsExcess();
    error OnlyVoucherSeller();
    error SnapshotFeeTooHigh();
    error InvalidTradeId();
    error TradeExists();
    error InvalidVoucherSeller();
    error SelfTrade();
    error VoucherExpired();
    error VoucherBadBounds();
    error BelowVoucherMin();
    error AboveVoucherMax();
    error VoucherCancelledErr();
    error ExceedsVoucherCapacity();
    error BadVoucherSignature();
    error SellerInsufficient();
    error InvalidMerchant();
    error WindowOutOfBounds();
    error NotPending();
    error OnlyBuyer();
    error NotReleasable();
    error OnlySeller();
    error EmptyTrade();
    error LockedInvariant();
    error SystemWalletNotSet();
    error TimeoutNotElapsed();
    error NotDisputable();
    error NotAParty();
    error NotDisputed();
    error NotPaid();
    error AutoReleaseNotArmed();
    error WindowNotElapsed();
    error FeeTooHigh();
    error InvalidMcgp();
    error McgpNotAccepted();
    error FeeManagerZero();
    error Permit2NotSet();
    error InvalidSystemWallet();

    // ── Inline upgradeable reentrancy guard ────────────────────────────────
    // OZ v5 removed ReentrancyGuardUpgradeable; implement the slot directly.
    uint256 private constant _NOT_ENTERED = 1;
    uint256 private constant _ENTERED = 2;
    uint256 private _reentrancyStatus;

    modifier nonReentrant() {
        if (_reentrancyStatus == _ENTERED) revert Reentrant();
        _reentrancyStatus = _ENTERED;
        _;
        _reentrancyStatus = _NOT_ENTERED;
    }

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
        uint16  feeBps;                 // KEPT for old-trade back-compat (V3 sets 0 for new trades)
        uint16  platformFeeBpsSnapshot; // NEW — packs into the existing slot-D free bytes
        uint16  gasFeeBpsSnapshot;      // NEW — also packs into slot D
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

    // A1 escrow-side caps on the FeeManager snapshot so a misconfig can't strand a trade (§7.1).
    uint16  public constant MAX_PLATFORM_FEE_BPS = 300;   // 3%
    uint16  public constant MAX_GAS_FEE_BPS = 300;        // 3%

    // Feature keys (match the off-chain DefaultFeeConfigs seed rows — §7.1 combined-feature note).
    string private constant FEATURE_P2P_PLATFORM = "p2p_platform";
    string private constant FEATURE_P2P_GAS = "p2p";

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
    /// @dev RESERVED — currently has NO effect on fees. The loser-pays dispute fee
    ///      is deferred to the future dispute-v2 sub-project. adminResolve uses
    ///      the trade-level snapshots, not this field.
    uint16 public disputeFeeBps;
    uint40 public paymentWindow;
    uint40 public autoReleaseWindow;

    // Gasless hooks (disabled until set).
    address public trustedForwarder;
    address public permit2;

    // Per-merchant ⚡ auto-release opt-in (default false).
    mapping(address => bool) public autoReleaseOptIn;

    // ── V3 appended storage (A1 live-read, §3.1 / §7.1) ─────────────────────
    /// @notice FeeManager proxy for this chain (the on-chain rate source of truth). +1 slot.
    address public feeManager;
    /// @notice Deployed TRUE; flipped FALSE at cutover (D5). P2P already sends 100% to system so
    ///         there is no split branch — the bool is carried for boot-guard / symmetry parity. +1 slot.
    bool    public legacySplitEnabled;
    /// @notice Owner-set token symbol per accepted token, used as the FeeManager key. +1 slot.
    mapping(address => string) public tokenSymbol;
    // platformFeeBps/gasFeeBps are NOT stored as contract state — they are READ from FeeManager
    // at openTrade and SNAPSHOTTED into the Trade. No cached escrow copy to drift (A1).

    // Slot math: `feeManager` (address, 20B) + `legacySplitEnabled` (bool, 1B) PACK into one slot;
    // `tokenSymbol` (mapping head) takes one slot → 2 new slots, so __gap shrinks 50 → 48.
    // (The OZ upgrades plugin is authoritative on this packing — see validateUpgrade test.)
    uint256[48] private __gap;

    // ── Events (v2 set, plus v3 additions) ─────────────────────────────────
    event Deposited(address indexed seller, address indexed token, uint256 amount);
    event Withdrawn(address indexed seller, address indexed token, uint256 amount);
    event VoucherCancelled(bytes32 indexed voucherHash, address indexed seller);
    event TradeOpened(bytes32 indexed tradeId, bytes32 indexed voucherHash, address indexed buyer, address seller, address token, uint256 amount);
    event TradeOpenedWithDeposit(bytes32 indexed tradeId, address indexed seller, address indexed buyer, address token, uint256 amount);
    event TradePaid(bytes32 indexed tradeId);
    event TradeReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeAutoReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeCancelled(bytes32 indexed tradeId, uint256 amount, bool returnedToPool);
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
    // V3 additions
    event FeeManagerUpdated(address indexed oldFm, address indexed newFm);
    event LegacySplitEnabledUpdated(bool enabled);
    event TokenSymbolUpdated(address indexed token, string symbol);

    /// @dev Owner authorization must NOT honor the EIP-2771 forwarder — a trusted
    /// forwarder must never be able to impersonate the owner. Override to use the
    /// real msg.sender instead of _msgSender().
    function _checkOwner() internal view override {
        if (owner() != msg.sender) {
            revert OwnableUnauthorizedAccount(msg.sender);
        }
    }

    modifier onlyAdmin() { if (!isAdmin(msg.sender)) revert NotAdmin(); _; }

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() { _disableInitializers(); }

    function initialize(address initialOwner, address[] calldata initialAdmins) external initializer {
        __Ownable_init(initialOwner);
        __Pausable_init();
        __EIP712_init("TSAConnectP2PEscrow", "2");
        _reentrancyStatus = _NOT_ENTERED;

        feeBps = 50;
        paymentWindow = 30 minutes;
        legacySplitEnabled = true; // deployed TRUE (today's behavior — §9 cutover)
        // autoReleaseWindow, disputeFeeBps, trustedForwarder, permit2 stay zero (disabled).

        for (uint256 i = 0; i < initialAdmins.length; i++) {
            address a = initialAdmins[i];
            if (a == address(0)) revert ZeroAddress();
            if (admins[a]) revert AlreadyAdmin();
            admins[a] = true;
            emit AdminAdded(a);
        }
    }

    function _authorizeUpgrade(address) internal override onlyOwner {}

    /// @notice One-shot V3 re-initializer, run via upgradeProxy's `call` option at the V2→V3 upgrade.
    /// @dev `upgradeProxy` does NOT re-run `initialize` (initializer modifier), so the freshly-added
    ///      `legacySplitEnabled` slot would otherwise read zero=false. `reinitializer(2)` runs exactly
    ///      once on the live proxy to set it TRUE — the spec D5 / §9-step-1 "deployed TRUE = byte-for-byte
    ///      today" guarantee. For P2P the bool has no split branch so this is symmetry/boot-guard parity;
    ///      it is still set so the on-chain state matches Product/Service. Harmless if a fresh proxy
    ///      already ran `initialize` (version 1 < 2, so this still runs once).
    function initializeV3() external reinitializer(2) {
        legacySplitEnabled = true;
    }

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
        // Mirror the exact guard used by _msgSender/_msgData so the reported
        // suffix length can never disagree with what those actually strip.
        return (msg.sender == trustedForwarder && msg.data.length >= 20)
            ? 20
            : super._contextSuffixLength();
    }

    // ── Deposit / withdraw ─────────────────────────────────────────────────

    /// @notice Deposit tokens into the seller's available pool.
    /// @dev ONLY standard ERC-20s may be whitelisted. `accountedBalance` is credited
    ///      with the nominal `amount`; fee-on-transfer or rebasing tokens would cause
    ///      `balanceOf(this) < accountedBalance` and break the sweepExcess invariant.
    function deposit(address token, uint256 amount) external nonReentrant whenNotPaused {
        if (token == address(0)) revert InvalidToken();
        if (!acceptedTokens[token]) revert TokenNotAccepted();
        if (amount == 0) revert BadAmount();
        address from = _msgSender();
        IERC20(token).safeTransferFrom(from, address(this), amount);
        available[from][token] += amount;
        accountedBalance[token] += amount;
        emit Deposited(from, token, amount);
    }

    // No `acceptedTokens` check here BY DESIGN: a token can be delisted while a
    // seller still holds an `available` balance in it, and they must always be
    // able to recover it. Also intentionally not pause-guarded (recovery path).
    function withdraw(address token, uint256 amount) external nonReentrant {
        if (amount == 0) revert BadAmount();
        address acct = _msgSender();
        uint256 avail = available[acct][token];
        if (avail < amount) revert InsufficientAvailable();
        available[acct][token] = avail - amount;
        accountedBalance[token] -= amount;
        IERC20(token).safeTransfer(acct, amount);
        emit Withdrawn(acct, token, amount);
    }

    function balanceOf(address seller, address token) external view returns (uint256 _available, uint256 _locked) {
        return (available[seller][token], locked[seller][token]);
    }

    function sweepExcess(address token, uint256 amount) external onlyOwner {
        if (token == address(0)) revert InvalidToken();
        uint256 contractBal = IERC20(token).balanceOf(address(this));
        uint256 accounted = accountedBalance[token];
        if (contractBal < accounted) revert AccountingCorrupt();
        if (amount > contractBal - accounted) revert ExceedsExcess();
        IERC20(token).safeTransfer(owner(), amount);
        emit ExcessSwept(token, amount);
    }

    // ── Voucher management ─────────────────────────────────────────────────

    function hashVoucher(AdVoucher calldata v) public view returns (bytes32) {
        return _hashTypedDataV4(keccak256(abi.encode(
            AD_VOUCHER_TYPEHASH, v.seller, v.token, v.maxAmount, v.minPerTrade, v.maxPerTrade, v.deadline, v.nonce
        )));
    }

    function cancelVoucher(AdVoucher calldata v) external {
        if (_msgSender() != v.seller) revert OnlyVoucherSeller();
        bytes32 h = hashVoucher(v);
        if (!voucherCancelled[h]) { voucherCancelled[h] = true; emit VoucherCancelled(h, v.seller); }
    }

    // ── Trade lifecycle ────────────────────────────────────────────────────

    /// @dev A1 live-read: snapshot the platform + gas bps for `token` from the FeeManager,
    ///      clamped to the escrow caps and honoring mcgpExempt. When `feeManager == address(0)`
    ///      (pre-wire window) fall back to the legacy `feeBps` into the platform slot so a trade
    ///      opened before the FeeManager is wired never snapshots 0 (§7.1 point 3).
    ///
    ///      HONORS `active` (IFeeManager.rateFor 4th return value): an UNSET / disabled key returns
    ///      active==false, which signals "not configured". Rather than silently snapshot 0 (a silent
    ///      fee leak — see FeeManager.sol:75-76), each leg FALLS BACK to the legacy path when its
    ///      `active` is false: the platform leg falls back to `feeBps`, the gas leg to 0 (legacy P2P
    ///      had no separate gas bps). This matches the `feeManager == address(0)` branch so a
    ///      FeeManager wired before a token's rate is seeded never releases fee-free.
    function _snapshotFeeBps(address token) internal view returns (uint16 platformBps, uint16 gasBps) {
        bool isMcgp = (mcgpToken != address(0) && token == mcgpToken);
        uint16 legacyPlatform = isMcgp ? 0 : feeBps;
        if (feeManager == address(0)) {
            return (legacyPlatform, 0);
        }
        string memory sym = tokenSymbol[token];

        (uint16 pBps, , bool pExempt, bool pActive) = IFeeManager(feeManager).rateFor(FEATURE_P2P_PLATFORM, sym);
        (uint16 gBps, , bool gExempt, bool gActive) = IFeeManager(feeManager).rateFor(FEATURE_P2P_GAS, sym);

        if (pBps > MAX_PLATFORM_FEE_BPS || gBps > MAX_GAS_FEE_BPS) revert SnapshotFeeTooHigh();

        // active==false → not configured → fall back to legacy rather than charge 0 silently.
        platformBps = !pActive ? legacyPlatform : ((isMcgp && pExempt) ? 0 : pBps);
        gasBps = !gActive ? 0 : ((isMcgp && gExempt) ? 0 : gBps);
    }

    /// @dev Snapshot the live fee bps and write a fresh Pending trade. Shared by all open paths so
    ///      the (large) struct literal and the FeeManager read live in one place.
    function _storeTrade(
        bytes32 tradeId,
        address seller,
        address buyer,
        address token,
        uint96 amount,
        Funding funding,
        bytes32 voucherHash
    ) internal {
        (uint16 platformBps, uint16 gasBps) = _snapshotFeeBps(token);
        trades[tradeId] = Trade({
            seller: seller, buyer: buyer, token: token, amount: amount,
            createdAt: uint40(block.timestamp), autoReleaseAt: 0,
            status: Status.Pending, funding: funding,
            feeBps: 0, platformFeeBpsSnapshot: platformBps, gasFeeBpsSnapshot: gasBps,
            voucherHash: voucherHash
        });
    }

    function getTrade(bytes32 tradeId) external view returns (
        address seller, address buyer, address token, uint256 amount,
        uint256 createdAt, uint8 status, uint8 funding, uint16 feeBps_, uint256 autoReleaseAt, bytes32 voucherHash
    ) {
        Trade storage t = trades[tradeId];
        return (t.seller, t.buyer, t.token, t.amount, t.createdAt, uint8(t.status), uint8(t.funding), t.feeBps, t.autoReleaseAt, t.voucherHash);
    }

    // The public `trades(bytes32)` mapping getter exposes platformFeeBpsSnapshot/gasFeeBpsSnapshot
    // (and the legacy feeBps) directly, so off-chain accrual (§8) reads them there — no extra accessor.

    function openTrade(bytes32 tradeId, AdVoucher calldata v, bytes calldata sig, uint256 amount)
        external nonReentrant whenNotPaused
    {
        if (tradeId == bytes32(0)) revert InvalidTradeId();
        if (trades[tradeId].status != Status.None) revert TradeExists();
        if (amount == 0 || amount > type(uint96).max) revert BadAmount();
        address buyer = _msgSender();
        if (v.seller == address(0)) revert InvalidVoucherSeller();
        if (v.seller == buyer) revert SelfTrade();
        if (v.token == address(0)) revert InvalidToken();
        if (!acceptedTokens[v.token]) revert TokenNotAccepted();
        if (block.timestamp > v.deadline) revert VoucherExpired();
        if (v.maxAmount == 0) revert VoucherBadBounds();
        if (v.maxPerTrade != 0 && v.maxPerTrade > v.maxAmount) revert VoucherBadBounds();
        if (v.minPerTrade != 0 && v.maxPerTrade != 0 && v.minPerTrade > v.maxPerTrade) revert VoucherBadBounds();
        if (v.minPerTrade > v.maxAmount) revert VoucherBadBounds();
        if (v.minPerTrade != 0 && amount < v.minPerTrade) revert BelowVoucherMin();
        if (v.maxPerTrade != 0 && amount > v.maxPerTrade) revert AboveVoucherMax();

        bytes32 voucherHash = hashVoucher(v);
        if (voucherCancelled[voucherHash]) revert VoucherCancelledErr();
        uint256 filled = voucherFilled[voucherHash];
        if (filled + amount > v.maxAmount) revert ExceedsVoucherCapacity();
        if (ECDSA.recover(voucherHash, sig) != v.seller) revert BadVoucherSignature();

        uint256 sellerAvail = available[v.seller][v.token];
        if (sellerAvail < amount) revert SellerInsufficient();
        available[v.seller][v.token] = sellerAvail - amount;
        locked[v.seller][v.token] += amount;
        voucherFilled[voucherHash] = filled + amount;

        _storeTrade(tradeId, v.seller, buyer, v.token, uint96(amount), Funding.PooledFromMaker, voucherHash);
        emit TradeOpened(tradeId, voucherHash, buyer, v.seller, v.token, amount);
    }

    /// @notice Open a buy-ad trade by depositing tokens JIT (taker supplies funds).
    /// @dev ONLY standard ERC-20s may be whitelisted. `accountedBalance` is credited
    ///      with the nominal `amount`; fee-on-transfer or rebasing tokens would cause
    ///      `balanceOf(this) < accountedBalance` and break the sweepExcess invariant.
    function openTradeWithDeposit(bytes32 tradeId, address token, uint256 amount, address merchant)
        external nonReentrant whenNotPaused
    {
        if (tradeId == bytes32(0)) revert InvalidTradeId();
        if (trades[tradeId].status != Status.None) revert TradeExists();
        if (amount == 0 || amount > type(uint96).max) revert BadAmount();
        if (token == address(0)) revert InvalidToken();
        if (!acceptedTokens[token]) revert TokenNotAccepted();
        if (merchant == address(0)) revert InvalidMerchant();
        address user = _msgSender();
        if (merchant == user) revert SelfTrade();

        IERC20(token).safeTransferFrom(user, address(this), amount); // pull source is always the signer
        locked[user][token] += amount;
        accountedBalance[token] += amount;

        _storeTrade(tradeId, user, merchant, token, uint96(amount), Funding.DepositedByTaker, bytes32(0));
        emit TradeOpenedWithDeposit(tradeId, user, merchant, token, amount);
    }

    function setAutoReleaseWindow(uint40 window) external onlyOwner {
        if (window > MAX_AUTO_RELEASE_WINDOW) revert WindowOutOfBounds();
        uint40 old = autoReleaseWindow; autoReleaseWindow = window;
        emit AutoReleaseWindowUpdated(old, window);
    }

    function setAutoReleaseOptIn(bool optedIn) external {
        autoReleaseOptIn[_msgSender()] = optedIn;
        emit AutoReleaseOptInUpdated(_msgSender(), optedIn);
    }

    function markPaid(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        if (t.status != Status.Pending) revert NotPending();
        if (_msgSender() != t.buyer) revert OnlyBuyer();
        t.status = Status.Paid;
        // Auto-release opt-in is keyed on `t.seller` — the crypto PROVIDER,
        // who bears the optimistic-release risk (their escrowed crypto is what
        // auto-releases to the buyer if they neither release nor dispute in the
        // window). This is the right party for BOTH flows: on a sell-ad the
        // seller is the merchant; on a buy-ad the seller is the user selling
        // crypto, who is exactly the party consenting to "no action = release".
        if (autoReleaseWindow > 0 && autoReleaseOptIn[t.seller]) {
            t.autoReleaseAt = uint40(block.timestamp + autoReleaseWindow);
        }
        emit TradePaid(tradeId);
    }

    function release(bytes32 tradeId) external nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        if (t.status != Status.Pending && t.status != Status.Paid) revert NotReleasable();
        if (_msgSender() != t.seller) revert OnlySeller();
        _settleRelease(tradeId, t, false);
    }

    function adminRelease(bytes32 tradeId) external onlyAdmin nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        if (t.status != Status.Pending && t.status != Status.Paid) revert NotReleasable();
        _settleRelease(tradeId, t, false);
    }

    /// @dev Total fee bps for a trade. New V3 trades carry `platformFeeBpsSnapshot +
    ///      gasFeeBpsSnapshot` (feeBps==0). Trades opened by V2 carry feeBps>0 and zero
    ///      snapshots — the back-compat branch falls back to `feeBps` so a pre-upgrade
    ///      trade releases at its original snapshot (§7.1 point 4).
    function _tradeTotalBps(Trade storage t) internal view returns (uint16) {
        uint16 total = t.platformFeeBpsSnapshot + t.gasFeeBpsSnapshot;
        if (total == 0 && t.feeBps != 0) total = t.feeBps; // back-compat for pre-V3 trades
        return total;
    }

    function _settleRelease(bytes32 tradeId, Trade storage t, bool auto_) internal {
        uint96 amt = t.amount;
        if (amt == 0) revert EmptyTrade();
        if (locked[t.seller][t.token] < amt) revert LockedInvariant();
        (uint256 toBuyer, uint256 fee) = _splitFee(amt, _tradeTotalBps(t));
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
        if (sw == address(0)) revert SystemWalletNotSet();
        return sw;
    }

    /// Shared refund: pool-credit for PooledFromMaker, transfer-out for DepositedByTaker.
    /// Guards the voucherFilled decrement so buy-ads (voucherHash==0) never underflow.
    function _refund(Trade storage t) internal {
        uint96 amt = t.amount;
        if (amt == 0) revert EmptyTrade();
        if (locked[t.seller][t.token] < amt) revert LockedInvariant();
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
        if (t.status != Status.Pending) revert NotPending();
        if (block.timestamp < uint256(t.createdAt) + paymentWindow) revert TimeoutNotElapsed();
        t.status = Status.Cancelled;
        _refund(t);
        emit TradeCancelled(tradeId, t.amount, t.funding == Funding.PooledFromMaker);
    }

    function raiseDispute(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        if (t.status != Status.Pending && t.status != Status.Paid) revert NotDisputable();
        address s = _msgSender();
        if (s != t.buyer && s != t.seller) revert NotAParty();
        t.status = Status.Disputed;
        emit TradeDisputed(tradeId, s);
    }

    function adminResolve(bytes32 tradeId, bool releaseToBuyer) external onlyAdmin nonReentrant {
        Trade storage t = trades[tradeId];
        if (t.status != Status.Disputed) revert NotDisputed();
        uint96 amt = t.amount;
        if (amt == 0) revert EmptyTrade();
        if (locked[t.seller][t.token] < amt) revert LockedInvariant();

        if (releaseToBuyer) {
            (uint256 toBuyer, uint256 fee) = _splitFee(amt, _tradeTotalBps(t));
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

    /// Anyone may finalize after the window; destination is fixed (buyer), so caller is irrelevant.
    function finalizeRelease(bytes32 tradeId) external nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        if (t.status != Status.Paid) revert NotPaid();
        if (t.autoReleaseAt == 0) revert AutoReleaseNotArmed();
        if (block.timestamp < t.autoReleaseAt) revert WindowNotElapsed();
        _settleRelease(tradeId, t, true);
    }

    // ── Owner setters ──────────────────────────────────────────────────────

    function setFeeBps(uint16 bps) external onlyOwner {
        if (bps > MAX_FEE_BPS) revert FeeTooHigh();
        uint16 old = feeBps; feeBps = bps; emit FeeBpsUpdated(old, bps);
    }

    /// @dev RESERVED — setting this value currently has NO effect on fees. The
    ///      loser-pays dispute fee policy is deferred to the future dispute-v2
    ///      sub-project. Stored for forward-compatibility only.
    function setDisputeFeeBps(uint16 bps) external onlyOwner {
        if (bps > MAX_DISPUTE_FEE_BPS) revert FeeTooHigh();
        uint16 old = disputeFeeBps; disputeFeeBps = bps; emit DisputeFeeBpsUpdated(old, bps);
    }

    function setPaymentWindow(uint40 window) external onlyOwner {
        if (window < MIN_PAYMENT_WINDOW || window > MAX_PAYMENT_WINDOW) revert WindowOutOfBounds();
        uint40 old = paymentWindow; paymentWindow = window; emit PaymentWindowUpdated(old, window);
    }

    function setTrustedForwarder(address fwd) external onlyOwner {
        address old = trustedForwarder; trustedForwarder = fwd; emit TrustedForwarderUpdated(old, fwd);
    }

    function setPermit2(address p) external onlyOwner {
        address old = permit2; permit2 = p; emit Permit2Updated(old, p);
    }

    function setMcgpToken(address _mcgpToken) external onlyOwner {
        if (_mcgpToken == address(0)) revert InvalidMcgp();
        if (!acceptedTokens[_mcgpToken]) revert McgpNotAccepted();
        address old = mcgpToken; mcgpToken = _mcgpToken; emit McgpTokenUpdated(old, _mcgpToken);
    }

    function addAdmin(address account) external onlyOwner {
        if (account == address(0)) revert ZeroAddress();
        if (admins[account]) revert AlreadyAdmin();
        admins[account] = true; emit AdminAdded(account);
    }

    function removeAdmin(address account) external onlyOwner {
        if (!admins[account]) revert NotAdmin();
        admins[account] = false; emit AdminRemoved(account);
    }

    // ── V3 owner setters (A1 live-read wiring, §3.1) ────────────────────────

    /// @notice Wire the per-chain FeeManager. Set once at upgrade (§3.1).
    function setFeeManager(address fm) external onlyOwner {
        if (fm == address(0)) revert FeeManagerZero();
        address old = feeManager; feeManager = fm; emit FeeManagerUpdated(old, fm);
    }

    /// @notice Toggle the legacy on-chain split (deployed TRUE; flipped FALSE at cutover — D5).
    /// @dev P2P already sends 100% of the fee to the system wallet, so this bool has no split
    ///      branch here; it is carried for boot-guard parity with Product/Service.
    function setLegacySplitEnabled(bool on) external onlyOwner {
        legacySplitEnabled = on; emit LegacySplitEnabledUpdated(on);
    }

    /// @notice Set the FeeManager-keying symbol for an accepted token (owner-set, §3.1).
    function setTokenSymbol(address token, string calldata sym) external onlyOwner {
        if (token == address(0)) revert InvalidToken();
        tokenSymbol[token] = sym;
        emit TokenSymbolUpdated(token, sym);
    }

    // ── Permit2 variants ───────────────────────────────────────────────────

    /// @notice Deposit tokens via Permit2 signature-transfer into the seller's available pool.
    /// @dev ONLY standard ERC-20s may be whitelisted. `accountedBalance` is credited
    ///      with the nominal `amount`; fee-on-transfer or rebasing tokens would cause
    ///      `balanceOf(this) < accountedBalance` and break the sweepExcess invariant.
    function depositWithPermit2(IPermit2.PermitTransferFrom calldata permit, bytes calldata signature)
        external nonReentrant whenNotPaused
    {
        if (permit2 == address(0)) revert Permit2NotSet();
        address token = permit.permitted.token;
        if (token == address(0)) revert InvalidToken();
        if (!acceptedTokens[token]) revert TokenNotAccepted();
        uint256 amount = permit.permitted.amount;
        if (amount == 0) revert BadAmount();
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

    /// @notice Open a buy-ad trade with JIT funding via Permit2 signature-transfer.
    /// @dev ONLY standard ERC-20s may be whitelisted. `accountedBalance` is credited
    ///      with the nominal `amount`; fee-on-transfer or rebasing tokens would cause
    ///      `balanceOf(this) < accountedBalance` and break the sweepExcess invariant.
    function openTradeWithDepositPermit2(
        bytes32 tradeId, address merchant,
        IPermit2.PermitTransferFrom calldata permit, bytes calldata signature
    ) external nonReentrant whenNotPaused {
        if (permit2 == address(0)) revert Permit2NotSet();
        if (tradeId == bytes32(0)) revert InvalidTradeId();
        if (trades[tradeId].status != Status.None) revert TradeExists();
        address token = permit.permitted.token;
        if (token == address(0)) revert InvalidToken();
        uint256 amount = permit.permitted.amount;
        if (amount == 0 || amount > type(uint96).max) revert BadAmount();
        if (!acceptedTokens[token]) revert TokenNotAccepted();
        if (merchant == address(0)) revert InvalidMerchant();
        address user = _msgSender();
        if (merchant == user) revert SelfTrade();

        IPermit2(permit2).permitTransferFrom(
            permit,
            IPermit2.SignatureTransferDetails({ to: address(this), requestedAmount: amount }),
            user, signature
        );
        locked[user][token] += amount;
        accountedBalance[token] += amount;
        _storeTrade(tradeId, user, merchant, token, uint96(amount), Funding.DepositedByTaker, bytes32(0));
        emit TradeOpenedWithDeposit(tradeId, user, merchant, token, amount);
    }

    // ── Pause controls ─────────────────────────────────────────────────────

    function pause() external onlyOwner { _pause(); }
    function unpause() external onlyOwner { _unpause(); }

    function setSystemWallet(address _systemWallet) external onlyOwner {
        if (_systemWallet == address(0)) revert InvalidSystemWallet();
        address old = systemWallet; systemWallet = _systemWallet;
        emit SystemWalletUpdated(old, _systemWallet);
    }

    function setTokenAcceptance(address token, bool accepted) external onlyOwner {
        if (token == address(0)) revert InvalidToken();
        acceptedTokens[token] = accepted;
        emit TokenAcceptanceUpdated(token, accepted);
    }
}
