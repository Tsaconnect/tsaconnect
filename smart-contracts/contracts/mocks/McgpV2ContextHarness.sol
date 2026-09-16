// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "../McgpEntitlementSwapV2.sol";

/// @dev Test-only subclass that exposes the ERC-2771 context overrides.
///
///      `_msgData` and `_contextSuffixLength` are never called by V2's own code —
///      they exist so that any future logic (or inherited OZ code) which does use
///      them strips the appended sender consistently with `_msgSender`. That makes
///      them untestable from outside without a harness, and untested overrides are
///      exactly where a forwarder-aware contract goes subtly wrong.
contract McgpV2ContextHarness is McgpEntitlementSwapV2 {
    function exposedMsgSender() external view returns (address) {
        return _msgSender();
    }

    function exposedMsgData() external view returns (bytes memory) {
        return _msgData();
    }

    function exposedSuffixLength() external view returns (uint256) {
        return _contextSuffixLength();
    }
}
