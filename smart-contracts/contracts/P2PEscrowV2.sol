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

/// @title P2PEscrowV2
/// @notice Two-sided pool/JIT escrow. See
///         docs/superpowers/specs/2026-05-29-p2p-escrow-v2-two-sided-design.md
contract P2PEscrowV2 is
    Initializable,
    UUPSUpgradeable,
    OwnableUpgradeable,
    PausableUpgradeable,
    EIP712Upgradeable
{
    using SafeERC20 for IERC20;

    // ── Inline upgradeable reentrancy guard ────────────────────────────────
    // OZ v5 removed ReentrancyGuardUpgradeable; implement the slot directly.
    uint256 private constant _NOT_ENTERED = 1;
    uint256 private constant _ENTERED = 2;
    uint256 private _reentrancyStatus;

    modifier nonReentrant() {
        require(_reentrancyStatus != _ENTERED, "ReentrancyGuard: reentrant call");
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
        uint16  feeBps;
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
    uint16 public disputeFeeBps;
    uint40 public paymentWindow;
    uint40 public autoReleaseWindow;

    // Gasless hooks (disabled until set).
    address public trustedForwarder;
    address public permit2;

    // Per-merchant ⚡ auto-release opt-in (default false).
    mapping(address => bool) public autoReleaseOptIn;

    uint256[50] private __gap;

    // ── Events (v1 set, plus v2 additions) ─────────────────────────────────
    event Deposited(address indexed seller, address indexed token, uint256 amount);
    event Withdrawn(address indexed seller, address indexed token, uint256 amount);
    event VoucherCancelled(bytes32 indexed voucherHash, address indexed seller);
    event TradeOpened(bytes32 indexed tradeId, bytes32 indexed voucherHash, address indexed buyer, address seller, address token, uint256 amount);
    event TradeOpenedWithDeposit(bytes32 indexed tradeId, address indexed seller, address indexed buyer, address token, uint256 amount);
    event TradePaid(bytes32 indexed tradeId);
    event TradeReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeAutoReleased(bytes32 indexed tradeId, address indexed buyer, uint256 amountToBuyer, uint256 feeToSystem);
    event TradeCancelled(bytes32 indexed tradeId, uint256 returnedToAvailable);
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

    modifier onlyAdmin() { require(isAdmin(_msgSender()), "Not admin"); _; }

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() { _disableInitializers(); }

    function initialize(address initialOwner, address[] calldata initialAdmins) external initializer {
        __Ownable_init(initialOwner);
        __Pausable_init();
        __EIP712_init("TSAConnectP2PEscrow", "2");
        _reentrancyStatus = _NOT_ENTERED;

        feeBps = 50;
        paymentWindow = 30 minutes;
        // autoReleaseWindow, disputeFeeBps, trustedForwarder, permit2 stay zero (disabled).

        for (uint256 i = 0; i < initialAdmins.length; i++) {
            address a = initialAdmins[i];
            require(a != address(0), "Zero address");
            require(!admins[a], "Already admin");
            admins[a] = true;
            emit AdminAdded(a);
        }
    }

    function _authorizeUpgrade(address) internal override onlyOwner {}

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
        return (msg.sender == trustedForwarder) ? 20 : super._contextSuffixLength();
    }

    // ── Deposit / withdraw ─────────────────────────────────────────────────

    function deposit(address token, uint256 amount) external nonReentrant whenNotPaused {
        require(token != address(0), "Invalid token");
        require(acceptedTokens[token], "Token not accepted");
        require(amount > 0, "Amount must be > 0");
        address from = _msgSender();
        IERC20(token).safeTransferFrom(from, address(this), amount);
        available[from][token] += amount;
        accountedBalance[token] += amount;
        emit Deposited(from, token, amount);
    }

    function withdraw(address token, uint256 amount) external nonReentrant {
        require(amount > 0, "Amount must be > 0");
        address acct = _msgSender();
        uint256 avail = available[acct][token];
        require(avail >= amount, "Insufficient available");
        available[acct][token] = avail - amount;
        accountedBalance[token] -= amount;
        IERC20(token).safeTransfer(acct, amount);
        emit Withdrawn(acct, token, amount);
    }

    function balanceOf(address seller, address token) external view returns (uint256 _available, uint256 _locked) {
        return (available[seller][token], locked[seller][token]);
    }

    function sweepExcess(address token, uint256 amount) external onlyOwner {
        require(token != address(0), "Invalid token");
        uint256 contractBal = IERC20(token).balanceOf(address(this));
        uint256 accounted = accountedBalance[token];
        require(contractBal >= accounted, "Accounting corrupt");
        require(amount <= contractBal - accounted, "Exceeds excess");
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
        require(_msgSender() == v.seller, "Only voucher seller");
        bytes32 h = hashVoucher(v);
        if (!voucherCancelled[h]) { voucherCancelled[h] = true; emit VoucherCancelled(h, v.seller); }
    }

    // ── Trade lifecycle ────────────────────────────────────────────────────

    function _feeBpsForToken(address token) internal view returns (uint16) {
        if (mcgpToken != address(0) && token == mcgpToken) return 0;
        return feeBps;
    }

    function getTrade(bytes32 tradeId) external view returns (
        address seller, address buyer, address token, uint256 amount,
        uint256 createdAt, uint8 status, uint8 funding, uint16 feeBps_, uint256 autoReleaseAt, bytes32 voucherHash
    ) {
        Trade storage t = trades[tradeId];
        return (t.seller, t.buyer, t.token, t.amount, t.createdAt, uint8(t.status), uint8(t.funding), t.feeBps, t.autoReleaseAt, t.voucherHash);
    }

    function openTrade(bytes32 tradeId, AdVoucher calldata v, bytes calldata sig, uint256 amount)
        external nonReentrant whenNotPaused
    {
        require(tradeId != bytes32(0), "Invalid trade id");
        require(trades[tradeId].status == Status.None, "Trade already exists");
        require(amount > 0, "Amount must be > 0");
        require(amount <= type(uint96).max, "Amount overflow");
        address buyer = _msgSender();
        require(v.seller != address(0), "Invalid voucher seller");
        require(v.seller != buyer, "Seller cannot self-buy");
        require(v.token != address(0), "Voucher: token=0");
        require(acceptedTokens[v.token], "Token not accepted");
        require(block.timestamp <= v.deadline, "Voucher expired");
        require(v.maxAmount > 0, "Voucher maxAmount=0");
        require(v.maxPerTrade == 0 || v.maxPerTrade <= v.maxAmount, "Voucher: maxPerTrade > maxAmount");
        require(v.minPerTrade == 0 || v.maxPerTrade == 0 || v.minPerTrade <= v.maxPerTrade, "Voucher: minPerTrade > maxPerTrade");
        require(v.minPerTrade <= v.maxAmount, "Voucher: minPerTrade > maxAmount");
        require(v.minPerTrade == 0 || amount >= v.minPerTrade, "Below voucher min");
        require(v.maxPerTrade == 0 || amount <= v.maxPerTrade, "Above voucher max");

        bytes32 voucherHash = hashVoucher(v);
        require(!voucherCancelled[voucherHash], "Voucher cancelled");
        uint256 filled = voucherFilled[voucherHash];
        require(filled + amount <= v.maxAmount, "Exceeds voucher capacity");
        require(ECDSA.recover(voucherHash, sig) == v.seller, "Bad voucher signature");

        uint256 sellerAvail = available[v.seller][v.token];
        require(sellerAvail >= amount, "Seller balance insufficient");
        available[v.seller][v.token] = sellerAvail - amount;
        locked[v.seller][v.token] += amount;
        voucherFilled[voucherHash] = filled + amount;

        trades[tradeId] = Trade({
            seller: v.seller, buyer: buyer, token: v.token, amount: uint96(amount),
            createdAt: uint40(block.timestamp), autoReleaseAt: 0,
            status: Status.Pending, funding: Funding.PooledFromMaker,
            feeBps: _feeBpsForToken(v.token), voucherHash: voucherHash
        });
        emit TradeOpened(tradeId, voucherHash, buyer, v.seller, v.token, amount);
    }

    function openTradeWithDeposit(bytes32 tradeId, address token, uint256 amount, address merchant)
        external nonReentrant whenNotPaused
    {
        require(tradeId != bytes32(0), "Invalid trade id");
        require(trades[tradeId].status == Status.None, "Trade already exists");
        require(amount > 0, "Amount must be > 0");
        require(amount <= type(uint96).max, "Amount overflow");
        require(token != address(0), "Invalid token");
        require(acceptedTokens[token], "Token not accepted");
        require(merchant != address(0), "Invalid merchant");
        address user = _msgSender();
        require(merchant != user, "Self-trade");

        IERC20(token).safeTransferFrom(user, address(this), amount); // pull source is always the signer
        locked[user][token] += amount;
        accountedBalance[token] += amount;

        trades[tradeId] = Trade({
            seller: user, buyer: merchant, token: token, amount: uint96(amount),
            createdAt: uint40(block.timestamp), autoReleaseAt: 0,
            status: Status.Pending, funding: Funding.DepositedByTaker,
            feeBps: _feeBpsForToken(token), voucherHash: bytes32(0)
        });
        emit TradeOpenedWithDeposit(tradeId, user, merchant, token, amount);
    }

    function setAutoReleaseWindow(uint40 window) external onlyOwner {
        require(window <= MAX_AUTO_RELEASE_WINDOW, "Window too large");
        uint40 old = autoReleaseWindow; autoReleaseWindow = window;
        emit AutoReleaseWindowUpdated(old, window);
    }

    function setAutoReleaseOptIn(bool optedIn) external {
        autoReleaseOptIn[_msgSender()] = optedIn;
        emit AutoReleaseOptInUpdated(_msgSender(), optedIn);
    }

    function markPaid(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending, "Not pending");
        require(_msgSender() == t.buyer, "Only buyer");
        t.status = Status.Paid;
        if (autoReleaseWindow > 0 && autoReleaseOptIn[t.seller]) {
            t.autoReleaseAt = uint40(block.timestamp + autoReleaseWindow);
        }
        emit TradePaid(tradeId);
    }

    function release(bytes32 tradeId) external nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending || t.status == Status.Paid, "Not releasable");
        require(_msgSender() == t.seller, "Only seller");
        _settleRelease(tradeId, t, false);
    }

    function adminRelease(bytes32 tradeId) external onlyAdmin nonReentrant whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending || t.status == Status.Paid, "Not releasable");
        _settleRelease(tradeId, t, false);
    }

    function _settleRelease(bytes32 tradeId, Trade storage t, bool auto_) internal {
        uint96 amt = t.amount;
        require(amt > 0, "Empty trade");
        require(locked[t.seller][t.token] >= amt, "Locked invariant");
        (uint256 toBuyer, uint256 fee) = _splitFee(amt, t.feeBps);
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
        require(sw != address(0), "System wallet not set");
        return sw;
    }

    /// Shared refund: pool-credit for PooledFromMaker, transfer-out for DepositedByTaker.
    /// Guards the voucherFilled decrement so buy-ads (voucherHash==0) never underflow.
    function _refund(Trade storage t) internal {
        uint96 amt = t.amount;
        require(amt > 0, "Empty trade");
        require(locked[t.seller][t.token] >= amt, "Locked invariant");
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
        require(t.status == Status.Pending, "Not pending");
        require(block.timestamp >= uint256(t.createdAt) + paymentWindow, "Timeout not elapsed");
        t.status = Status.Cancelled;
        _refund(t);
        emit TradeCancelled(tradeId, t.amount);
    }

    function raiseDispute(bytes32 tradeId) external whenNotPaused {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Pending || t.status == Status.Paid, "Not disputable");
        address s = _msgSender();
        require(s == t.buyer || s == t.seller, "Not a party");
        t.status = Status.Disputed;
        emit TradeDisputed(tradeId, s);
    }

    function adminResolve(bytes32 tradeId, bool releaseToBuyer) external onlyAdmin nonReentrant {
        Trade storage t = trades[tradeId];
        require(t.status == Status.Disputed, "Not disputed");
        uint96 amt = t.amount;
        require(amt > 0, "Empty trade");
        require(locked[t.seller][t.token] >= amt, "Locked invariant");

        if (releaseToBuyer) {
            (uint256 toBuyer, uint256 fee) = _splitFee(amt, t.feeBps);
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
        require(t.status == Status.Paid, "Not paid");
        require(t.autoReleaseAt != 0, "Auto-release not armed");
        require(block.timestamp >= t.autoReleaseAt, "Window not elapsed");
        _settleRelease(tradeId, t, true);
    }

    // ── Owner setters ──────────────────────────────────────────────────────

    function setFeeBps(uint16 bps) external onlyOwner {
        require(bps <= MAX_FEE_BPS, "Fee too high");
        uint16 old = feeBps; feeBps = bps; emit FeeBpsUpdated(old, bps);
    }

    function setDisputeFeeBps(uint16 bps) external onlyOwner {
        require(bps <= MAX_DISPUTE_FEE_BPS, "Fee too high");
        uint16 old = disputeFeeBps; disputeFeeBps = bps; emit DisputeFeeBpsUpdated(old, bps);
    }

    function setPaymentWindow(uint40 window) external onlyOwner {
        require(window >= MIN_PAYMENT_WINDOW && window <= MAX_PAYMENT_WINDOW, "Window out of bounds");
        uint40 old = paymentWindow; paymentWindow = window; emit PaymentWindowUpdated(old, window);
    }

    function setTrustedForwarder(address fwd) external onlyOwner {
        address old = trustedForwarder; trustedForwarder = fwd; emit TrustedForwarderUpdated(old, fwd);
    }

    function setPermit2(address p) external onlyOwner {
        address old = permit2; permit2 = p; emit Permit2Updated(old, p);
    }

    function setMcgpToken(address _mcgpToken) external onlyOwner {
        require(_mcgpToken != address(0), "Invalid MCGP address");
        require(acceptedTokens[_mcgpToken], "MCGP must be accepted first");
        address old = mcgpToken; mcgpToken = _mcgpToken; emit McgpTokenUpdated(old, _mcgpToken);
    }

    function addAdmin(address account) external onlyOwner {
        require(account != address(0), "Zero address");
        require(!admins[account], "Already admin");
        admins[account] = true; emit AdminAdded(account);
    }

    function removeAdmin(address account) external onlyOwner {
        require(admins[account], "Not admin");
        admins[account] = false; emit AdminRemoved(account);
    }

    // ── Permit2 variants ───────────────────────────────────────────────────

    function depositWithPermit2(IPermit2.PermitTransferFrom calldata permit, bytes calldata signature)
        external nonReentrant whenNotPaused
    {
        require(permit2 != address(0), "Permit2 not set");
        address token = permit.permitted.token;
        require(acceptedTokens[token], "Token not accepted");
        uint256 amount = permit.permitted.amount;
        require(amount > 0, "Amount must be > 0");
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

    function openTradeWithDepositPermit2(
        bytes32 tradeId, address merchant,
        IPermit2.PermitTransferFrom calldata permit, bytes calldata signature
    ) external nonReentrant whenNotPaused {
        require(permit2 != address(0), "Permit2 not set");
        require(tradeId != bytes32(0), "Invalid trade id");
        require(trades[tradeId].status == Status.None, "Trade already exists");
        address token = permit.permitted.token;
        uint256 amount = permit.permitted.amount;
        require(amount > 0 && amount <= type(uint96).max, "Bad amount");
        require(acceptedTokens[token], "Token not accepted");
        require(merchant != address(0), "Invalid merchant");
        address user = _msgSender();
        require(merchant != user, "Self-trade");

        IPermit2(permit2).permitTransferFrom(
            permit,
            IPermit2.SignatureTransferDetails({ to: address(this), requestedAmount: amount }),
            user, signature
        );
        locked[user][token] += amount;
        accountedBalance[token] += amount;
        trades[tradeId] = Trade({
            seller: user, buyer: merchant, token: token, amount: uint96(amount),
            createdAt: uint40(block.timestamp), autoReleaseAt: 0,
            status: Status.Pending, funding: Funding.DepositedByTaker,
            feeBps: _feeBpsForToken(token), voucherHash: bytes32(0)
        });
        emit TradeOpenedWithDeposit(tradeId, user, merchant, token, amount);
    }

    // ── Pause controls ─────────────────────────────────────────────────────

    function pause() external onlyOwner { _pause(); }
    function unpause() external onlyOwner { _unpause(); }

    function setSystemWallet(address _systemWallet) external onlyOwner {
        require(_systemWallet != address(0), "Invalid system wallet");
        address old = systemWallet; systemWallet = _systemWallet;
        emit SystemWalletUpdated(old, _systemWallet);
    }

    function setTokenAcceptance(address token, bool accepted) external onlyOwner {
        require(token != address(0), "Invalid token");
        acceptedTokens[token] = accepted;
        emit TokenAcceptanceUpdated(token, accepted);
    }
}
