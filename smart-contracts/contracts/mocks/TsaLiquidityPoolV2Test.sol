// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "../TsaLiquidityPool.sol";

/// @dev Upgrade target for the timelock test: append-only, adds one view.
contract TsaLiquidityPoolV2Test is TsaLiquidityPool {
    function version() external pure returns (string memory) {
        return "v2-test";
    }
}
