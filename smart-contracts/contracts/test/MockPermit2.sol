// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";

/// Minimal Permit2 stand-in: ignores the signature and just does a transferFrom
/// (the user must have approved THIS mock). Exercises the contract's Permit2 path.
contract MockPermit2 {
    struct TokenPermissions { address token; uint256 amount; }
    struct PermitTransferFrom { TokenPermissions permitted; uint256 nonce; uint256 deadline; }
    struct SignatureTransferDetails { address to; uint256 requestedAmount; }
    function permitTransferFrom(
        PermitTransferFrom calldata permit,
        SignatureTransferDetails calldata d,
        address owner,
        bytes calldata
    ) external {
        IERC20(permit.permitted.token).transferFrom(owner, d.to, d.requestedAmount);
    }
}
