// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "../FeeManager.sol";

/// @dev Test-only subclass used to verify a no-op UUPS upgrade preserves previously-set rates
///      and storage layout. Adds one harmless view function; changes no existing storage or behavior.
contract FeeManagerV2Test is FeeManager {
    function version() external pure returns (string memory) {
        return "v2-test";
    }
}
