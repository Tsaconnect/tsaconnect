// SPDX-License-Identifier: MIT
pragma solidity ^0.8.22;

import "../P2PEscrow.sol";

/// @dev Test-only subclass used to verify that upgrading the proxy preserves
///      the admin mapping and other pre-upgrade state. Adds one harmless view
///      function; does not change any existing storage or behavior.
contract P2PEscrowV2Test is P2PEscrow {
    function version() external pure returns (string memory) {
        return "v2-test";
    }
}
