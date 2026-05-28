// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// Minimal EIP-2771 forwarder: appends `from` (20 bytes) to calldata and relays.
contract MockForwarder {
    function relay(address target, bytes calldata data, address from) external returns (bool ok, bytes memory ret) {
        (ok, ret) = target.call(abi.encodePacked(data, from));
        require(ok, "relay failed");
    }
}
