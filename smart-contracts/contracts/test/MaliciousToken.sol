// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";

interface IEscrowWithdraw {
    function withdraw(address token, uint256 amount) external;
}

/// @dev ERC-20 whose `transfer` attempts to re-enter the escrow's withdraw()
///      when the `armed` flag is set. Used to verify the nonReentrant guard.
contract MaliciousToken is ERC20 {
    address public escrow;
    bool public armed;
    address public attackToken; // the actual whitelisted token to try to withdraw

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

    function transfer(address to, uint256 amount) public override returns (bool) {
        if (armed && escrow != address(0)) {
            armed = false; // prevent infinite recursion
            // Attempt reentrancy: try to withdraw 1 wei from the escrow.
            // This should revert with "ReentrancyGuard: reentrant call".
            try IEscrowWithdraw(escrow).withdraw(address(this), 1) {} catch {}
        }
        return super.transfer(to, amount);
    }
}
