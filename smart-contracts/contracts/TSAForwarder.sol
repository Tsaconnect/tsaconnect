// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/metatx/ERC2771Forwarder.sol";

/**
 * @title TSAForwarder
 * @notice The single shared EIP-2771 forwarder for TSA Connect. Deployed once per
 *         chain; each ERC-2771-aware contract opts in by setting it as its
 *         `trustedForwarder`.
 *
 * @dev This is stock `ERC2771Forwarder` — nothing is overridden. It is declared
 *      here only so the contract is compiled and deployable from this repo.
 *
 *      IMPORTANT, because it is easy to assume otherwise: `execute` is
 *      PERMISSIONLESS. Anyone can submit a validly signed ForwardRequest, not
 *      just our relayer. The relayer's allowlist, rate limits and gas ceiling
 *      protect the relayer's own balance — they are NOT access control on the
 *      target contract. The only on-chain guarantees are the forwarder's EIP-712
 *      signature check, its per-`from` nonce, the request deadline, and whatever
 *      the target enforces in `_msgSender()`.
 *
 *      Corollary: prepared requests should carry SHORT deadlines. Until it
 *      expires, a signed request is executable by anyone, at a block of their
 *      choosing.
 */
contract TSAForwarder is ERC2771Forwarder {
    constructor(string memory name) ERC2771Forwarder(name) {}
}
