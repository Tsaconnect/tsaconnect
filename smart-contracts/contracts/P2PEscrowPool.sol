// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/cryptography/EIP712Upgradeable.sol";
import "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

/// @title  P2PEscrowPool
/// @notice Pool-based P2P escrow. Sellers deposit once into a per-(seller,token)
///         available balance, publish unlimited ads off-chain by signing EIP-712
///         vouchers (no gas, instant), and funds only move to a `locked` balance
///         when a buyer actually opens a trade.
///
///         This replaces V1's lock-on-publish model. V1 remains deployed for any
///         in-flight ads to run to completion; new ads route to V2.
///
/// Lifecycle:
///   1. Seller calls `deposit(token, amount)` once (or refills as needed)
///   2. Seller signs an `AdVoucher` off-chain at ad publish — recorded only in
///      the backend DB. Editing limits/pricing/payment-methods is purely
///      off-chain.
///   3. Buyer calls `openTrade(tradeId, voucher, sig, amount)` — contract
///      verifies the seller's signature, debits seller's `available`, credits
///      seller's `locked`, and records the trade.
///   4. Buyer `markPaid` → seller `release` → contract pays buyer minus fee.
///   5. Failure paths (`cancelByTimeout`, `raiseDispute`, `adminResolve`) mirror
///      V1; they return the locked amount to `available` or release per dispute.
contract P2PEscrowPool is
    Initializable,
    UUPSUpgradeable,
    OwnableUpgradeable,
    ReentrancyGuard,
    PausableUpgradeable,
    EIP712Upgradeable
{
    using SafeERC20 for IERC20;

    enum Status { None, Pending, Paid, Released, Cancelled, Disputed, Resolved }

    /// @notice Off-chain voucher signed by the seller authorising buyers to
    /// open trades against their on-chain `available` balance. Identified
    /// uniquely by `nonce` (server-generated; one nonce per published ad).
    struct AdVoucher {
        address seller;
        address token;
        uint256 maxAmount;     // total fillable across all trades on this voucher
        uint256 minPerTrade;   // 0 disables the floor
        uint256 maxPerTrade;   // 0 disables the ceiling
        uint256 deadline;      // unix seconds; voucher invalid after this
        bytes32 nonce;         // server-generated, doubles as the off-chain adId
    }

    bytes32 private constant AD_VOUCHER_TYPEHASH = keccak256(
        "AdVoucher(address seller,address token,uint256 maxAmount,uint256 minPerTrade,uint256 maxPerTrade,uint256 deadline,bytes32 nonce)"
    );

    struct Trade {
        address seller;
        address buyer;
        address token;
        uint96  amount;
        uint40  createdAt;
        Status  status;
        bytes32 voucherHash; // links the trade back to the voucher for analytics + dispute
    }

    /// @notice 30-minute window for buyer to mark paid. After this anyone may
    /// cancel pending trades (returns locked → available). Matches V1.
    uint256 public constant TIMEOUT = 30 minutes;

    /// @notice Platform fee on release, in basis points (0.5%). MCGP trades
    /// are fee-free. Matches V1.
    uint256 public constant FEE_BPS = 50;

    address public mcgpToken;
    address public systemWallet;

    mapping(address token => bool) public acceptedTokens;
    mapping(address account => bool) public admins;

    /// @dev Per-(seller,token) free balance; debited at openTrade, credited
    /// at deposit / cancelByTimeout / adminResolve(false). Withdrawable
    /// at any time.
    mapping(address seller => mapping(address token => uint256)) public available;

    /// @dev Per-(seller,token) reserved-against-open-trades balance. Only
    /// `release` / `adminResolve(true)` move funds out of `locked` to the
    /// buyer; cancellations return `locked` → `available`.
    mapping(address seller => mapping(address token => uint256)) public locked;

    /// @dev Per-voucher cumulative fill amount across all trades. The
    /// voucher hash is the EIP-712 digest, so changing any field (incl.
    /// nonce) produces a different hash → independent fill tracking.
    mapping(bytes32 voucherHash => uint256) public voucherFilled;

    /// @dev Seller-side voucher revocation. A voucher is invalid once
    /// either (a) `block.timestamp > deadline` or (b) `voucherCancelled` is
    /// true. Open trades against a cancelled voucher still settle normally.
    mapping(bytes32 voucherHash => bool) public voucherCancelled;

    mapping(bytes32 tradeId => Trade) public trades;

    event Deposited(address indexed seller, address indexed token, uint256 amount);
    event Withdrawn(address indexed seller, address indexed token, uint256 amount);
    event VoucherCancelled(bytes32 indexed voucherHash, address indexed seller);
    event TradeOpened(
        bytes32 indexed tradeId,
        bytes32 indexed voucherHash,
        address indexed buyer,
        address seller,
        address token,
        uint256 amount
    );
    event TradePaid(bytes32 indexed tradeId);
    event TradeReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeCancelled(bytes32 indexed tradeId, uint256 returnedToAvailable);
    event TradeDisputed(bytes32 indexed tradeId, address indexed by);
    event TradeResolved(bytes32 indexed tradeId, bool releasedToBuyer, uint256 amountMoved, uint256 feeToSystem);

    event TokenAcceptanceUpdated(address indexed token, bool accepted);
    event McgpTokenUpdated(address indexed oldToken, address indexed newToken);
    event SystemWalletUpdated(address indexed oldWallet, address indexed newWallet);
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

    function initialize(address initialOwner, address[] calldata initialAdmins) external initializer {
        __Ownable_init(initialOwner);
        __Pausable_init();
        __EIP712_init("TSAConnectP2PEscrowPool", "1");

        for (uint256 i = 0; i < initialAdmins.length; i++) {
            address a = initialAdmins[i];
            require(a != address(0), "Zero address");
            require(!admins[a], "Already admin");
            admins[a] = true;
            emit AdminAdded(a);
        }
    }

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
        require(acceptedTokens[_mcgpToken], "MCGP must be accepted first");
        address old = mcgpToken;
        mcgpToken = _mcgpToken;
        emit McgpTokenUpdated(old, _mcgpToken);
    }

    function setSystemWallet(address _systemWallet) external onlyOwner {
        require(_systemWallet != address(0), "Invalid system wallet");
        address old = systemWallet;
        systemWallet = _systemWallet;
        emit SystemWalletUpdated(old, _systemWallet);
    }

    function setTokenAcceptance(address token, bool accepted) external onlyOwner {
        require(token != address(0), "Invalid token");
        acceptedTokens[token] = accepted;
        emit TokenAcceptanceUpdated(token, accepted);
    }

    function pause() external onlyOwner { _pause(); }
    function unpause() external onlyOwner { _unpause(); }

    /// @notice Sweep tokens accidentally sent to the contract or rounding dust.
    /// @dev Owner-only. Cannot touch funds tracked in `available` or `locked`
    ///      because every transfer in/out is per-(seller,token) accounted.
    function sweepExcess(address token, uint256 amount) external onlyOwner {
        require(token != address(0), "Invalid token");
        IERC20(token).safeTransfer(owner(), amount);
        emit ExcessSwept(token, amount);
    }

    // --- Deposit / withdraw ---

    /// @notice Seller tops up their P2P-trading balance for a token. Caller
    /// must have ERC20-approved this contract for `amount`.
    function deposit(address token, uint256 amount) external nonReentrant whenNotPaused {
        require(token != address(0), "Invalid token");
        require(acceptedTokens[token], "Token not accepted");
        require(amount > 0, "Amount must be > 0");

        IERC20(token).safeTransferFrom(msg.sender, address(this), amount);
        available[msg.sender][token] += amount;

        emit Deposited(msg.sender, token, amount);
    }

    /// @notice Seller pulls funds back out of their available balance.
    /// @dev Not pause-guarded — sellers must always be able to recover unused
    ///      capital during a pause-for-incident-response. Locked balance
    ///      (against open trades) is untouchable until the trade settles.
    function withdraw(address token, uint256 amount) external nonReentrant {
        require(amount > 0, "Amount must be > 0");
        uint256 avail = available[msg.sender][token];
        require(avail >= amount, "Insufficient available");
        available[msg.sender][token] = avail - amount;

        IERC20(token).safeTransfer(msg.sender, amount);
        emit Withdrawn(msg.sender, token, amount);
    }

    // --- Voucher management ---

    /// @notice Returns the EIP-712 digest the seller signs for a given
    /// voucher. Exposed so the frontend / backend can compute the same hash
    /// for indexing and cancellation.
    function hashVoucher(AdVoucher calldata v) public view returns (bytes32) {
        return _hashTypedDataV4(keccak256(abi.encode(
            AD_VOUCHER_TYPEHASH,
            v.seller, v.token, v.maxAmount, v.minPerTrade, v.maxPerTrade, v.deadline, v.nonce
        )));
    }

    /// @notice Seller invalidates a previously-published voucher. New trades
    /// against it will revert; in-flight trades still settle normally.
    /// @dev Idempotent. Only the voucher's seller can cancel.
    function cancelVoucher(AdVoucher calldata v) external {
        require(msg.sender == v.seller, "Only voucher seller");
        bytes32 h = hashVoucher(v);
        if (!voucherCancelled[h]) {
            voucherCancelled[h] = true;
            emit VoucherCancelled(h, v.seller);
        }
    }

    // --- Trade lifecycle ---

    /// @notice Buyer opens a trade against a seller's published voucher.
    /// Verifies the seller's signature, that the voucher is live and not
    /// over-filled, and reserves `amount` of the seller's `available` into
    /// `locked`.
    ///
    /// @param tradeId Server-generated unique id (keccak256 of off-chain UUID).
    /// @param v       The signed voucher.
    /// @param sig     The seller's EIP-712 signature over `v`.
    /// @param amount  Portion of the voucher the buyer is taking.
    function openTrade(
        bytes32 tradeId,
        AdVoucher calldata v,
        bytes calldata sig,
        uint256 amount
    ) external nonReentrant whenNotPaused {
        require(tradeId != bytes32(0), "Invalid trade id");
        require(trades[tradeId].status == Status.None, "Trade already exists");
        require(amount > 0, "Amount must be > 0");
        require(amount <= type(uint96).max, "Amount overflow");
        require(v.seller != address(0), "Invalid voucher seller");
        require(v.seller != msg.sender, "Seller cannot self-buy");
        require(acceptedTokens[v.token], "Token not accepted");
        require(block.timestamp <= v.deadline, "Voucher expired");
        require(v.minPerTrade == 0 || amount >= v.minPerTrade, "Below voucher min");
        require(v.maxPerTrade == 0 || amount <= v.maxPerTrade, "Above voucher max");

        bytes32 voucherHash = hashVoucher(v);
        require(!voucherCancelled[voucherHash], "Voucher cancelled");

        uint256 filled = voucherFilled[voucherHash];
        require(filled + amount <= v.maxAmount, "Exceeds voucher capacity");

        address recovered = ECDSA.recover(voucherHash, sig);
        require(recovered == v.seller, "Bad voucher signature");

        uint256 sellerAvailable = available[v.seller][v.token];
        require(sellerAvailable >= amount, "Seller balance insufficient");

        available[v.seller][v.token] = sellerAvailable - amount;
        locked[v.seller][v.token] += amount;
        voucherFilled[voucherHash] = filled + amount;

        trades[tradeId] = Trade({
            seller:      v.seller,
            buyer:       msg.sender,
            token:       v.token,
            amount:      uint96(amount),
            createdAt:   uint40(block.timestamp),
            status:      Status.Pending,
            voucherHash: voucherHash
        });

        emit TradeOpened(tradeId, voucherHash, msg.sender, v.seller, v.token, amount);
    }

    /// @notice Buyer marks the off-chain fiat payment as sent. State only —
    /// no funds move. As in V1 this is an UNVERIFIED claim; the seller's
    /// release signature is the real authorisation gate. See V1 comment for
    /// the bad-faith analysis (raiseDispute / adminResolve fallback).
    function markPaid(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending, "Not pending");
        require(msg.sender == t.buyer, "Only buyer");

        t.status = Status.Paid;
        emit TradePaid(tradeId);
    }

    /// @notice Seller releases the trade after fiat receipt. Pays buyer
    /// `amount - fee` and `fee` to systemWallet. Callable from Pending or
    /// Paid (seller's signature is the gate, not the buyer's markPaid).
    function release(bytes32 tradeId) external nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending || t.status == Status.Paid, "Not releasable");
        require(msg.sender == t.seller, "Only seller");
        _settleRelease(tradeId, t);
    }

    /// @notice Admin-driven release (Phase 2+ webhook-signer use). Same
    /// semantics as `release` but callable by any admin.
    function adminRelease(bytes32 tradeId) external onlyAdmin nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending || t.status == Status.Paid, "Not releasable");
        _settleRelease(tradeId, t);
    }

    /// @notice After TIMEOUT, anyone can cancel a Pending trade whose buyer
    /// never marked paid. Returns locked → available. No fee.
    /// @dev Not pause-guarded — pure internal accounting (see V1 rationale).
    function cancelByTimeout(bytes32 tradeId) external nonReentrant {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending, "Not pending");
        require(block.timestamp >= uint256(t.createdAt) + TIMEOUT, "Timeout not elapsed");

        uint96 amt = t.amount;
        require(locked[t.seller][t.token] >= amt, "Locked underflow");

        t.status = Status.Cancelled;
        locked[t.seller][t.token]    -= amt;
        available[t.seller][t.token] += amt;

        emit TradeCancelled(tradeId, amt);
    }

    /// @notice Either party can escalate a Pending or Paid trade to Disputed.
    /// Funds are frozen until adminResolve.
    function raiseDispute(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending || t.status == Status.Paid, "Not disputable");
        require(msg.sender == t.buyer || msg.sender == t.seller, "Not a party");

        t.status = Status.Disputed;
        emit TradeDisputed(tradeId, msg.sender);
    }

    /// @notice Admin resolves a disputed trade. `releaseToBuyer=true` →
    /// buyer gets `amount-fee`, system gets `fee`. Otherwise refund to
    /// seller's `available` (void trade).
    /// @dev Admin operations are allowed while paused so disputes can resolve.
    function adminResolve(bytes32 tradeId, bool releaseToBuyer) external onlyAdmin nonReentrant {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Disputed, "Not disputed");

        uint96 amt = t.amount;
        require(locked[t.seller][t.token] >= amt, "Locked underflow");

        if (releaseToBuyer) {
            (uint256 toBuyer, uint256 fee) = _splitFee(amt, t.token);
            locked[t.seller][t.token] -= amt;
            t.status = Status.Resolved;

            IERC20(t.token).safeTransfer(t.buyer, toBuyer);
            if (fee > 0) {
                IERC20(t.token).safeTransfer(_systemRecipient(), fee);
            }
            emit TradeResolved(tradeId, true, toBuyer, fee);
        } else {
            locked[t.seller][t.token]    -= amt;
            available[t.seller][t.token] += amt;
            t.status = Status.Resolved;
            emit TradeResolved(tradeId, false, 0, 0);
        }
    }

    // --- Views ---

    function getTrade(bytes32 tradeId)
        external
        view
        returns (
            address seller,
            address buyer,
            address token,
            uint256 amount,
            uint256 createdAt,
            uint8   status,
            bytes32 voucherHash
        )
    {
        Trade storage t = trades[tradeId];
        return (t.seller, t.buyer, t.token, t.amount, t.createdAt, uint8(t.status), t.voucherHash);
    }

    /// @notice Convenience: free + locked balance for a (seller, token).
    function balanceOf(address seller, address token)
        external
        view
        returns (uint256 _available, uint256 _locked)
    {
        return (available[seller][token], locked[seller][token]);
    }

    // --- Internals ---

    function _settleRelease(bytes32 tradeId, Trade storage t) internal {
        uint96 amt = t.amount;
        require(locked[t.seller][t.token] >= amt, "Locked underflow");

        (uint256 toBuyer, uint256 fee) = _splitFee(amt, t.token);

        locked[t.seller][t.token] -= amt;
        t.status = Status.Released;

        IERC20(t.token).safeTransfer(t.buyer, toBuyer);
        if (fee > 0) {
            IERC20(t.token).safeTransfer(_systemRecipient(), fee);
        }
        emit TradeReleased(tradeId, t.buyer, toBuyer, fee);
    }

    function _splitFee(uint96 amount, address token) internal view returns (uint256 toBuyer, uint256 fee) {
        if (mcgpToken != address(0) && token == mcgpToken) {
            return (amount, 0);
        }
        fee = (uint256(amount) * FEE_BPS) / 10000;
        toBuyer = uint256(amount) - fee;
    }

    function _systemRecipient() internal view returns (address) {
        address sw = systemWallet;
        return sw == address(0) ? owner() : sw;
    }
}
