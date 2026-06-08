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

/// @title ServiceContactV2 (UUPS upgradeable)
/// @notice Caller pays a per-token contact fee to reveal a service provider's off-chain details.
///         UUPS implementation upgrade of ServiceContact that adds the A1 live-read wiring and the
///         `legacySplitEnabled` split gate (see
///         docs/superpowers/specs/2026-06-08-configurable-feemanager-design.md §3.1 / §7.3).
///         The proxy address and full V1 storage layout are preserved; `feeManager` /
///         `legacySplitEnabled` / `tokenSymbol` are APPENDED before `__gap` (which shrinks 48 → 45).
///
/// @dev    IMPORTANT (§7.3 / I2): `feeAmount` stays > 0. Zeroing it would brick `payContactFee` at
///         the `require(fee >= MIN_FEE_AMOUNT)` guard. The split is zeroed via the `legacySplitEnabled`
///         branch, NOT by zeroing the fee. The flat fee stays mirrored into `feeAmounts` (the one
///         documented A1 exception — the fee is a token-denominated flat, not bps).
contract ServiceContactV2 is
    Initializable,
    UUPSUpgradeable,
    OwnableUpgradeable,
    ReentrancyGuard,
    PausableUpgradeable
{
    using SafeERC20 for IERC20;

    // Minimum fee to prevent rounding-to-zero in any of the four splits.
    // Applied uniformly because the smallest split is 12.5% of the fee,
    // and 10000 / 8 = 1250 still leaves a non-zero amount everywhere.
    uint256 public constant MIN_FEE_AMOUNT = 10000;

    // Split basis points (out of 10000)
    // 50% system, 25% provider, 12.5% caller cashback, 12.5% upline — legacy-split branch.
    uint256 public constant PROVIDER_BPS = 2500; // 25%
    uint256 public constant CALLER_BPS = 1250;   // 12.5%
    uint256 public constant UPLINE_BPS = 1250;   // 12.5%
    // System gets the remainder (absorbs rounding dust).

    /// @notice Per-token fee in the token's smallest unit.
    ///         e.g. USDC (6 dec): 100000 = $0.10
    ///              MCGP (18 dec, ~$0.02): 5e18 = ~$0.10 worth
    mapping(address => uint256) public feeAmounts;

    /// @notice Whitelist of tokens that may be used to pay the contact fee.
    mapping(address => bool) public acceptedTokens;

    // ── V2 appended storage (A1 live-read + legacy-split gate, §3.1 / §7.3) ──
    /// @notice FeeManager proxy for this chain (the on-chain rate source of truth). +1 slot.
    address public feeManager;
    /// @notice Deployed TRUE; flipped FALSE at cutover (D5). Gates the on-chain 4-way split. +1 slot.
    bool    public legacySplitEnabled;
    /// @notice Owner-set token symbol per accepted token, used as the FeeManager key. +1 slot.
    mapping(address => string) public tokenSymbol;

    event ContactFeePaid(
        address indexed caller,
        address indexed serviceProvider,
        address indexed token,
        uint256 amount,
        address upline
    );
    event FeeAmountUpdated(address indexed token, uint256 oldAmount, uint256 newAmount);
    event TokenAcceptanceUpdated(address indexed token, bool accepted);
    event ExcessSwept(address indexed token, uint256 amount);
    // V2 additions
    event FeeManagerUpdated(address indexed oldFm, address indexed newFm);
    event LegacySplitEnabledUpdated(bool enabled);
    event TokenSymbolUpdated(address indexed token, string symbol);

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(address initialOwner) external initializer {
        __Ownable_init(initialOwner);
        __Pausable_init();

        legacySplitEnabled = true; // deployed TRUE (today's behavior — §9 cutover)
    }

    /// @dev Restricts upgrade authority to owner.
    function _authorizeUpgrade(address) internal override onlyOwner {}

    /// @notice One-shot V2 re-initializer, run via upgradeProxy's `call` option at the V1→V2 upgrade.
    /// @dev `upgradeProxy` does NOT re-run `initialize` (initializer modifier), so the freshly-added
    ///      `legacySplitEnabled` slot would otherwise read zero=false — silently bypassing the
    ///      provider/caller/upline/system 4-way split on the first payContactFee after upgrade
    ///      (100% to owner). `reinitializer(2)` runs exactly once on the live proxy to set it TRUE,
    ///      honoring the spec D5 / §9-step-1 "deployed TRUE = byte-for-byte today" guarantee.
    function initializeV2() external reinitializer(2) {
        legacySplitEnabled = true;
    }

    function payContactFee(
        address serviceProvider,
        address requesterUpline,
        address token
    ) external nonReentrant whenNotPaused {
        require(serviceProvider != address(0), "Invalid service provider");
        require(msg.sender != serviceProvider, "Cannot pay for own contact");
        require(token != address(0), "Invalid token");
        require(acceptedTokens[token], "Token not accepted");

        uint256 fee = feeAmounts[token];
        require(fee >= MIN_FEE_AMOUNT, "Fee not configured for token"); // UNCHANGED — fee stays > 0

        IERC20 erc20 = IERC20(token);

        // Pull fee from caller
        erc20.safeTransferFrom(msg.sender, address(this), fee);

        if (legacySplitEnabled) {
            // EXISTING 4-way split — system gets remainder to absorb rounding dust
            uint256 providerAmount = (fee * PROVIDER_BPS) / 10000;
            uint256 callerCashback = (fee * CALLER_BPS) / 10000;
            uint256 uplineAmount = (fee * UPLINE_BPS) / 10000;
            uint256 systemAmount = fee - providerAmount - callerCashback - uplineAmount;

            // Distribute
            if (requesterUpline == address(0)) {
                erc20.safeTransfer(owner(), systemAmount + uplineAmount);
            } else {
                erc20.safeTransfer(owner(), systemAmount);
                erc20.safeTransfer(requesterUpline, uplineAmount);
            }
            erc20.safeTransfer(serviceProvider, providerAmount);
            erc20.safeTransfer(msg.sender, callerCashback);
        } else {
            // D5 cutover: 100% of the fee to system; off-chain v2 engine does the
            // provider(25%)/caller(12.5%)/upline(12.5%) split.
            erc20.safeTransfer(owner(), fee);
        }

        emit ContactFeePaid(msg.sender, serviceProvider, token, fee, requesterUpline);
    }

    /// @notice Set the per-token fee. Pass 0 to clear (effectively disables payment in that token
    ///         even if it remains in the acceptedTokens whitelist).
    /// @dev    To DISABLE the on-chain split (D5 cutover) use `setLegacySplitEnabled(false)` — do NOT
    ///         zero `feeAmount` (that bricks payContactFee at the MIN_FEE_AMOUNT require).
    function setFeeAmount(address token, uint256 newFeeAmount) external onlyOwner {
        require(token != address(0), "Invalid token");
        require(newFeeAmount == 0 || newFeeAmount >= MIN_FEE_AMOUNT, "Fee below minimum");
        uint256 oldAmount = feeAmounts[token];
        feeAmounts[token] = newFeeAmount;
        emit FeeAmountUpdated(token, oldAmount, newFeeAmount);
    }

    function setTokenAcceptance(address token, bool accepted) external onlyOwner {
        require(token != address(0), "Invalid token");
        acceptedTokens[token] = accepted;
        emit TokenAcceptanceUpdated(token, accepted);
    }

    // --- V2 owner setters (A1 live-read wiring + split gate, §3.1 / §7.3) ---

    /// @notice Wire the per-chain FeeManager. Set once at upgrade (§3.1).
    function setFeeManager(address fm) external onlyOwner {
        require(fm != address(0), "fm=0");
        address old = feeManager; feeManager = fm; emit FeeManagerUpdated(old, fm);
    }

    /// @notice Toggle the legacy on-chain 4-way split (deployed TRUE; flipped FALSE at cutover — D5).
    function setLegacySplitEnabled(bool on) external onlyOwner {
        legacySplitEnabled = on; emit LegacySplitEnabledUpdated(on);
    }

    /// @notice Set the FeeManager-keying symbol for an accepted token (owner-set, §3.1).
    function setTokenSymbol(address token, string calldata sym) external onlyOwner {
        require(token != address(0), "Invalid token");
        tokenSymbol[token] = sym;
        emit TokenSymbolUpdated(token, sym);
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

    /// @dev Reserved storage to allow new state variables in future upgrades
    ///      without shifting existing slots. V2 added feeManager+legacySplitEnabled (PACK into one
    ///      slot) and tokenSymbol (one slot) → 2 new slots → 48 - 2 = 46. (OZ plugin authoritative.)
    uint256[46] private __gap;
}
