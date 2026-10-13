// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "../P2PEscrowV3.sol";

/// @dev Test-only subclass used to prove a deterministically deployed (CREATE2 +
///      forceImport) P2PEscrowV3 proxy still upgrades through the OpenZeppelin
///      plugin. Adds one harmless view; no storage or behaviour change.
/// @custom:oz-upgrades-unsafe-allow missing-initializer
contract P2PEscrowV3UpgradeTest is P2PEscrowV3 {
    function upgradeMarker() external pure returns (string memory) {
        return "v3-upgrade-test";
    }
}
