// SPDX-License-Identifier: MIT
pragma solidity ^0.8.22;

import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

/// @title P2PEscrow
/// @notice Holds seller-deposited tokens for P2P trades. Seller locks the full
///         ad amount in one transaction (createAd). Buyers individually open
///         trades against the ad (openTrade); seller releases per-trade after
///         off-chain fiat receipt. Admin can resolve disputes and webhook-driven
///         release (Phase 2+) reuses `adminRelease`.
contract P2PEscrow is
    Initializable,
    UUPSUpgradeable,
    OwnableUpgradeable,
    ReentrancyGuard,
    PausableUpgradeable
{
    using SafeERC20 for IERC20;

    enum Status { None, Pending, Paid, Released, Cancelled, Disputed, Resolved }

    /// @dev `remaining` is the unallocated amount available for new openTrade calls.
    ///      `reserved` is the sum of currently-open trade amounts.
    ///      Invariant: contract balance of `token` for this ad == remaining + reserved
    ///      (released and timed-out portions have left the contract or returned to remaining).
    struct Ad {
        address seller;
        address token;
        uint96  remaining;
        uint96  reserved;
        uint40  createdAt;
        bool    open;
    }

    struct Trade {
        bytes32 adId;
        address buyer;
        uint96  amount;
        uint40  createdAt;
        Status  status;
    }

    /// @notice 30-minute window for buyer to mark paid. After this, anyone may
    /// cancel pending trades (returning funds to ad.remaining).
    uint256 public constant TIMEOUT = 30 minutes;

    /// @notice P2P platform fee in basis points (0.5%). Charged on release out
    /// of the seller's locked amount; buyer receives `amount - fee`, system
    /// wallet receives `fee`. MCGP trades are fee-free. Maker-pays model:
    /// P2P is a liquidity-builder, not a revenue centre — see fiat-swap (2%)
    /// and instant-transfer (1%) for premium-convenience pricing.
    uint256 public constant FEE_BPS = 50;

    /// @dev MCGP token address. When `trade.token == mcgpToken`, fee is zero.
    address public mcgpToken;

    /// @dev System wallet — receives platform fees on release. Defaults to
    /// owner() if unset. Set via setSystemWallet.
    address public systemWallet;

    /// @dev Token whitelist. Only whitelisted tokens may be used in createAd.
    mapping(address => bool) public acceptedTokens;

    /// @dev Admins. Owner is implicitly admin (see isAdmin). Operator EOA
    /// (Phase 2+ webhook signer) is added here.
    mapping(address => bool) public admins;

    mapping(bytes32 => Ad)    public ads;
    mapping(bytes32 => Trade) public trades;

    event AdCreated(bytes32 indexed adId, address indexed seller, address indexed token, uint256 amount);
    event AdClosed(bytes32 indexed adId, uint256 refundedToSeller);
    event TradeOpened(bytes32 indexed tradeId, bytes32 indexed adId, address indexed buyer, uint256 amount);
    event TradePaid(bytes32 indexed tradeId);
    event TradeReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeCancelled(bytes32 indexed tradeId, uint256 returnedToAd);
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

    /// @notice Sets the MCGP-token address that receives the 0% fee discount.
    /// @dev    Requires the token to be already whitelisted via setTokenAcceptance.
    ///         Coupling these two settings prevents an admin from accidentally
    ///         (or maliciously) granting the fee discount to a token that
    ///         cannot actually be traded — or from quietly handing the discount
    ///         to an arbitrary contract.
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

    /// @notice Sweep tokens accidentally sent to the contract (or rounding dust).
    /// @dev Owner-only. Does not touch funds tracked in `ads` or `trades` because
    ///      those are enforced by per-ad/per-trade accounting on every transfer.
    function sweepExcess(address token, uint256 amount) external onlyOwner {
        require(token != address(0), "Invalid token");
        IERC20(token).safeTransfer(owner(), amount);
        emit ExcessSwept(token, amount);
    }

    // --- Ad lifecycle ---

    /// @notice Seller posts a sell ad and locks `amount` of `token` in the contract.
    /// @param  adId    Server-generated unique id (keccak256 of off-chain UUID).
    /// @param  token   Whitelisted token contract.
    /// @param  amount  Total amount being offered for sale.
    function createAd(bytes32 adId, address token, uint256 amount)
        external
        nonReentrant
        whenNotPaused
    {
        require(adId != bytes32(0), "Invalid ad id");
        require(ads[adId].seller == address(0), "Ad already exists");
        require(token != address(0), "Invalid token");
        require(acceptedTokens[token], "Token not accepted");
        require(amount > 0, "Amount must be > 0");
        require(amount <= type(uint96).max, "Amount overflow");

        IERC20(token).safeTransferFrom(msg.sender, address(this), amount);

        ads[adId] = Ad({
            seller:    msg.sender,
            token:     token,
            remaining: uint96(amount),
            reserved:  0,
            createdAt: uint40(block.timestamp),
            open:      true
        });

        emit AdCreated(adId, msg.sender, token, amount);
    }

    /// @notice Seller closes the ad. Refunds `remaining` to seller. Reverts if
    /// any open trades exist (`reserved > 0`) — seller must wait for trades to
    /// release or cancel first.
    function closeAd(bytes32 adId) external nonReentrant whenNotPaused {
        Ad storage ad = ads[adId];
        require(ad.seller != address(0), "Ad does not exist");
        require(ad.open, "Ad not open");
        require(msg.sender == ad.seller, "Only seller can close");
        require(ad.reserved == 0, "Open trades exist");

        uint256 refund = ad.remaining;
        ad.remaining = 0;
        ad.open = false;

        if (refund > 0) {
            IERC20(ad.token).safeTransfer(ad.seller, refund);
        }
        emit AdClosed(adId, refund);
    }

    // --- Trade lifecycle ---

    /// @notice Buyer opens a trade against an open ad. Decrements ad.remaining,
    /// increments ad.reserved. Funds remain in the contract until release.
    /// @param tradeId Server-generated unique id.
    /// @param adId    Existing open ad.
    /// @param amount  Portion of the ad the buyer is taking.
    function openTrade(bytes32 tradeId, bytes32 adId, uint256 amount)
        external
        nonReentrant
        whenNotPaused
    {
        require(tradeId != bytes32(0), "Invalid trade id");
        require(trades[tradeId].status == Status.None, "Trade already exists");

        Ad storage ad = ads[adId];
        require(ad.seller != address(0), "Ad does not exist");
        require(ad.open, "Ad not open");
        require(msg.sender != ad.seller, "Seller cannot self-buy");
        require(amount > 0, "Amount must be > 0");
        require(amount <= ad.remaining, "Exceeds remaining");

        ad.remaining -= uint96(amount);
        ad.reserved  += uint96(amount);

        trades[tradeId] = Trade({
            adId:      adId,
            buyer:     msg.sender,
            amount:    uint96(amount),
            createdAt: uint40(block.timestamp),
            status:    Status.Pending
        });

        emit TradeOpened(tradeId, adId, msg.sender, amount);
    }

    /// @notice Buyer marks the off-chain fiat payment as sent. State only;
    /// no funds move. Proof is captured off-chain.
    /// @dev    WARNING: this is an UNVERIFIED buyer claim. A malicious buyer
    ///         can openTrade → markPaid → raiseDispute in a single transaction
    ///         to lock seller funds without ever sending fiat. The system
    ///         relies on off-chain proof requirements and admin arbitration
    ///         (adminResolve) to handle bad-faith claims.
    function markPaid(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending, "Not pending");
        require(msg.sender == t.buyer, "Only buyer");

        t.status = Status.Paid;
        emit TradePaid(tradeId);
    }

    /// @notice Seller releases the trade after off-chain fiat receipt.
    /// Sends `amount - fee` to buyer and `fee` to systemWallet. Decrements
    /// ad.reserved (does NOT return to ad.remaining — funds have left).
    function release(bytes32 tradeId) external nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Paid, "Not paid");
        Ad storage ad = ads[t.adId];
        require(msg.sender == ad.seller, "Only seller");

        _settleRelease(tradeId, t, ad);
    }

    /// @notice Admin-driven release. Used by Phase 2+ Flutterwave webhook
    /// (operator EOA registered as admin). Same semantics as `release` but
    /// callable by any admin. Trade must be in Paid state.
    function adminRelease(bytes32 tradeId) external onlyAdmin nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Paid, "Not paid");
        Ad storage ad = ads[t.adId];

        _settleRelease(tradeId, t, ad);
    }

    /// @notice After TIMEOUT, anyone can cancel a trade still in Pending state
    /// (buyer never marked paid). Returns funds to ad.remaining so the ad
    /// remains usable. No fee charged.
    /// @dev    Intentionally NOT pause-guarded — this is pure internal accounting
    ///         (no external token transfer). Sellers must always be able to
    ///         recover funds from expired-pending trades, including during a
    ///         pause-for-incident-response.
    function cancelByTimeout(bytes32 tradeId) external nonReentrant {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending, "Not pending");
        require(block.timestamp >= uint256(t.createdAt) + TIMEOUT, "Timeout not elapsed");

        Ad storage ad = ads[t.adId];
        uint96 amt = t.amount;
        require(ad.reserved >= amt, "Reserved underflow");

        t.status = Status.Cancelled;
        ad.reserved  -= amt;
        ad.remaining += amt;

        emit TradeCancelled(tradeId, amt);
    }

    /// @notice Either party can escalate a Paid trade to a Disputed state.
    /// Funds are frozen until adminResolve is called.
    /// @dev    There is no on-chain dispute timeout in v1 — once raised, the
    ///         dispute can only be resolved by an admin. Funds are locked
    ///         indefinitely on chain; the off-chain SLA governs resolution
    ///         time. Phase 1.5 may add a force-resolve path after a deadline.
    function raiseDispute(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Paid, "Not paid");
        Ad storage ad = ads[t.adId];
        require(msg.sender == t.buyer || msg.sender == ad.seller, "Not a party");

        t.status = Status.Disputed;
        emit TradeDisputed(tradeId, msg.sender);
    }

    /// @notice Admin resolves a disputed trade. If `releaseToBuyer` is true,
    /// behaves like release (buyer gets `amount - fee`, system gets `fee`).
    /// Otherwise refunds to the ad pool (treated as a void trade — seller
    /// keeps everything, the ad can continue).
    /// @dev Admin operations are allowed while paused so disputes can still resolve.
    function adminResolve(bytes32 tradeId, bool releaseToBuyer) external onlyAdmin nonReentrant {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Disputed, "Not disputed");
        Ad storage ad = ads[t.adId];

        uint96 amt = t.amount;
        require(ad.reserved >= amt, "Reserved underflow");

        if (releaseToBuyer) {
            (uint256 toBuyer, uint256 fee) = _splitFee(amt, ad.token);
            ad.reserved -= amt;
            t.status = Status.Resolved;

            IERC20(ad.token).safeTransfer(t.buyer, toBuyer);
            if (fee > 0) {
                IERC20(ad.token).safeTransfer(_systemRecipient(), fee);
            }
            emit TradeResolved(tradeId, true, toBuyer, fee);
        } else {
            ad.reserved  -= amt;
            ad.remaining += amt;
            t.status = Status.Resolved;
            emit TradeResolved(tradeId, false, 0, 0);
        }
    }

    // --- Views ---

    /// @notice Convenience read for off-chain reconciliation.
    function getTrade(bytes32 tradeId)
        external
        view
        returns (bytes32 adId, address buyer, uint256 amount, uint256 createdAt, uint8 status)
    {
        Trade storage t = trades[tradeId];
        return (t.adId, t.buyer, t.amount, t.createdAt, uint8(t.status));
    }

    function getAd(bytes32 adId)
        external
        view
        returns (address seller, address token, uint256 remaining, uint256 reserved, uint256 createdAt, bool open)
    {
        Ad storage ad = ads[adId];
        return (ad.seller, ad.token, ad.remaining, ad.reserved, ad.createdAt, ad.open);
    }

    // --- Internals ---

    function _settleRelease(bytes32 tradeId, Trade storage t, Ad storage ad) internal {
        uint96 amt = t.amount;
        require(ad.reserved >= amt, "Reserved underflow");

        (uint256 toBuyer, uint256 fee) = _splitFee(amt, ad.token);

        ad.reserved -= amt;
        t.status = Status.Released;

        IERC20(ad.token).safeTransfer(t.buyer, toBuyer);
        if (fee > 0) {
            IERC20(ad.token).safeTransfer(_systemRecipient(), fee);
        }
        emit TradeReleased(tradeId, t.buyer, toBuyer, fee);
    }

    /// @dev Fee is 0 for MCGP, FEE_BPS otherwise. Computed at release time
    /// against the current policy — keep policy immutable per deployment.
    function _splitFee(uint96 amount, address token) internal view returns (uint256 toBuyer, uint256 fee) {
        if (mcgpToken != address(0) && token == mcgpToken) {
            return (amount, 0);
        }
        fee = (uint256(amount) * FEE_BPS) / 10000;
        toBuyer = uint256(amount) - fee;
    }

    function _systemRecipient() internal view returns (address) {
        return systemWallet == address(0) ? owner() : systemWallet;
    }

    /// @dev Reserved slots for future state variables. When adding a new state
    ///      variable, place it above this gap and shrink the gap by the same
    ///      number of slots. Total storage budget is 51 slots (6 state vars +
    ///      45 gap), matching the ProductEscrow convention so sibling contracts
    ///      share the same upgrade-reasoning model.
    uint256[45] private __gap;
}
