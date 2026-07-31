// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "../McgpEntitlementSwapV2.sol";

/// @dev Test-only successor used to prove the V2 storage layout is upgrade-safe
///      and that a real upgrade preserves every ledger. Appends one storage slot
///      plus a view function; changes no existing storage or behaviour.
///
///      Appending here (rather than editing V2) is the shape a real V3 would take:
///      new state lands after V2's whole layout, including its `__gap`.
contract McgpEntitlementSwapV3Test is McgpEntitlementSwapV2 {
    uint256 public appendedSlot;

    function setAppendedSlot(uint256 v) external onlyOwner {
        appendedSlot = v;
    }

    function version() external pure returns (string memory) {
        return "v3-test";
    }
}
