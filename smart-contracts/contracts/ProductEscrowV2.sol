// SPDX-License-Identifier: MIT
pragma solidity ^0.8.22;

import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

import "./interfaces/IFeeManager.sol";

/// @title ProductEscrowV2
/// @notice UUPS implementation upgrade of ProductEscrow that adds the A1 live-read fee model and the
///         `legacySplitEnabled` split gate (see
///         docs/superpowers/specs/2026-06-08-configurable-feemanager-design.md §3.1 / §7.2).
///         The proxy address and full V1 storage layout are preserved; `feeManager` /
///         `legacySplitEnabled` / `tokenSymbol` are APPENDED before `__gap` (which shrinks 47 → 45;
///         `feeManager` (address) + `legacySplitEnabled` (bool) pack into one slot, `tokenSymbol`
///         takes one → 2 new slots).
///
///         `_calculatePlatformFee` reads the merchant bps from `rateFor("product_merchant", sym)` and the
///         flat gas component from `rateFor("product", sym).flatUSD`, clamped to `MAX_PLATFORM_FEE_BPS`
///         and honoring mcgpExempt. When `feeManager == address(0)` it falls back to the legacy
///         `PLATFORM_FEE_BPS`/`GAS_FEE` constants (pre-wire behavior == today). `_releaseToSeller` keeps
///         the legacy cashback/upline/system split behind `legacySplitEnabled` (deployed TRUE); flipped
///         FALSE at cutover (D5) it sends the whole platformFee to `owner()` (the off-chain v2 engine
///         becomes the sole splitter). Reversible by flipping back.
contract ProductEscrowV2 is
    Initializable,
    UUPSUpgradeable,
    OwnableUpgradeable,
    ReentrancyGuard,
    PausableUpgradeable
{
    using SafeERC20 for IERC20;

    struct Order {
        address buyer;
        address seller;
        address token;
        uint256 productAmount;
        uint256 shippingAmount;
        uint256 platformFee;
        address buyerUpline;
        uint256 createdAt;
        bool sellerDelivered;
        bool buyerConfirmed;
        bool refundRequested;
        bool resolved;
    }

    uint256 public constant AUTO_REFUND_DELAY = 30 days;
    uint256 public constant AUTO_REFUND_GRACE_PERIOD = 3 days;

    // Fee basis points (out of 10000)
    // Merchant fee: 2% baked into listing price, paid by merchant not buyer
    // KEPT as the `feeManager == address(0)` fallback (pre-wire behavior == today).
    uint256 public constant PLATFORM_FEE_BPS = 200;  // 2% of productAmount
    uint256 public constant BUYER_CASHBACK_BPS = 50;  // 0.5% of productAmount — legacy-split branch
    uint256 public constant UPLINE_FEE_BPS = 50;      // 0.5% of productAmount — legacy-split branch
    // System fee = 1% (platformFee - buyerCashback - uplineAmount, remainder absorbs rounding)

    // Fixed gas fee in token units ($0.10 for 6-decimal tokens like USDC/USDT).
    // KEPT as the `feeManager == address(0)` fallback.
    uint256 public constant GAS_FEE = 100000; // $0.10 — goes to system wallet

    // A1 escrow-side cap on the FeeManager merchant bps so a misconfig can't strand funds (§7.2).
    uint256 public constant MAX_PLATFORM_FEE_BPS = 300; // 3%

    // Feature keys (match the off-chain DefaultFeeConfigs seed rows — §7.2).
    string private constant FEATURE_PRODUCT_MERCHANT = "product_merchant";
    string private constant FEATURE_PRODUCT_GAS = "product";

    // Auto-refund split when seller delivered but buyer didn't confirm (basis points of total escrowed)
    uint256 public constant AUTO_REFUND_BUYER_BPS = 9000;  // 90%
    uint256 public constant AUTO_REFUND_SYSTEM_BPS = 700;  // 7%
    uint256 public constant AUTO_REFUND_SELLER_BPS = 300;  // 3%

    // MCGP token address
    address public mcgpToken;

    // Token whitelist
    mapping(address => bool) public acceptedTokens;

    mapping(bytes32 => Order) public orders;

    /// @dev Addresses authorized to call adminResolve. Owner is always implicitly
    ///      admin (see isAdmin) and does not need to be added to this mapping.
    mapping(address => bool) public admins;

    // ── V2 appended storage (A1 live-read + legacy-split gate, §3.1 / §7.2) ──
    /// @notice FeeManager proxy for this chain (the on-chain rate source of truth). +1 slot.
    address public feeManager;
    /// @notice Deployed TRUE; flipped FALSE at cutover (D5). Gates the on-chain platform-fee split. +1 slot.
    bool    public legacySplitEnabled;
    /// @notice Owner-set token symbol per accepted token, used as the FeeManager key. +1 slot.
    mapping(address => string) public tokenSymbol;

    event OrderCreated(
        bytes32 indexed orderId,
        address indexed buyer,
        address indexed seller,
        address token,
        uint256 productAmount,
        uint256 shippingAmount,
        uint256 platformFee
    );
    event DeliveryMarked(bytes32 indexed orderId);
    event ReceiptConfirmed(bytes32 indexed orderId);
    event RefundRequested(bytes32 indexed orderId);
    event OrderRefunded(bytes32 indexed orderId, address indexed buyer, uint256 amount);
    event OrderReleased(
        bytes32 indexed orderId,
        address indexed seller,
        uint256 sellerAmount,
        uint256 systemAmount,
        uint256 buyerCashback,
        uint256 uplineAmount
    );
    event OrderCancelled(bytes32 indexed orderId);
    event AutoRefunded(bytes32 indexed orderId);
    event DisputeResolved(bytes32 indexed orderId, bool refundedToBuyer);
    event TokenAcceptanceUpdated(address indexed token, bool accepted);
    event McgpTokenUpdated(address indexed oldToken, address indexed newToken);
    event ExcessSwept(address indexed token, uint256 amount);
    event AdminAdded(address indexed account);
    event AdminRemoved(address indexed account);
    // V2 additions
    event FeeManagerUpdated(address indexed oldFm, address indexed newFm);
    event LegacySplitEnabledUpdated(bool enabled);
    event TokenSymbolUpdated(address indexed token, string symbol);

    modifier onlyAdmin() {
        require(isAdmin(msg.sender), "Not admin");
        _;
    }

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(
        address initialOwner,
        address[] calldata initialAdmins
    ) external initializer {
        __Ownable_init(initialOwner);
        __Pausable_init();

        legacySplitEnabled = true; // deployed TRUE (today's behavior — §9 cutover)

        for (uint256 i = 0; i < initialAdmins.length; i++) {
            address a = initialAdmins[i];
            require(a != address(0), "Zero address");
            require(!admins[a], "Already admin");
            admins[a] = true;
            emit AdminAdded(a);
        }
    }

    /// @dev Restricts upgrade authority to owner. Admin role does not grant upgrade rights.
    function _authorizeUpgrade(address) internal override onlyOwner {}

    /// @notice One-shot V2 re-initializer, run via upgradeProxy's `call` option at the V1→V2 upgrade.
    /// @dev `upgradeProxy` does NOT re-run `initialize` (initializer modifier), so the freshly-added
    ///      `legacySplitEnabled` slot would otherwise read zero=false — silently bypassing the
    ///      buyer-cashback/upline/system split on the first confirmReceipt after upgrade (100% to
    ///      owner). `reinitializer(2)` runs exactly once on the live proxy to set it TRUE, honoring
    ///      the spec D5 / §9-step-1 "deployed TRUE = byte-for-byte today" guarantee.
    function initializeV2() external reinitializer(2) {
        legacySplitEnabled = true;
    }

    // --- Admin functions ---

    function addAdmin(address account) external onlyOwner {
        require(account != address(0), "Zero address");
        require(!admins[account], "Already admin");
        admins[account] = true;
        emit AdminAdded(account);
    }

    function removeAdmin(address account) external onlyOwner {
        require(admins[account], "Not admin");
        admins[account] = false;
        emit AdminRemoved(account);
    }

    function isAdmin(address account) public view returns (bool) {
        return admins[account] || account == owner();
    }

    function setMcgpToken(address _mcgpToken) external onlyOwner {
        require(_mcgpToken != address(0), "Invalid MCGP address");
        address old = mcgpToken;
        mcgpToken = _mcgpToken;
        emit McgpTokenUpdated(old, _mcgpToken);
    }

    function setTokenAcceptance(address token, bool accepted) external onlyOwner {
        require(token != address(0), "Invalid token");
        acceptedTokens[token] = accepted;
        emit TokenAcceptanceUpdated(token, accepted);
    }

    // --- V2 owner setters (A1 live-read wiring + split gate, §3.1 / §7.2) ---

    /// @notice Wire the per-chain FeeManager. Set once at upgrade (§3.1).
    function setFeeManager(address fm) external onlyOwner {
        require(fm != address(0), "fm=0");
        address old = feeManager; feeManager = fm; emit FeeManagerUpdated(old, fm);
    }

    /// @notice Toggle the legacy on-chain platform-fee split (deployed TRUE; flipped FALSE at cutover — D5).
    function setLegacySplitEnabled(bool on) external onlyOwner {
        legacySplitEnabled = on; emit LegacySplitEnabledUpdated(on);
    }

    /// @notice Set the FeeManager-keying symbol for an accepted token (owner-set, §3.1).
    function setTokenSymbol(address token, string calldata sym) external onlyOwner {
        require(token != address(0), "Invalid token");
        tokenSymbol[token] = sym;
        emit TokenSymbolUpdated(token, sym);
    }

    event SystemWalletUpdated(address indexed oldWallet, address indexed newWallet);

    /// @notice Point platform fees at the system fee wallet. Zero is rejected: clear it by setting `owner()` explicitly.
    function setSystemWallet(address wallet) external onlyOwner {
        require(wallet != address(0), "Zero address");
        address old = systemWallet;
        systemWallet = wallet;
        emit SystemWalletUpdated(old, wallet);
    }

    /// @dev Fee destination: the system wallet once set, else the owner as before.
    function _feeRecipient() internal view returns (address) {
        address sw = systemWallet;
        return sw == address(0) ? owner() : sw;
    }

    function pause() external onlyOwner {
        _pause();
    }

    function unpause() external onlyOwner {
        _unpause();
    }

    /// @notice Sweep excess tokens (rounding dust or accidental transfers) to owner
    function sweepExcess(address token, uint256 amount) external onlyOwner {
        require(token != address(0), "Invalid token");
        IERC20(token).safeTransfer(owner(), amount);
        emit ExcessSwept(token, amount);
    }

    // --- Order lifecycle ---

    function createOrder(
        bytes32 orderId,
        address seller,
        address token,
        uint256 productAmount,
        uint256 shippingAmount,
        address buyerUpline
    ) external nonReentrant whenNotPaused {
        require(orders[orderId].buyer == address(0), "Order already exists");
        require(seller != address(0), "Invalid seller");
        require(msg.sender != seller, "Buyer cannot be seller");
        require(token != address(0), "Invalid token");
        require(acceptedTokens[token], "Token not accepted");
        require(productAmount > 0, "Product amount must be > 0");

        uint256 platformFee = _calculatePlatformFee(productAmount, token);
        uint256 gasFee = _gasFee(token);
        uint256 totalAmount = productAmount + shippingAmount + platformFee + gasFee;

        IERC20(token).safeTransferFrom(msg.sender, address(this), totalAmount);

        // Gas fee goes directly to system wallet
        if (gasFee > 0) {
            IERC20(token).safeTransfer(_feeRecipient(), gasFee);
        }

        orders[orderId] = Order({
            buyer: msg.sender,
            seller: seller,
            token: token,
            productAmount: productAmount,
            shippingAmount: shippingAmount,
            platformFee: platformFee,
            buyerUpline: buyerUpline,
            createdAt: block.timestamp,
            sellerDelivered: false,
            buyerConfirmed: false,
            refundRequested: false,
            resolved: false
        });

        emit OrderCreated(orderId, msg.sender, seller, token, productAmount, shippingAmount, platformFee);
    }

    function markDelivered(bytes32 orderId) external {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");
        require(msg.sender == order.seller, "Only seller can mark delivered");
        require(!order.sellerDelivered, "Already marked delivered");

        order.sellerDelivered = true;
        emit DeliveryMarked(orderId);
    }

    function confirmReceipt(bytes32 orderId) external nonReentrant whenNotPaused {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");
        require(msg.sender == order.buyer, "Only buyer can confirm");

        order.buyerConfirmed = true;
        order.resolved = true;

        _releaseToSeller(orderId, order);
        emit ReceiptConfirmed(orderId);
    }

    function requestRefund(bytes32 orderId) external nonReentrant whenNotPaused {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");
        require(msg.sender == order.buyer, "Only buyer can request refund");
        require(!order.refundRequested, "Refund already requested");

        order.refundRequested = true;

        if (!order.sellerDelivered) {
            // Instant full refund
            order.resolved = true;
            uint256 totalAmount = order.productAmount + order.shippingAmount + order.platformFee;
            IERC20(order.token).safeTransfer(order.buyer, totalAmount);
            emit OrderRefunded(orderId, order.buyer, totalAmount);
        } else {
            // Dispute state — admin must resolve
            emit RefundRequested(orderId);
        }
    }

    function cancelOrder(bytes32 orderId) external nonReentrant whenNotPaused {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");
        require(msg.sender == order.seller, "Only seller can cancel");
        require(!order.sellerDelivered, "Cannot cancel after delivery");

        order.resolved = true;
        uint256 totalAmount = order.productAmount + order.shippingAmount + order.platformFee;
        IERC20(order.token).safeTransfer(order.buyer, totalAmount);

        emit OrderCancelled(orderId);
        emit OrderRefunded(orderId, order.buyer, totalAmount);
    }

    /// @notice Auto-refund after 30 days. Restricted to buyer/owner for the first 3 days
    /// after the 30-day deadline, then open to anyone. AUTO_REFUND_* splits are untouched (§7.2).
    function autoRefund(bytes32 orderId) external nonReentrant whenNotPaused {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");
        require(block.timestamp >= order.createdAt + AUTO_REFUND_DELAY, "30 days not elapsed");

        // Grace period: only buyer or owner can call during first 3 days after deadline
        if (block.timestamp < order.createdAt + AUTO_REFUND_DELAY + AUTO_REFUND_GRACE_PERIOD) {
            require(
                msg.sender == order.buyer || msg.sender == owner(),
                "Grace period: only buyer or owner"
            );
        }

        order.resolved = true;
        uint256 totalAmount = order.productAmount + order.shippingAmount + order.platformFee;

        if (!order.sellerDelivered) {
            // 100% refund to buyer
            IERC20(order.token).safeTransfer(order.buyer, totalAmount);
            emit OrderRefunded(orderId, order.buyer, totalAmount);
        } else {
            // Seller delivered but buyer didn't confirm: 90% buyer, 7% system, 3% seller
            uint256 buyerShare = (totalAmount * AUTO_REFUND_BUYER_BPS) / 10000;
            uint256 systemShare = (totalAmount * AUTO_REFUND_SYSTEM_BPS) / 10000;
            uint256 sellerShare = totalAmount - buyerShare - systemShare; // remainder to seller

            IERC20(order.token).safeTransfer(order.buyer, buyerShare);
            IERC20(order.token).safeTransfer(_feeRecipient(), systemShare);
            IERC20(order.token).safeTransfer(order.seller, sellerShare);
        }

        emit AutoRefunded(orderId);
    }

    /// @notice Admin resolves a disputed order (buyer requested refund after seller delivered)
    function adminResolve(bytes32 orderId, bool refundBuyer) external onlyAdmin nonReentrant {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");
        require(order.refundRequested, "No dispute to resolve");

        order.resolved = true;

        if (refundBuyer) {
            uint256 totalAmount = order.productAmount + order.shippingAmount + order.platformFee;
            IERC20(order.token).safeTransfer(order.buyer, totalAmount);
            emit OrderRefunded(orderId, order.buyer, totalAmount);
        } else {
            _releaseToSeller(orderId, order);
        }

        emit DisputeResolved(orderId, refundBuyer);
    }

    // --- Internal helpers ---

    /// @dev A1 live-read: merchant bps from FeeManager `rateFor("product_merchant", sym)`, clamped
    ///      to MAX_PLATFORM_FEE_BPS and honoring mcgpExempt. Falls back to PLATFORM_FEE_BPS when
    ///      `feeManager == address(0)` (pre-wire == today) OR when the rate's `active` flag is false
    ///      (an UNSET / disabled key — see FeeManager.sol:75-76). Honoring `active` prevents a silent
    ///      0% merchant fee if the FeeManager is wired before this token's rate is seeded.
    function _calculatePlatformFee(uint256 productAmount, address token) internal view returns (uint256) {
        if (_isMCGP(token)) {
            return 0;
        }
        if (feeManager == address(0)) {
            return (productAmount * PLATFORM_FEE_BPS) / 10000;
        }
        (uint16 bps, , bool exempt, bool active) = IFeeManager(feeManager).rateFor(FEATURE_PRODUCT_MERCHANT, tokenSymbol[token]);
        if (!active) {
            // Not configured — fall back to legacy rather than charge 0 silently.
            return (productAmount * PLATFORM_FEE_BPS) / 10000;
        }
        if (exempt) {
            return 0;
        }
        require(bps <= MAX_PLATFORM_FEE_BPS, "Platform fee too high");
        return (productAmount * uint256(bps)) / 10000;
    }

    /// @dev A1 live-read: flat gas from FeeManager `rateFor("product", sym).flatUSD`, honoring
    ///      mcgpExempt. Falls back to GAS_FEE when `feeManager == address(0)` (pre-wire == today) OR
    ///      when the rate's `active` flag is false (an UNSET / disabled key) — never a silent $0 gas.
    function _gasFee(address token) internal view returns (uint256) {
        if (_isMCGP(token)) {
            return 0;
        }
        if (feeManager == address(0)) {
            return GAS_FEE;
        }
        (, uint64 flatUSD, bool exempt, bool active) = IFeeManager(feeManager).rateFor(FEATURE_PRODUCT_GAS, tokenSymbol[token]);
        if (!active) {
            // Not configured — fall back to legacy rather than charge 0 silently.
            return GAS_FEE;
        }
        if (exempt) {
            return 0;
        }
        return uint256(flatUSD);
    }

    function _isMCGP(address token) internal view returns (bool) {
        return mcgpToken != address(0) && token == mcgpToken;
    }

    function _releaseToSeller(bytes32 orderId, Order storage order) internal {
        IERC20 token = IERC20(order.token);
        uint256 sellerAmount = order.productAmount + order.shippingAmount;

        if (order.platformFee > 0) {
            if (legacySplitEnabled) {
                // EXISTING behavior — buyerCashback/uplineAmount/systemAmount split.
                // Calculate smaller splits first, system gets remainder (absorbs rounding dust)
                uint256 buyerCashback = (order.productAmount * BUYER_CASHBACK_BPS) / 10000;
                uint256 uplineAmount = (order.productAmount * UPLINE_FEE_BPS) / 10000;
                uint256 systemAmount = order.platformFee - buyerCashback - uplineAmount;

                // Transfer fee splits — upline uses try-catch to prevent reverting recipients from blocking resolution
                token.safeTransfer(order.buyer, buyerCashback);

                if (order.buyerUpline != address(0)) {
                    try token.transfer(order.buyerUpline, uplineAmount) returns (bool success) {
                        if (!success) {
                            // Upline transfer failed, send to system wallet
                            token.safeTransfer(_feeRecipient(), systemAmount + uplineAmount);
                            emit OrderReleased(orderId, order.seller, sellerAmount, systemAmount + uplineAmount, buyerCashback, 0);
                            token.safeTransfer(order.seller, sellerAmount);
                            return;
                        }
                    } catch {
                        // Upline is a reverting contract, send their share to system wallet
                        token.safeTransfer(_feeRecipient(), systemAmount + uplineAmount);
                        emit OrderReleased(orderId, order.seller, sellerAmount, systemAmount + uplineAmount, buyerCashback, 0);
                        token.safeTransfer(order.seller, sellerAmount);
                        return;
                    }
                    token.safeTransfer(_feeRecipient(), systemAmount);
                } else {
                    // No upline — system gets system + upline share
                    token.safeTransfer(_feeRecipient(), systemAmount + uplineAmount);
                }

                emit OrderReleased(orderId, order.seller, sellerAmount, systemAmount, buyerCashback, uplineAmount);
            } else {
                // D5 cutover: 100% of the platform fee to system; off-chain v2 engine splits.
                token.safeTransfer(_feeRecipient(), order.platformFee);
                emit OrderReleased(orderId, order.seller, sellerAmount, order.platformFee, 0, 0);
            }
        } else {
            emit OrderReleased(orderId, order.seller, sellerAmount, 0, 0, 0);
        }

        token.safeTransfer(order.seller, sellerAmount);
    }

    /// @dev Reserved slots for future state variables. When adding a new state
    ///      variable, place it above this gap and shrink the gap by the same
    ///      number of slots. V2 added feeManager+legacySplitEnabled (PACK into one slot) and
    ///      tokenSymbol (one slot) → 2 new slots → 47 - 2 = 45. (OZ plugin authoritative.)
    /// @notice Where platform fees are paid. address(0) = fall back to `owner()` (pre-upgrade behaviour).
    address public systemWallet;

    uint256[44] private __gap;
}
