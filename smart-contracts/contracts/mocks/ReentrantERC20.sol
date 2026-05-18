// SPDX-License-Identifier: MIT
pragma solidity ^0.8.22;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";

interface IP2PEscrow {
    function release(bytes32 tradeId) external;
    function cancelByTimeout(bytes32 tradeId) external;
}

/// @dev Mock token that attempts to re-enter a P2PEscrow function on every
///      transfer. Used solely to verify ReentrancyGuard prevents double-spend.
///
///      Set `target` and `tradeId` via configureAttack, then trigger any flow
///      that ends in a transfer (release / adminResolve(true)) — the token
///      will attempt to call `target.release(tradeId)` from inside `transfer`,
///      which must revert with the ReentrancyGuard error.
contract ReentrantERC20 is ERC20 {
    uint8 private _decimals;
    address public target;
    bytes32 public tradeId;
    bool    public attackArmed;

    constructor(string memory name, string memory symbol, uint8 decimals_)
        ERC20(name, symbol)
    {
        _decimals = decimals_;
    }

    function decimals() public view override returns (uint8) {
        return _decimals;
    }

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    function configureAttack(address _target, bytes32 _tradeId) external {
        target = _target;
        tradeId = _tradeId;
        attackArmed = true;
    }

    function _update(address from, address to, uint256 value) internal override {
        super._update(from, to, value);
        // Only attempt re-entry on outgoing transfers from the escrow (i.e. when
        // the escrow is paying somebody) — not on the initial inbound transferFrom.
        if (attackArmed && from == target && from != address(0) && to != address(0)) {
            attackArmed = false; // one-shot to avoid infinite recursion if guard absent
            IP2PEscrow(target).release(tradeId);
        }
    }
}
