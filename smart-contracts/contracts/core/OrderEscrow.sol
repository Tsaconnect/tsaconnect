// SPDX-License-Identifier: MIT
pragma solidity 0.8.24;

import {EscrowBase} from "./EscrowBase.sol";
import {IFeeConfig} from "./interfaces/IFeeConfig.sol";

/// @title OrderEscrow
/// @notice Holds a buyer's payment for a product or paid-service order until it is delivered,
///         then pays the seller minus the merchant fee. Replaces ProductEscrowV2 / ServiceContactV2.
/// @dev    Money only moves to the buyer, the seller or the FeeConfig fee recipient. Cashback,
///         TradePoints and referral shares are off-chain accounting and never touch this contract.
///
///         Lifecycle (one order per backend order id):
///           Funded ─markDelivered→ Delivered ─confirmReceipt / confirm window passes→ Released
///           Funded ─confirmReceipt→ Released
///           Funded ─cancel (seller) / delivery window passes→ Refunded
///           Delivered ─dispute (buyer, inside confirm window)→ Disputed
///           Disputed ─resolve (resolver)→ Resolved   ─backstop passes→ Refunded
///         Every non-terminal state reaches a terminal one without an admin, and no exit is paused.
///
///         Fees: the merchant fee (bps of the product price) is charged to the SELLER and
///         snapshotted as an amount at creation; the flat fee is charged to the buyer at creation,
///         paid straight to the fee recipient and is not refundable (it covers gas already spent).
contract OrderEscrow is EscrowBase {
    bytes32 public constant RESOLVER_ROLE = keccak256("RESOLVER_ROLE");

    /// @notice FeeConfig feature key for orders: `bps` is the merchant fee, `flat` the buyer's flat fee.
    bytes32 public constant FEATURE = "order";

    uint16 public constant MAX_FEE_BPS = 300;
    uint8 public constant MIN_HANDLING_DAYS = 1;
    uint8 public constant MAX_HANDLING_DAYS = 30;
    uint40 public constant CONFIRM_WINDOW = 7 days;
    uint40 public constant DISPUTE_BACKSTOP = 14 days;

    enum State {
        None,
        Funded,
        Delivered,
        Disputed,
        Released,
        Refunded,
        Resolved
    }

    enum RefundReason {
        Cancelled,
        NotDelivered,
        DisputeBackstop
    }

    /// @dev Packed into 4 slots. Deadlines are absolute timestamps fixed when each phase starts.
    struct Order {
        address buyer;
        uint40 createdAt;
        uint40 deliverBy;
        State state;
        address seller;
        uint40 confirmBy;
        uint40 resolveBy;
        address token;
        uint96 merchantFee;
        uint128 productAmount;
        uint128 shippingAmount;
    }

    /// @custom:storage-location erc7201:tsa.storage.OrderEscrow
    struct OrderEscrowStorage {
        mapping(bytes32 orderId => Order) orders;
    }

    // keccak256(abi.encode(uint256(keccak256("tsa.storage.OrderEscrow")) - 1)) & ~bytes32(uint256(0xff))
    bytes32 private constant STORAGE_SLOT = 0xeaafaec88ecb23d8c6b2aca7eb09458862bdce65a9d62fddd15377e70e9e4c00;

    event OrderCreated(
        bytes32 indexed orderId,
        address indexed buyer,
        address indexed seller,
        address token,
        uint128 productAmount,
        uint128 shippingAmount,
        uint96 merchantFee,
        uint128 flatFee,
        uint40 deliverBy
    );
    event OrderDelivered(bytes32 indexed orderId, uint40 confirmBy);
    event OrderDisputed(bytes32 indexed orderId, uint40 resolveBy);
    event OrderReleased(
        bytes32 indexed orderId, address indexed seller, uint256 sellerAmount, uint256 fee, address feeRecipient
    );
    event OrderRefunded(bytes32 indexed orderId, address indexed buyer, uint256 amount, RefundReason reason);
    event OrderResolved(
        bytes32 indexed orderId, address indexed resolver, uint256 toBuyer, uint256 toSeller, uint256 fee
    );

    error OrderExists(bytes32 orderId);
    error InvalidState(bytes32 orderId, State state);
    error NotBuyer(bytes32 orderId);
    error NotSeller(bytes32 orderId);
    error InvalidSeller();
    error ZeroAmount();
    error InvalidHandlingDays(uint8 days_);
    error FeeAboveCap(uint16 bps);
    error OrderAboveLimit(uint256 amount, uint256 limit);
    error AmountTooLarge();
    error WindowOpen(bytes32 orderId, uint40 until);
    error WindowClosed(bytes32 orderId, uint40 at);
    error ToBuyerAboveTotal(uint256 toBuyer, uint256 total);

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor(address forwarder, IFeeConfig feeConfig_) EscrowBase(forwarder, feeConfig_) {}

    /// @param admin Holds every role, including RESOLVER (grant it to console wallets separately).
    function initialize(address admin) external initializer {
        __EscrowBase_init(admin);
        _grantRole(RESOLVER_ROLE, admin);
    }

    // ── Buyer / seller actions ─────────────────────────────────────────────────

    /// @notice Pay for an order. Pulls `productAmount + shippingAmount + flat fee` from the buyer.
    /// @param handlingDays Days the seller has to mark delivery (from the listing), 1–30.
    function createOrder(
        bytes32 orderId,
        address seller,
        address token,
        uint128 productAmount,
        uint128 shippingAmount,
        uint8 handlingDays
    ) external nonReentrant whenNotPaused {
        address buyer = _msgSender();
        OrderEscrowStorage storage s = _s();
        if (s.orders[orderId].state != State.None) revert OrderExists(orderId);
        if (seller == address(0) || seller == buyer) revert InvalidSeller();
        if (productAmount == 0) revert ZeroAmount();
        if (handlingDays < MIN_HANDLING_DAYS || handlingDays > MAX_HANDLING_DAYS) {
            revert InvalidHandlingDays(handlingDays);
        }

        IFeeConfig.Rate memory rate = feeConfig.rateFor(FEATURE, token);
        if (rate.bps > MAX_FEE_BPS) revert FeeAboveCap(rate.bps);
        IFeeConfig.Limits memory limits = feeConfig.limitsFor(token);

        uint256 gross = uint256(productAmount) + shippingAmount;
        if (gross > limits.maxPerOrder) revert OrderAboveLimit(gross, limits.maxPerOrder);
        uint256 fee = (uint256(productAmount) * rate.bps) / 10_000;
        if (fee > type(uint96).max) revert AmountTooLarge();

        uint40 deliverBy = uint40(block.timestamp) + uint40(handlingDays) * 1 days;
        s.orders[orderId] = Order({
            buyer: buyer,
            createdAt: uint40(block.timestamp),
            deliverBy: deliverBy,
            state: State.Funded,
            seller: seller,
            confirmBy: 0,
            resolveBy: 0,
            token: token,
            merchantFee: uint96(fee),
            productAmount: productAmount,
            shippingAmount: shippingAmount
        });
        _addOwed(token, gross, limits.maxOwed);

        _pull(token, buyer, gross + rate.flat);
        _push(token, feeConfig.feeRecipient(), rate.flat);

        emit OrderCreated(orderId, buyer, seller, token, productAmount, shippingAmount, uint96(fee), rate.flat, deliverBy);
    }

    /// @notice Seller marks the order delivered; the buyer then has CONFIRM_WINDOW to confirm or dispute.
    function markDelivered(bytes32 orderId) external {
        Order storage o = _order(orderId);
        if (_msgSender() != o.seller) revert NotSeller(orderId);
        if (o.state != State.Funded) revert InvalidState(orderId, o.state);
        uint40 confirmBy = uint40(block.timestamp) + CONFIRM_WINDOW;
        o.state = State.Delivered;
        o.confirmBy = confirmBy;
        emit OrderDelivered(orderId, confirmBy);
    }

    /// @notice Buyer accepts the order and releases payment to the seller.
    function confirmReceipt(bytes32 orderId) external nonReentrant {
        Order storage o = _order(orderId);
        if (_msgSender() != o.buyer) revert NotBuyer(orderId);
        if (o.state != State.Funded && o.state != State.Delivered) revert InvalidState(orderId, o.state);
        _release(orderId, o);
    }

    /// @notice Seller cancels before delivery; the buyer gets the full order amount back.
    function cancel(bytes32 orderId) external nonReentrant {
        Order storage o = _order(orderId);
        if (_msgSender() != o.seller) revert NotSeller(orderId);
        if (o.state != State.Funded) revert InvalidState(orderId, o.state);
        _refund(orderId, o, RefundReason.Cancelled);
    }

    /// @notice Buyer disputes a delivered order, inside the confirm window.
    function dispute(bytes32 orderId) external {
        Order storage o = _order(orderId);
        if (_msgSender() != o.buyer) revert NotBuyer(orderId);
        if (o.state != State.Delivered) revert InvalidState(orderId, o.state);
        if (block.timestamp > o.confirmBy) revert WindowClosed(orderId, o.confirmBy);
        uint40 resolveBy = uint40(block.timestamp) + DISPUTE_BACKSTOP;
        o.state = State.Disputed;
        o.resolveBy = resolveBy;
        emit OrderDisputed(orderId, resolveBy);
    }

    /// @notice Anyone can settle an order whose window has passed:
    ///         not delivered by `deliverBy` → refund buyer; delivered and not disputed by
    ///         `confirmBy` → release to seller; disputed and unresolved by `resolveBy` → refund buyer.
    function claimTimeout(bytes32 orderId) external nonReentrant {
        Order storage o = _order(orderId);
        State st = o.state;
        if (st == State.Funded) {
            _requirePassed(orderId, o.deliverBy);
            _refund(orderId, o, RefundReason.NotDelivered);
        } else if (st == State.Delivered) {
            _requirePassed(orderId, o.confirmBy);
            _release(orderId, o);
        } else if (st == State.Disputed) {
            _requirePassed(orderId, o.resolveBy);
            _refund(orderId, o, RefundReason.DisputeBackstop);
        } else {
            revert InvalidState(orderId, st);
        }
    }

    // ── Resolver ───────────────────────────────────────────────────────────────

    /// @notice Settle a dispute by splitting the order's own funds. The merchant fee is charged
    ///         pro rata on the seller's share.
    /// @param toBuyer Amount of the order total returned to the buyer (0 … product + shipping).
    function resolve(bytes32 orderId, uint128 toBuyer) external onlyRole(RESOLVER_ROLE) nonReentrant {
        Order storage o = _order(orderId);
        if (o.state != State.Disputed) revert InvalidState(orderId, o.state);
        uint256 gross = uint256(o.productAmount) + o.shippingAmount;
        if (toBuyer > gross) revert ToBuyerAboveTotal(toBuyer, gross);

        uint256 sellerGross = gross - toBuyer;
        uint256 fee = (uint256(o.merchantFee) * sellerGross) / gross;
        address token = o.token;
        o.state = State.Resolved;
        _subOwed(token, gross);

        _push(token, o.buyer, toBuyer);
        _push(token, o.seller, sellerGross - fee);
        _push(token, feeConfig.feeRecipient(), fee);
        emit OrderResolved(orderId, _msgSender(), toBuyer, sellerGross - fee, fee);
    }

    // ── Views ──────────────────────────────────────────────────────────────────

    function getOrder(bytes32 orderId) external view returns (Order memory) {
        return _s().orders[orderId];
    }

    // ── Internals ──────────────────────────────────────────────────────────────

    function _release(bytes32 orderId, Order storage o) private {
        uint256 gross = uint256(o.productAmount) + o.shippingAmount;
        uint256 fee = o.merchantFee;
        address token = o.token;
        address recipient = feeConfig.feeRecipient();
        o.state = State.Released;
        _subOwed(token, gross);

        _push(token, o.seller, gross - fee);
        _push(token, recipient, fee);
        emit OrderReleased(orderId, o.seller, gross - fee, fee, recipient);
    }

    function _refund(bytes32 orderId, Order storage o, RefundReason reason) private {
        uint256 gross = uint256(o.productAmount) + o.shippingAmount;
        address token = o.token;
        o.state = State.Refunded;
        _subOwed(token, gross);

        _push(token, o.buyer, gross);
        emit OrderRefunded(orderId, o.buyer, gross, reason);
    }

    function _requirePassed(bytes32 orderId, uint40 deadline) private view {
        if (block.timestamp <= deadline) revert WindowOpen(orderId, deadline);
    }

    /// @dev Existing order or revert; every action below targets a created order.
    function _order(bytes32 orderId) private view returns (Order storage o) {
        o = _s().orders[orderId];
        if (o.state == State.None) revert InvalidState(orderId, State.None);
    }

    function _s() private pure returns (OrderEscrowStorage storage s) {
        assembly {
            s.slot := STORAGE_SLOT
        }
    }
}
