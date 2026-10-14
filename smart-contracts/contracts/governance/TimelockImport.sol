// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

// Compiles OpenZeppelin's TimelockController so deploy scripts and tests can
// deploy it as the owner of TsaLiquidityPool (spec 2026-10-14 O1).
import "@openzeppelin/contracts/governance/TimelockController.sol";
