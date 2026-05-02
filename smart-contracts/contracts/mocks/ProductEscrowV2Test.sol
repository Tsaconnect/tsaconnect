// SPDX-License-Identifier: MIT
pragma solidity ^0.8.22;

import "../ProductEscrow.sol";

/// @dev Test-only subclass used to verify that upgrading the proxy preserves
///      the admin mapping and other pre-upgrade state. Adds one harmless view
///      function; does not change any existing storage or behavior.
contract ProductEscrowV2Test is ProductEscrow {
    function version() external pure returns (string memory) {
        return "v2-test";
    }
}
