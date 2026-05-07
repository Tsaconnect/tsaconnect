// SPDX-License-Identifier: MIT
pragma solidity ^0.8.22;

import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

/// @title ServiceContact (UUPS upgradeable)
/// @notice Caller pays a per-token contact fee to reveal a service provider's
///         off-chain details. Fee is split between the system, provider,
///         caller (cashback), and the caller's referral upline.
///
/// @dev v2 of this contract makes the fee per-token instead of a single global
///      value, so the same contract can accept tokens of different decimals
///      (e.g. 6-decimal USDC alongside 18-decimal MCGP). It is UUPS
///      upgradeable so future fee/split changes can ship without
///      redeploying.
contract ServiceContact is
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
    // 50% system, 25% provider, 12.5% caller cashback, 12.5% upline
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

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(address initialOwner) external initializer {
        __Ownable_init(initialOwner);
        __Pausable_init();
    }

    /// @dev Restricts upgrade authority to owner.
    function _authorizeUpgrade(address) internal override onlyOwner {}

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
        require(fee >= MIN_FEE_AMOUNT, "Fee not configured for token");

        IERC20 erc20 = IERC20(token);

        // Pull fee from caller
        erc20.safeTransferFrom(msg.sender, address(this), fee);

        // Calculate splits — system gets remainder to absorb rounding dust
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

        emit ContactFeePaid(msg.sender, serviceProvider, token, fee, requesterUpline);
    }

    /// @notice Set the per-token fee. Pass 0 to clear (effectively disables payment in that token
    ///         even if it remains in the acceptedTokens whitelist).
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
    ///      without shifting existing slots.
    uint256[48] private __gap;
}
