// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";

/// @dev Mock token that attempts to re-enter a configurable function on every
///      outgoing transfer from the configured target. Used solely to verify
///      that ReentrancyGuard prevents double-spend.
///
///      Configure via `armAttack(target, callData)`, then trigger any flow
///      that ends in a transfer FROM the target (e.g. escrow.release pays
///      buyer). On the first such transfer the mock calls `target.call(callData)`
///      and disarms itself, so a properly-guarded function reverts the original
///      tx (since the inner call reverts and SafeERC20 propagates).
contract ReentrantERC20 is ERC20 {
    uint8 private _decimals;
    address public target;
    bytes   public attackCallData;
    bool    public attackArmed;

    constructor(string memory name, string memory symbol, uint8 decimals_)
        ERC20(name, symbol)
    {
        _decimals = decimals_;
    }

    function decimals() public view override returns (uint8) {
        return _decimals;
    }

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    /// @notice Arm a one-shot re-entry attack. The next outgoing transfer
    /// from `_target` will call `_target.call(_callData)`.
    function armAttack(address _target, bytes calldata _callData) external {
        target = _target;
        attackCallData = _callData;
        attackArmed = true;
    }

    function _update(address from, address to, uint256 value) internal override {
        super._update(from, to, value);
        // Only attempt re-entry on transfers OUT of the configured target —
        // not on incoming transferFrom during a deposit.
        if (attackArmed && from == target && from != address(0) && to != address(0)) {
            attackArmed = false; // one-shot so the test fails loud if the guard is absent
            (bool ok, bytes memory ret) = target.call(attackCallData);
            // Bubble up the inner revert reason so the test can match it.
            if (!ok) {
                assembly {
                    revert(add(ret, 0x20), mload(ret))
                }
            }
        }
    }
}
