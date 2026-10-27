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
///         Orders are stored under `orderKey(buyer, orderId)`, so nobody can claim a buyer's order
///         id before them. Every action after creation takes that key.
///
///         Lifecycle:
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
        mapping(bytes32 key => Order) orders;
    }

    // keccak256(abi.encode(uint256(keccak256("tsa.storage.OrderEscrow")) - 1)) & ~bytes32(uint256(0xff))
    bytes32 private constant STORAGE_SLOT = 0xeaafaec88ecb23d8c6b2aca7eb09458862bdce65a9d62fddd15377e70e9e4c00;

    event OrderCreated(
        bytes32 indexed key,
        bytes32 indexed orderId,
        address indexed buyer,
        address seller,
        address token,
        uint128 productAmount,
        uint128 shippingAmount,
        uint96 merchantFee,
        uint128 flatFee,
        uint40 deliverBy
    );
    event OrderDelivered(bytes32 indexed key, uint40 confirmBy);
    event OrderDisputed(bytes32 indexed key, uint40 resolveBy);
    event OrderReleased(
        bytes32 indexed key, address indexed seller, uint256 sellerAmount, uint256 fee, address feeRecipient
    );
    event OrderRefunded(bytes32 indexed key, address indexed buyer, uint256 amount, RefundReason reason);
    event OrderResolved(
        bytes32 indexed key, address indexed resolver, uint256 toBuyer, uint256 toSeller, uint256 fee
    );

    error OrderExists(bytes32 orderId);
    error InvalidState(bytes32 key, State state);
    error NotBuyer(bytes32 key);
    error NotSeller(bytes32 key);
    error InvalidSeller();
    error ZeroAmount();
    error InvalidHandlingDays(uint8 days_);
    error FeeAboveCap(uint16 bps);
    error FlatFeeAboveMax(uint256 flat, uint256 maxFlatFee);
    error OrderAboveLimit(uint256 amount, uint256 limit);
    error AmountTooLarge();
    error WindowOpen(bytes32 key, uint40 until);
    error WindowClosed(bytes32 key, uint40 at);
    error ToBuyerAboveTotal(uint256 toBuyer, uint256 total);

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor(address forwarder, IFeeConfig feeConfig_) EscrowBase(forwarder, feeConfig_) {}

    /// @param admin     Holds every role, including RESOLVER.
    /// @param resolvers Dispute resolvers (console wallets), granted RESOLVER_ROLE.
    /// @param pausers   Ops keys allowed to pause (not unpause).
    function initialize(address admin, address[] calldata resolvers, address[] calldata pausers) external initializer {
        __EscrowBase_init(admin, pausers);
        _grantRole(RESOLVER_ROLE, admin);
        _grantAll(RESOLVER_ROLE, resolvers);
    }

    // ── Buyer / seller actions ─────────────────────────────────────────────────

    /// @notice The storage key of `buyer`'s order `orderId`.
    function orderKey(address buyer, bytes32 orderId) public pure returns (bytes32) {
        return keccak256(abi.encode(buyer, orderId));
    }

    /// @notice Pay for an order. Pulls `productAmount + shippingAmount + flat fee` from the buyer.
    /// @param handlingDays Days the seller has to mark delivery (from the listing), 1–30.
    /// @param maxFlatFee   The most the buyer agrees to pay as the flat fee; protects a signed
    ///                     (e.g. gasless) request from a fee change before it executes.
    /// @return key The order's key for every later action.
    function createOrder(
        bytes32 orderId,
        address seller,
        address token,
        uint128 productAmount,
        uint128 shippingAmount,
        uint8 handlingDays,
        uint256 maxFlatFee
    ) external nonReentrant whenNotPaused returns (bytes32 key) {
        address buyer = _msgSender();
        key = orderKey(buyer, orderId);
        if (_s().orders[key].state != State.None) revert OrderExists(orderId);
        if (seller == address(0) || seller == buyer) revert InvalidSeller();
        if (productAmount == 0) revert ZeroAmount();
        if (handlingDays < MIN_HANDLING_DAYS || handlingDays > MAX_HANDLING_DAYS) {
            revert InvalidHandlingDays(handlingDays);
        }
        (uint96 fee, uint128 flat) = _priceAndReserve(token, productAmount, shippingAmount, maxFlatFee);

        Order storage o = _s().orders[key];
        o.buyer = buyer;
        o.createdAt = uint40(block.timestamp);
        o.deliverBy = uint40(block.timestamp) + uint40(handlingDays) * 1 days;
        o.state = State.Funded;
        o.seller = seller;
        o.token = token;
        o.merchantFee = fee;
        o.productAmount = productAmount;
        o.shippingAmount = shippingAmount;

        _pull(token, buyer, uint256(productAmount) + shippingAmount + flat);
        _push(token, feeConfig.feeRecipient(), flat);
        _emitCreated(key, orderId, o, flat);
    }

    /// @notice Seller marks the order delivered, by the delivery deadline; the buyer then has
    ///         CONFIRM_WINDOW to confirm or dispute. After the deadline the buyer's refund is due,
    ///         so a late mark (which would restart the clock) is refused.
    function markDelivered(bytes32 key) external {
        Order storage o = _order(key);
        if (_msgSender() != o.seller) revert NotSeller(key);
        if (o.state != State.Funded) revert InvalidState(key, o.state);
        if (block.timestamp > o.deliverBy) revert WindowClosed(key, o.deliverBy);
        uint40 confirmBy = uint40(block.timestamp) + CONFIRM_WINDOW;
        o.state = State.Delivered;
        o.confirmBy = confirmBy;
        emit OrderDelivered(key, confirmBy);
    }

    /// @notice Buyer accepts the order and releases payment to the seller.
    function confirmReceipt(bytes32 key) external nonReentrant {
        Order storage o = _order(key);
        if (_msgSender() != o.buyer) revert NotBuyer(key);
        if (o.state != State.Funded && o.state != State.Delivered) revert InvalidState(key, o.state);
        _release(key, o);
    }

    /// @notice Seller cancels before delivery; the buyer gets the full order amount back.
    function cancel(bytes32 key) external nonReentrant {
        Order storage o = _order(key);
        if (_msgSender() != o.seller) revert NotSeller(key);
        if (o.state != State.Funded) revert InvalidState(key, o.state);
        _refund(key, o, RefundReason.Cancelled);
    }

    /// @notice Buyer disputes a delivered order, inside the confirm window.
    function dispute(bytes32 key) external {
        Order storage o = _order(key);
        if (_msgSender() != o.buyer) revert NotBuyer(key);
        if (o.state != State.Delivered) revert InvalidState(key, o.state);
        if (block.timestamp > o.confirmBy) revert WindowClosed(key, o.confirmBy);
        uint40 resolveBy = uint40(block.timestamp) + DISPUTE_BACKSTOP;
        o.state = State.Disputed;
        o.resolveBy = resolveBy;
        emit OrderDisputed(key, resolveBy);
    }

    /// @notice Anyone can settle an order whose window has passed:
    ///         not delivered by `deliverBy` → refund buyer; delivered and not disputed by
    ///         `confirmBy` → release to seller; disputed and unresolved by `resolveBy` → refund buyer.
    function claimTimeout(bytes32 key) external nonReentrant {
        Order storage o = _order(key);
        State st = o.state;
        if (st == State.Funded) {
            _requirePassed(key, o.deliverBy);
            _refund(key, o, RefundReason.NotDelivered);
        } else if (st == State.Delivered) {
            _requirePassed(key, o.confirmBy);
            _release(key, o);
        } else if (st == State.Disputed) {
            _requirePassed(key, o.resolveBy);
            _refund(key, o, RefundReason.DisputeBackstop);
        } else {
            revert InvalidState(key, st);
        }
    }

    // ── Resolver ───────────────────────────────────────────────────────────────

    /// @notice Settle a dispute by splitting the order's own funds. The buyer's share comes out of
    ///         shipping first, then the product price; the merchant fee is charged pro rata on the
    ///         product price the seller keeps (the fee was only ever on the price).
    /// @dev    Allowed after `resolveBy` too: the backstop exists for when no resolver acts, and a
    ///         resolver decision still takes precedence until someone claims the backstop.
    /// @param toBuyer Amount of the order total returned to the buyer (0 … product + shipping).
    function resolve(bytes32 key, uint128 toBuyer) external onlyRole(RESOLVER_ROLE) nonReentrant {
        Order storage o = _order(key);
        if (o.state != State.Disputed) revert InvalidState(key, o.state);
        uint256 product = o.productAmount;
        uint256 shipping = o.shippingAmount;
        uint256 gross = product + shipping;
        if (toBuyer > gross) revert ToBuyerAboveTotal(toBuyer, gross);

        uint256 fromProduct = toBuyer > shipping ? toBuyer - shipping : 0;
        uint256 sellerGross = gross - toBuyer;
        uint256 fee = (uint256(o.merchantFee) * (product - fromProduct)) / product;
        address token = o.token;
        o.state = State.Resolved;
        _subOwed(token, gross);

        _push(token, o.buyer, toBuyer);
        _push(token, o.seller, sellerGross - fee);
        _push(token, feeConfig.feeRecipient(), fee);
        emit OrderResolved(key, _msgSender(), toBuyer, sellerGross - fee, fee);
    }

    // ── Views ──────────────────────────────────────────────────────────────────

    function getOrder(bytes32 key) external view returns (Order memory) {
        return _s().orders[key];
    }

    // ── Internals ──────────────────────────────────────────────────────────────

    function _release(bytes32 key, Order storage o) private {
        uint256 gross = uint256(o.productAmount) + o.shippingAmount;
        uint256 fee = o.merchantFee;
        address token = o.token;
        address recipient = feeConfig.feeRecipient();
        o.state = State.Released;
        _subOwed(token, gross);

        _push(token, o.seller, gross - fee);
        _push(token, recipient, fee);
        emit OrderReleased(key, o.seller, gross - fee, fee, recipient);
    }

    function _refund(bytes32 key, Order storage o, RefundReason reason) private {
        uint256 gross = uint256(o.productAmount) + o.shippingAmount;
        address token = o.token;
        o.state = State.Refunded;
        _subOwed(token, gross);

        _push(token, o.buyer, gross);
        emit OrderRefunded(key, o.buyer, gross, reason);
    }

    /// @dev Read and check the fee and limits for a new order, and reserve its amount in `owed`.
    function _priceAndReserve(address token, uint128 productAmount, uint128 shippingAmount, uint256 maxFlatFee)
        private
        returns (uint96 fee, uint128 flat)
    {
        IFeeConfig.Rate memory rate = feeConfig.rateFor(FEATURE, token);
        if (rate.bps > MAX_FEE_BPS) revert FeeAboveCap(rate.bps);
        if (rate.flatFee > maxFlatFee) revert FlatFeeAboveMax(rate.flatFee, maxFlatFee);
        IFeeConfig.Limits memory limits = feeConfig.limitsFor(token);

        uint256 gross = uint256(productAmount) + shippingAmount;
        if (gross > limits.maxPerOrder) revert OrderAboveLimit(gross, limits.maxPerOrder);
        uint256 f = (uint256(productAmount) * rate.bps) / 10_000;
        if (f > type(uint96).max) revert AmountTooLarge();
        _addOwed(token, gross, limits.maxOwed);
        return (uint96(f), rate.flatFee);
    }

    function _emitCreated(bytes32 key, bytes32 orderId, Order storage o, uint128 flat) private {
        emit OrderCreated(
            key, orderId, o.buyer, o.seller, o.token, o.productAmount, o.shippingAmount, o.merchantFee, flat, o.deliverBy
        );
    }

    function _requirePassed(bytes32 key, uint40 deadline) private view {
        if (block.timestamp <= deadline) revert WindowOpen(key, deadline);
    }

    /// @dev Existing order or revert; every action below targets a created order.
    function _order(bytes32 key) private view returns (Order storage o) {
        o = _s().orders[key];
        if (o.state == State.None) revert InvalidState(key, State.None);
    }

    function _s() private pure returns (OrderEscrowStorage storage s) {
        assembly {
            s.slot := STORAGE_SLOT
        }
    }
}
