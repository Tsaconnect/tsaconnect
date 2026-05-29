// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";

interface IEscrowDeposit {
    function deposit(address token, uint256 amount) external;
}

/// @dev ERC-20 whose `transfer` re-enters a DIFFERENT nonReentrant function on the
///      escrow (deposit) when armed. This exercises the reentrancy guard directly:
///      - The outer call is withdraw() (nonReentrant, already ENTERED)
///      - During the ERC-20 transfer payout, transfer() calls deposit() (also nonReentrant)
///      - deposit() hits "ReentrancyGuard: reentrant call" because _reentrancyStatus == ENTERED
///      - The revert BUBBLES up (no try/catch), causing the outer withdraw() to also revert
///
///      Without the guard, deposit() would proceed normally (CEI on withdraw would not stop it).
///      The test therefore proves the guard fired, not just a balance check.
contract MaliciousToken is ERC20 {
    address public escrow;
    bool public armed;

    constructor() ERC20("Malicious", "MAL") {}

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    function arm(address _escrow) external {
        escrow = _escrow;
        armed = true;
    }

    function disarm() external {
        armed = false;
    }

    /// @dev On the first transfer out from the escrow (during withdraw payout), re-enter
    ///      deposit() — a DIFFERENT nonReentrant function — so the guard fires rather than
    ///      a balance check. The revert is NOT caught; it bubbles to the outer withdraw().
    function transfer(address to, uint256 amount) public override returns (bool) {
        if (armed && escrow != address(0)) {
            armed = false; // prevent infinite recursion
            // Re-enter deposit() — a different nonReentrant function.
            // This must revert with "ReentrancyGuard: reentrant call".
            // We do NOT catch; the revert bubbles to the outer withdraw() call.
            IEscrowDeposit(escrow).deposit(address(this), 1);
        }
        return super.transfer(to, amount);
    }
}
