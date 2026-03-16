// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

contract ProductEscrow is Ownable, ReentrancyGuard {
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

    // Fee basis points (out of 10000)
    uint256 public constant PLATFORM_FEE_BPS = 1000; // 10%
    uint256 public constant SYSTEM_FEE_BPS = 500;    // 5% of productAmount
    uint256 public constant BUYER_CASHBACK_BPS = 250; // 2.5% of productAmount
    uint256 public constant UPLINE_FEE_BPS = 250;    // 2.5% of productAmount

    // Auto-refund split when seller delivered but buyer didn't confirm (basis points of total escrowed)
    uint256 public constant AUTO_REFUND_BUYER_BPS = 9000;  // 90%
    uint256 public constant AUTO_REFUND_SYSTEM_BPS = 700;  // 7%
    uint256 public constant AUTO_REFUND_SELLER_BPS = 300;  // 3%

    mapping(bytes32 => Order) public orders;

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

    constructor(address initialOwner) Ownable(initialOwner) {}

    function createOrder(
        bytes32 orderId,
        address seller,
        address token,
        uint256 productAmount,
        uint256 shippingAmount,
        address buyerUpline
    ) external nonReentrant {
        require(orders[orderId].buyer == address(0), "Order already exists");
        require(seller != address(0), "Invalid seller");
        require(token != address(0), "Invalid token");
        require(productAmount > 0, "Product amount must be > 0");

        uint256 platformFee = _calculatePlatformFee(productAmount, token);
        uint256 totalAmount = productAmount + shippingAmount + platformFee;

        IERC20(token).safeTransferFrom(msg.sender, address(this), totalAmount);

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

    function confirmReceipt(bytes32 orderId) external nonReentrant {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");
        require(msg.sender == order.buyer, "Only buyer can confirm");

        order.buyerConfirmed = true;
        order.resolved = true;

        _releaseToSeller(orderId, order);

        emit ReceiptConfirmed(orderId);
    }

    function requestRefund(bytes32 orderId) external nonReentrant {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");
        require(msg.sender == order.buyer, "Only buyer can request refund");
        require(!order.refundRequested, "Refund already requested");

        if (!order.sellerDelivered) {
            // Instant full refund
            order.resolved = true;
            order.refundRequested = true;
            uint256 totalAmount = order.productAmount + order.shippingAmount + order.platformFee;
            IERC20(order.token).safeTransfer(order.buyer, totalAmount);
            emit OrderRefunded(orderId, order.buyer, totalAmount);
        } else {
            // Dispute state — admin must resolve
            order.refundRequested = true;
            emit RefundRequested(orderId);
        }
    }

    function cancelOrder(bytes32 orderId) external nonReentrant {
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

    function autoRefund(bytes32 orderId) external nonReentrant {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");
        require(block.timestamp >= order.createdAt + AUTO_REFUND_DELAY, "30 days not elapsed");

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

    function adminResolve(bytes32 orderId, bool refundBuyer) external onlyOwner nonReentrant {
        Order storage order = orders[orderId];
        require(order.buyer != address(0), "Order does not exist");
        require(!order.resolved, "Order already resolved");

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
        // Check if token is MCGP — for now we use a simple mapping approach
        // MCGP tokens get 0% platform fee
        if (_isMCGP(token)) {
            return 0;
        }
        return (productAmount * PLATFORM_FEE_BPS) / 10000;
    }

    // MCGP token address — set via constructor or made configurable
    address public mcgpToken;

    function setMcgpToken(address _mcgpToken) external onlyOwner {
        mcgpToken = _mcgpToken;
    }

    function _isMCGP(address token) internal view returns (bool) {
        return mcgpToken != address(0) && token == mcgpToken;
    }

    function _releaseToSeller(bytes32 orderId, Order storage order) internal {
        IERC20 token = IERC20(order.token);
        uint256 sellerAmount = order.productAmount + order.shippingAmount;

        if (order.platformFee > 0) {
            uint256 systemAmount = (order.productAmount * SYSTEM_FEE_BPS) / 10000;
            uint256 buyerCashback = (order.productAmount * BUYER_CASHBACK_BPS) / 10000;
            uint256 uplineAmount = (order.productAmount * UPLINE_FEE_BPS) / 10000;

            token.safeTransfer(owner(), order.buyerUpline == address(0) ? systemAmount + uplineAmount : systemAmount);
            token.safeTransfer(order.buyer, buyerCashback);

            if (order.buyerUpline != address(0)) {
                token.safeTransfer(order.buyerUpline, uplineAmount);
            }

            emit OrderReleased(orderId, order.seller, sellerAmount, systemAmount, buyerCashback, uplineAmount);
        } else {
            emit OrderReleased(orderId, order.seller, sellerAmount, 0, 0, 0);
        }

        token.safeTransfer(order.seller, sellerAmount);
    }
}
