// SPDX-License-Identifier: MIT
pragma solidity ^0.8.22;

import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

contract ProductEscrow is
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
    uint256 public constant PLATFORM_FEE_BPS = 200;  // 2% of productAmount
    uint256 public constant BUYER_CASHBACK_BPS = 50;  // 0.5% of productAmount
    uint256 public constant UPLINE_FEE_BPS = 50;      // 0.5% of productAmount
    // System fee = 1% (platformFee - buyerCashback - uplineAmount, remainder absorbs rounding)

    // Fixed gas fee in token units ($0.10 for 6-decimal tokens like USDC/USDT)
    uint256 public constant GAS_FEE = 100000; // $0.10 — goes to system wallet

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
        uint256 gasFee = _isMCGP(token) ? 0 : GAS_FEE;
        uint256 totalAmount = productAmount + shippingAmount + platformFee + gasFee;

        IERC20(token).safeTransferFrom(msg.sender, address(this), totalAmount);

        // Gas fee goes directly to system wallet
        if (gasFee > 0) {
            IERC20(token).safeTransfer(owner(), gasFee);
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
    /// after the 30-day deadline, then open to anyone.
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
            IERC20(order.token).safeTransfer(owner(), systemShare);
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

    function _calculatePlatformFee(uint256 productAmount, address token) internal view returns (uint256) {
        if (_isMCGP(token)) {
            return 0;
        }
        return (productAmount * PLATFORM_FEE_BPS) / 10000;
    }

    function _isMCGP(address token) internal view returns (bool) {
        return mcgpToken != address(0) && token == mcgpToken;
    }

    function _releaseToSeller(bytes32 orderId, Order storage order) internal {
        IERC20 token = IERC20(order.token);
        uint256 sellerAmount = order.productAmount + order.shippingAmount;

        if (order.platformFee > 0) {
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
                        token.safeTransfer(owner(), systemAmount + uplineAmount);
                        emit OrderReleased(orderId, order.seller, sellerAmount, systemAmount + uplineAmount, buyerCashback, 0);
                        token.safeTransfer(order.seller, sellerAmount);
                        return;
                    }
                } catch {
                    // Upline is a reverting contract, send their share to system wallet
                    token.safeTransfer(owner(), systemAmount + uplineAmount);
                    emit OrderReleased(orderId, order.seller, sellerAmount, systemAmount + uplineAmount, buyerCashback, 0);
                    token.safeTransfer(order.seller, sellerAmount);
                    return;
                }
                token.safeTransfer(owner(), systemAmount);
            } else {
                // No upline — system gets system + upline share
                token.safeTransfer(owner(), systemAmount + uplineAmount);
            }

            emit OrderReleased(orderId, order.seller, sellerAmount, systemAmount, buyerCashback, uplineAmount);
        } else {
            emit OrderReleased(orderId, order.seller, sellerAmount, 0, 0, 0);
        }

        token.safeTransfer(order.seller, sellerAmount);
    }

    /// @dev Reserved slots for future state variables. When adding a new state
    ///      variable, place it above this gap and shrink the gap by the same
    ///      number of slots. 48 slots = ~1 full "page".
    uint256[47] private __gap;
}
