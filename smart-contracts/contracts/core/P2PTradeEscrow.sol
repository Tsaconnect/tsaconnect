// SPDX-License-Identifier: MIT
pragma solidity 0.8.24;

import {EIP712Upgradeable} from "@openzeppelin/contracts-upgradeable/utils/cryptography/EIP712Upgradeable.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {EscrowBase} from "./EscrowBase.sol";
import {IFeeConfig} from "./interfaces/IFeeConfig.sol";

/// @title P2PTradeEscrow
/// @notice Pool-based P2P escrow. A seller deposits once and publishes ads off-chain as signed
///         EIP-712 vouchers; a buyer opens a trade against a voucher, which locks that amount
///         until it is released to the buyer (minus the fee), cancelled back to the seller's
///         pool, or resolved. A buy-ad trade is funded just in time by the selling user instead.
/// @dev    Successor to P2PEscrowV3, redeployed fresh (no storage to carry). Kept: the voucher,
///         pool, trade and refund logic and the event/function names the backend parses.
///         Changed, per the 2026-10-27 redesign spec:
///         - Fees: rate per token from FeeConfig (fail closed), snapshotted at open; paid to the
///           FeeConfig recipient at settlement. No legacy fee fallbacks, no MCGP special case.
///         - No global release power. A seller may name ONE delegate (`setReleaser`) that can
///           release their trades (e.g. a payment merchant's webhook signer). Nobody else can.
///         - Disputes only from Paid; an unresolved dispute returns the crypto to the seller's
///           side after DISPUTE_BACKSTOP so funds never stay locked forever.
///         - The payment deadline is snapshotted per trade; auto-release windows are chosen by
///           each seller (their consent), not by an admin.
///         - Pause blocks new exposure only (deposit, open). Every exit stays open.
///         - No forwarder or Permit2 setters (see EscrowBase).
///         - Trades are stored under `tradeKey(opener, tradeId)` (opener = the buyer for openTrade,
///           the depositing user for openTradeWithDeposit), so nobody can claim someone else's
///           trade id first. Events carry the key in the `tradeId` field; every action takes it.
contract P2PTradeEscrow is EscrowBase, EIP712Upgradeable {
    bytes32 public constant RESOLVER_ROLE = keccak256("RESOLVER_ROLE");

    bytes32 public constant FEATURE_PLATFORM = "p2p_platform";
    bytes32 public constant FEATURE_GAS = "p2p_gas";

    uint16 public constant MAX_PLATFORM_FEE_BPS = 300;
    uint16 public constant MAX_GAS_FEE_BPS = 300;
    uint16 public constant MAX_FEE_BPS = 500;
    uint40 public constant PAYMENT_WINDOW = 30 minutes;
    uint40 public constant MIN_AUTO_RELEASE_WINDOW = 30 minutes;
    uint40 public constant MAX_AUTO_RELEASE_WINDOW = 7 days;
    /// @dev A liveness fallback only. It returns the crypto to the seller, so a seller who
    ///      disputes a genuinely paid trade gains if no resolver acts; resolvers must act well
    ///      inside this window (alert on TradeDisputed). Long on purpose.
    uint40 public constant DISPUTE_BACKSTOP = 60 days;

    enum Status {
        None,
        Pending,
        Paid,
        Released,
        Cancelled,
        Disputed,
        Resolved
    }

    enum Funding {
        PooledFromMaker,
        DepositedByTaker
    }

    struct AdVoucher {
        address seller;
        address token;
        uint256 maxAmount;
        uint256 minPerTrade;
        uint256 maxPerTrade;
        uint256 deadline;
        bytes32 nonce;
        address taker; // address(0): any buyer; otherwise only this buyer may open trades on it
    }

    /// @dev `expiresAt` (payment deadline), the fee bps and the auto-release window are fixed when
    ///      the trade opens.
    struct Trade {
        address seller;
        uint96 amount;
        address buyer;
        uint40 createdAt;
        uint40 expiresAt;
        Status status;
        Funding funding;
        address token;
        uint40 autoReleaseAt;
        uint40 disputedAt;
        uint16 platformFeeBps;
        uint16 gasFeeBps;
        uint40 autoReleaseWindow; // the seller's window at open; 0 on buy-ad trades
        bytes32 voucherHash;
        address tradeReleaser; // set by the depositing user on a buy-ad trade; may release this trade only
    }

    bytes32 private constant AD_VOUCHER_TYPEHASH = keccak256(
        "AdVoucher(address seller,address token,uint256 maxAmount,uint256 minPerTrade,uint256 maxPerTrade,uint256 deadline,bytes32 nonce,address taker)"
    );

    /// @custom:storage-location erc7201:tsa.storage.P2PEscrow
    struct P2PEscrowStorage {
        mapping(address seller => mapping(address token => uint256)) available;
        mapping(address seller => mapping(address token => uint256)) locked;
        mapping(bytes32 voucherHash => uint256) voucherFilled;
        mapping(bytes32 voucherHash => bool) voucherCancelled;
        mapping(bytes32 key => Trade) trades;
        mapping(address seller => address) releaser;
        mapping(address seller => uint40) autoReleaseWindow;
    }

    // keccak256(abi.encode(uint256(keccak256("tsa.storage.P2PEscrow")) - 1)) & ~bytes32(uint256(0xff))
    bytes32 private constant STORAGE_SLOT = 0x4899e8a0884efb3b2c1a1378a627874eee3c5947407772bfe917889aedfe0d00;

    // Event signatures match P2PEscrowV3 where behaviour is unchanged, so the backend parsers carry over.
    event Deposited(address indexed seller, address indexed token, uint256 amount);
    event Withdrawn(address indexed seller, address indexed token, uint256 amount);
    event VoucherCancelled(bytes32 indexed voucherHash, address indexed seller);
    event TradeOpened(
        bytes32 indexed tradeId, bytes32 indexed voucherHash, address indexed buyer, address seller, address token, uint256 amount
    );
    event TradeOpenedWithDeposit(
        bytes32 indexed tradeId, address indexed seller, address indexed buyer, address token, uint256 amount
    );
    event TradePaid(bytes32 indexed tradeId);
    event TradeReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeAutoReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeCancelled(bytes32 indexed tradeId, uint256 amount, bool returnedToPool);
    event TradeDisputed(bytes32 indexed tradeId, address indexed by);
    event TradeResolved(bytes32 indexed tradeId, bool releasedToBuyer, uint256 amountMoved, uint256 feeToSystem);
    event ReleaserSet(address indexed seller, address indexed releaser);
    event TradeReleaserSet(bytes32 indexed tradeId, address indexed releaser);
    event AutoReleaseWindowSet(address indexed seller, uint40 window);

    error InvalidTradeId();
    error TradeExists();
    error BadAmount();
    error SelfTrade();
    error InvalidVoucherSeller();
    error VoucherExpired();
    error VoucherBadBounds();
    error BelowVoucherMin();
    error AboveVoucherMax();
    error VoucherCancelledErr();
    error ExceedsVoucherCapacity();
    error BadVoucherSignature();
    error SellerInsufficient();
    error InsufficientAvailable();
    error OnlyVoucherSeller();
    error OnlyBuyer();
    error NotReleaser();
    error NotAParty();
    error InvalidStatus(bytes32 tradeId, Status status);
    error TimeoutNotElapsed(uint40 at);
    error AutoReleaseNotArmed();
    error FeeAboveCap();
    error AmountAboveLimit(uint256 amount, uint256 limit);
    error WindowOutOfBounds();
    error NotVoucherTaker();
    error InvalidReleaser();
    error PaymentWindowClosed(uint40 at);
    error AutoReleaseDue(uint40 at);
    error FlatFeeNotSupported();

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor(address forwarder, IFeeConfig feeConfig_) EscrowBase(forwarder, feeConfig_) {}

    /// @param admin     Holds every role, including RESOLVER.
    /// @param resolvers Dispute arbiters, granted RESOLVER_ROLE.
    /// @param pausers   Ops keys allowed to pause (not unpause).
    function initialize(address admin, address[] calldata resolvers, address[] calldata pausers) external initializer {
        __EscrowBase_init(admin, pausers);
        __EIP712_init("TSAConnectP2PEscrow", "3");
        _grantRole(RESOLVER_ROLE, admin);
        _grantAll(RESOLVER_ROLE, resolvers);
    }

    // ── Seller pool ────────────────────────────────────────────────────────────

    /// @notice Add to your pool. The token must have a configured P2P rate on this chain.
    function deposit(address token, uint256 amount) external nonReentrant whenNotPaused {
        if (amount == 0) revert BadAmount();
        _requireConfigured(token);
        address from = _msgSender();
        _addOwed(token, amount, feeConfig.limitsFor(token).maxOwed);
        _s().available[from][token] += amount;
        _pull(token, from, amount);
        emit Deposited(from, token, amount);
    }

    /// @notice Take funds back out of your pool. Never paused, never checks the token config,
    ///         so a seller can always recover their balance.
    function withdraw(address token, uint256 amount) external nonReentrant {
        if (amount == 0) revert BadAmount();
        address acct = _msgSender();
        P2PEscrowStorage storage s = _s();
        uint256 avail = s.available[acct][token];
        if (avail < amount) revert InsufficientAvailable();
        s.available[acct][token] = avail - amount;
        _subOwed(token, amount);
        _push(token, acct, amount);
        emit Withdrawn(acct, token, amount);
    }

    /// @notice Let `releaser` release your trades (e.g. a payment merchant's signer). address(0) clears it.
    function setReleaser(address releaser) external {
        address seller = _msgSender();
        _s().releaser[seller] = releaser;
        emit ReleaserSet(seller, releaser);
    }

    /// @notice Opt in to auto-release: a Paid trade releases to the buyer `window` after markPaid
    ///         unless you release or dispute first. 0 turns it off. Applies to your sell-ad trades
    ///         opened later; a trade keeps the window it opened with. Never applies when you sell
    ///         into a buy-ad (openTradeWithDeposit): the merchant marking paid must not be able to
    ///         take your crypto by the clock.
    function setAutoReleaseWindow(uint40 window) external {
        if (window != 0 && (window < MIN_AUTO_RELEASE_WINDOW || window > MAX_AUTO_RELEASE_WINDOW)) revert WindowOutOfBounds();
        address seller = _msgSender();
        _s().autoReleaseWindow[seller] = window;
        emit AutoReleaseWindowSet(seller, window);
    }

    // ── Vouchers ───────────────────────────────────────────────────────────────

    function hashVoucher(AdVoucher calldata v) public view returns (bytes32) {
        return _hashTypedDataV4(
            keccak256(
                abi.encode(
                    AD_VOUCHER_TYPEHASH, v.seller, v.token, v.maxAmount, v.minPerTrade, v.maxPerTrade, v.deadline, v.nonce, v.taker
                )
            )
        );
    }

    function cancelVoucher(AdVoucher calldata v) external {
        if (_msgSender() != v.seller) revert OnlyVoucherSeller();
        bytes32 h = hashVoucher(v);
        P2PEscrowStorage storage s = _s();
        if (!s.voucherCancelled[h]) {
            s.voucherCancelled[h] = true;
            emit VoucherCancelled(h, v.seller);
        }
    }

    // ── Opening trades ─────────────────────────────────────────────────────────

    /// @notice Buyer opens a trade against a seller's signed voucher; the amount moves from the
    ///         seller's available pool to locked.
    function openTrade(bytes32 tradeId, AdVoucher calldata v, bytes calldata sig, uint256 amount)
        external
        nonReentrant
        whenNotPaused
    {
        address buyer = _msgSender();
        bytes32 key = _checkNewTrade(buyer, tradeId, amount);
        if (v.seller == address(0)) revert InvalidVoucherSeller();
        if (v.seller == buyer) revert SelfTrade();
        if (v.taker != address(0) && v.taker != buyer) revert NotVoucherTaker();
        if (block.timestamp > v.deadline) revert VoucherExpired();
        if (v.maxAmount == 0) revert VoucherBadBounds();
        if (v.maxPerTrade != 0 && v.maxPerTrade > v.maxAmount) revert VoucherBadBounds();
        if (v.minPerTrade != 0 && v.maxPerTrade != 0 && v.minPerTrade > v.maxPerTrade) revert VoucherBadBounds();
        if (v.minPerTrade > v.maxAmount) revert VoucherBadBounds();
        if (v.minPerTrade != 0 && amount < v.minPerTrade) revert BelowVoucherMin();
        if (v.maxPerTrade != 0 && amount > v.maxPerTrade) revert AboveVoucherMax();

        P2PEscrowStorage storage s = _s();
        bytes32 voucherHash = hashVoucher(v);
        if (s.voucherCancelled[voucherHash]) revert VoucherCancelledErr();
        uint256 filled = s.voucherFilled[voucherHash];
        if (filled + amount > v.maxAmount) revert ExceedsVoucherCapacity();
        if (ECDSA.recover(voucherHash, sig) != v.seller) revert BadVoucherSignature();

        uint256 sellerAvail = s.available[v.seller][v.token];
        if (sellerAvail < amount) revert SellerInsufficient();
        s.available[v.seller][v.token] = sellerAvail - amount;
        s.locked[v.seller][v.token] += amount;
        s.voucherFilled[voucherHash] = filled + amount;

        _storeTrade(key, v.seller, buyer, v.token, amount, Funding.PooledFromMaker, voucherHash);
        emit TradeOpened(key, voucherHash, buyer, v.seller, v.token, amount);
    }

    /// @notice A user sells to a merchant's buy-ad by depositing the crypto now. The caller is the
    ///         seller of the crypto and the merchant receives it on release.
    /// @param releaser Optional (address(0) for none): an address the user lets release THIS trade,
    ///                 e.g. the platform's release key, which releases once it has paid the user out
    ///                 off-chain. It can never be the merchant receiving the crypto.
    function openTradeWithDeposit(bytes32 tradeId, address token, uint256 amount, address merchant, address releaser)
        external
        nonReentrant
        whenNotPaused
    {
        address user = _msgSender();
        bytes32 key = _checkNewTrade(user, tradeId, amount);
        if (merchant == address(0)) revert ZeroAddress();
        if (merchant == user) revert SelfTrade();
        if (releaser == merchant) revert InvalidReleaser();

        _s().locked[user][token] += amount;
        _storeTrade(key, user, merchant, token, amount, Funding.DepositedByTaker, bytes32(0));
        if (releaser != address(0)) {
            _s().trades[key].tradeReleaser = releaser;
            emit TradeReleaserSet(key, releaser);
        }
        _pull(token, user, amount);
        emit TradeOpenedWithDeposit(key, user, merchant, token, amount);
    }

    // ── Trade lifecycle (never paused) ───────────────────────────────────────────

    /// @notice Buyer confirms the fiat was sent.
    function markPaid(bytes32 key) external {
        Trade storage t = _trade(key);
        if (t.status != Status.Pending) revert InvalidStatus(key, t.status);
        if (_msgSender() != t.buyer) revert OnlyBuyer();
        if (block.timestamp >= t.expiresAt) revert PaymentWindowClosed(t.expiresAt);
        t.status = Status.Paid;
        if (t.autoReleaseWindow != 0) t.autoReleaseAt = uint40(block.timestamp) + t.autoReleaseWindow;
        emit TradePaid(key);
    }

    /// @notice The seller, the seller's named releaser, or the releaser the seller named for this
    ///         trade releases the crypto to the buyer.
    function release(bytes32 key) external nonReentrant {
        Trade storage t = _trade(key);
        if (t.status != Status.Pending && t.status != Status.Paid) revert InvalidStatus(key, t.status);
        address caller = _msgSender();
        if (caller != t.seller && caller != _s().releaser[t.seller] && (t.tradeReleaser == address(0) || caller != t.tradeReleaser)) {
            revert NotReleaser();
        }
        _releaseToBuyer(key, t, false);
    }

    /// @notice Anyone may finalize an armed auto-release once its window has passed.
    function finalizeRelease(bytes32 key) external nonReentrant {
        Trade storage t = _trade(key);
        if (t.status != Status.Paid) revert InvalidStatus(key, t.status);
        if (t.autoReleaseAt == 0) revert AutoReleaseNotArmed();
        if (block.timestamp < t.autoReleaseAt) revert TimeoutNotElapsed(t.autoReleaseAt);
        _releaseToBuyer(key, t, true);
    }

    /// @notice Anyone may cancel a trade the buyer never marked paid within the payment window.
    function cancelByTimeout(bytes32 key) external nonReentrant {
        Trade storage t = _trade(key);
        if (t.status != Status.Pending) revert InvalidStatus(key, t.status);
        if (block.timestamp < t.expiresAt) revert TimeoutNotElapsed(t.expiresAt);
        t.status = Status.Cancelled;
        bool toPool = _returnToSeller(t);
        emit TradeCancelled(key, t.amount, toPool);
    }

    /// @notice Either party disputes a trade the buyer has marked paid. Not once an armed
    ///         auto-release is due: the seller agreed that silence means release.
    function raiseDispute(bytes32 key) external {
        Trade storage t = _trade(key);
        if (t.status != Status.Paid) revert InvalidStatus(key, t.status);
        if (t.autoReleaseAt != 0 && block.timestamp >= t.autoReleaseAt) revert AutoReleaseDue(t.autoReleaseAt);
        address by = _msgSender();
        if (by != t.buyer && by != t.seller) revert NotAParty();
        t.status = Status.Disputed;
        t.disputedAt = uint40(block.timestamp);
        emit TradeDisputed(key, by);
    }

    /// @notice Resolver settles a dispute: release to the buyer (minus the snapshotted fee) or
    ///         return the crypto to the seller's side.
    function adminResolve(bytes32 key, bool releaseToBuyer) external onlyRole(RESOLVER_ROLE) nonReentrant {
        Trade storage t = _trade(key);
        if (t.status != Status.Disputed) revert InvalidStatus(key, t.status);
        t.status = Status.Resolved;
        if (releaseToBuyer) {
            (uint256 toBuyer, uint256 fee) = _payOut(t);
            emit TradeResolved(key, true, toBuyer, fee);
        } else {
            _returnToSeller(t);
            emit TradeResolved(key, false, 0, 0);
        }
    }

    /// @notice Anyone may close a dispute nobody resolved within DISPUTE_BACKSTOP: the crypto goes
    ///         back to the seller's side (the default where the buyer's payment was never confirmed).
    function claimDisputeBackstop(bytes32 key) external nonReentrant {
        Trade storage t = _trade(key);
        if (t.status != Status.Disputed) revert InvalidStatus(key, t.status);
        uint40 at = t.disputedAt + DISPUTE_BACKSTOP;
        if (block.timestamp <= at) revert TimeoutNotElapsed(at);
        t.status = Status.Resolved;
        _returnToSeller(t);
        emit TradeResolved(key, false, 0, 0);
    }

    // ── Views ──────────────────────────────────────────────────────────────────

    /// @notice The storage key of `opener`'s trade `key`.
    function tradeKey(address opener, bytes32 tradeId) public pure returns (bytes32) {
        return keccak256(abi.encode(opener, tradeId));
    }

    function getTrade(bytes32 key) external view returns (Trade memory) {
        return _s().trades[key];
    }

    function balanceOf(address seller, address token) external view returns (uint256 available, uint256 locked) {
        P2PEscrowStorage storage s = _s();
        return (s.available[seller][token], s.locked[seller][token]);
    }

    function voucherFilled(bytes32 voucherHash) external view returns (uint256) {
        return _s().voucherFilled[voucherHash];
    }

    function voucherCancelled(bytes32 voucherHash) external view returns (bool) {
        return _s().voucherCancelled[voucherHash];
    }

    function releaserOf(address seller) external view returns (address) {
        return _s().releaser[seller];
    }

    function autoReleaseWindowOf(address seller) external view returns (uint40) {
        return _s().autoReleaseWindow[seller];
    }

    /// @notice The fixed payment window, kept as a function for the backend's existing call.
    function paymentWindow() external pure returns (uint40) {
        return PAYMENT_WINDOW;
    }

    // ── Internals ──────────────────────────────────────────────────────────────

    function _checkNewTrade(address opener, bytes32 tradeId, uint256 amount) private view returns (bytes32 key) {
        if (tradeId == bytes32(0)) revert InvalidTradeId();
        key = tradeKey(opener, tradeId);
        if (_s().trades[key].status != Status.None) revert TradeExists();
        if (amount == 0 || amount > type(uint96).max) revert BadAmount();
    }

    /// @dev Reverts FeeNotConfigured unless both P2P rates exist for `token`.
    function _requireConfigured(address token) private view {
        feeConfig.rateFor(FEATURE_PLATFORM, token);
        feeConfig.rateFor(FEATURE_GAS, token);
    }

    function _storeTrade(
        bytes32 key,
        address seller,
        address buyer,
        address token,
        uint256 amount,
        Funding funding,
        bytes32 voucherHash
    ) private {
        IFeeConfig.Rate memory p = feeConfig.rateFor(FEATURE_PLATFORM, token);
        IFeeConfig.Rate memory g = feeConfig.rateFor(FEATURE_GAS, token);
        // P2P fees are bps only; refuse a flat fee rather than silently ignore it.
        if (p.flatFee != 0 || g.flatFee != 0) revert FlatFeeNotSupported();
        if (p.bps > MAX_PLATFORM_FEE_BPS || g.bps > MAX_GAS_FEE_BPS || uint256(p.bps) + g.bps > MAX_FEE_BPS) {
            revert FeeAboveCap();
        }
        IFeeConfig.Limits memory l = feeConfig.limitsFor(token);
        if (amount > l.maxPerOrder) revert AmountAboveLimit(amount, l.maxPerOrder);
        // A buy-ad deposit brings new funds in; a sell-ad trade locks what the pool already owes.
        if (funding == Funding.DepositedByTaker) _addOwed(token, amount, l.maxOwed);

        _s().trades[key] = Trade({
            seller: seller,
            amount: uint96(amount),
            buyer: buyer,
            createdAt: uint40(block.timestamp),
            expiresAt: uint40(block.timestamp) + PAYMENT_WINDOW,
            status: Status.Pending,
            funding: funding,
            token: token,
            autoReleaseAt: 0,
            disputedAt: 0,
            platformFeeBps: p.bps,
            gasFeeBps: g.bps,
            autoReleaseWindow: funding == Funding.PooledFromMaker ? _s().autoReleaseWindow[seller] : 0,
            voucherHash: voucherHash,
            tradeReleaser: address(0)
        });
    }

    function _releaseToBuyer(bytes32 key, Trade storage t, bool auto_) private {
        t.status = Status.Released;
        (uint256 toBuyer, uint256 fee) = _payOut(t);
        if (auto_) emit TradeAutoReleased(key, t.buyer, toBuyer, fee);
        else emit TradeReleased(key, t.buyer, toBuyer, fee);
    }

    /// @dev Unlock the trade amount and pay the buyer, minus the snapshotted fee to the fee recipient.
    function _payOut(Trade storage t) private returns (uint256 toBuyer, uint256 fee) {
        uint256 amt = t.amount;
        address token = t.token;
        _s().locked[t.seller][token] -= amt;
        _subOwed(token, amt);
        fee = (amt * (uint256(t.platformFeeBps) + t.gasFeeBps)) / 10_000;
        toBuyer = amt - fee;
        _push(token, t.buyer, toBuyer);
        _push(token, feeConfig.feeRecipient(), fee);
    }

    /// @dev Pooled trades go back to the seller's available pool (stay in the contract); buy-ad
    ///      deposits go back to the user who deposited them. Returns true when returned to the pool.
    function _returnToSeller(Trade storage t) private returns (bool toPool) {
        uint256 amt = t.amount;
        address token = t.token;
        P2PEscrowStorage storage s = _s();
        s.locked[t.seller][token] -= amt;
        if (t.funding == Funding.PooledFromMaker) {
            s.available[t.seller][token] += amt;
            if (t.voucherHash != bytes32(0)) s.voucherFilled[t.voucherHash] -= amt;
            return true;
        }
        _subOwed(token, amt);
        _push(token, t.seller, amt);
        return false;
    }

    function _trade(bytes32 key) private view returns (Trade storage t) {
        t = _s().trades[key];
        if (t.status == Status.None) revert InvalidStatus(key, Status.None);
    }

    function _s() private pure returns (P2PEscrowStorage storage s) {
        assembly {
            s.slot := STORAGE_SLOT
        }
    }
}
