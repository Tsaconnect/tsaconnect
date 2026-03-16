// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/utils/Pausable.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

contract ServiceContact is Ownable, ReentrancyGuard, Pausable {
    using SafeERC20 for IERC20;

    // Fee amount in token units (default: 100000 = $0.10 for 6-decimal tokens)
    uint256 public feeAmount = 100000;

    // Minimum fee to prevent rounding-to-zero splits (must produce non-zero for all 4 splits)
    uint256 public constant MIN_FEE_AMOUNT = 10000;

    // Split basis points (out of feeAmount)
    // $0.05 system (50%), $0.025 provider (25%), $0.0125 caller cashback (12.5%), $0.0125 upline (12.5%)
    uint256 public constant PROVIDER_BPS = 2500;  // 25%
    uint256 public constant CALLER_BPS = 1250;    // 12.5%
    uint256 public constant UPLINE_BPS = 1250;    // 12.5%
    // System gets remainder (absorbs rounding dust)

    // Accepted tokens
    mapping(address => bool) public acceptedTokens;

    event ContactFeePaid(
        address indexed caller,
        address indexed serviceProvider,
        address indexed token,
        uint256 amount,
        address upline
    );
    event FeeAmountUpdated(uint256 oldAmount, uint256 newAmount);
    event TokenAcceptanceUpdated(address indexed token, bool accepted);
    event ExcessSwept(address indexed token, uint256 amount);

    constructor(address initialOwner) Ownable(initialOwner) {}

    function payContactFee(
        address serviceProvider,
        address requesterUpline,
        address token
    ) external nonReentrant whenNotPaused {
        require(serviceProvider != address(0), "Invalid service provider");
        require(msg.sender != serviceProvider, "Cannot pay for own contact");
        require(token != address(0), "Invalid token");
        require(acceptedTokens[token], "Token not accepted");

        IERC20 erc20 = IERC20(token);

        // Pull fee from caller
        erc20.safeTransferFrom(msg.sender, address(this), feeAmount);

        // Calculate splits — system gets remainder to absorb rounding dust
        uint256 providerAmount = (feeAmount * PROVIDER_BPS) / 10000;
        uint256 callerCashback = (feeAmount * CALLER_BPS) / 10000;
        uint256 uplineAmount = (feeAmount * UPLINE_BPS) / 10000;
        uint256 systemAmount = feeAmount - providerAmount - callerCashback - uplineAmount;

        // Distribute
        if (requesterUpline == address(0)) {
            erc20.safeTransfer(owner(), systemAmount + uplineAmount);
        } else {
            erc20.safeTransfer(owner(), systemAmount);
            erc20.safeTransfer(requesterUpline, uplineAmount);
        }
        erc20.safeTransfer(serviceProvider, providerAmount);
        erc20.safeTransfer(msg.sender, callerCashback);

        emit ContactFeePaid(msg.sender, serviceProvider, token, feeAmount, requesterUpline);
    }

    function setFeeAmount(uint256 newFeeAmount) external onlyOwner {
        require(newFeeAmount >= MIN_FEE_AMOUNT, "Fee below minimum");
        uint256 oldAmount = feeAmount;
        feeAmount = newFeeAmount;
        emit FeeAmountUpdated(oldAmount, newFeeAmount);
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
}
