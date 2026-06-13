// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/Pausable.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/utils/cryptography/MerkleProof.sol";
import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

/// @title MCGPRewardDistributor
/// @notice Standard CUMULATIVE Merkle distributor for MCGP rewards.
/// @dev See docs/superpowers/specs/2026-06-07-fee-tp-overhaul-design.md (§0 payout-rail row, §5 SP6).
///
///      An admin (the `owner`) periodically publishes a merkle root whose leaves encode each
///      account's *cumulative* claimable MCGP (in base units, 18 decimals). The treasury funds the
///      contract by transferring MCGP in (or via `fund()`). Users claim `cumulativeAmount - claimed`
///      with an app-signed tx + merkle proof. Claims are replay-safe and monotonic by construction:
///      `claimed[account]` only ever moves up, and a stale (lower-cumulative) leaf can never claw back.
///
///      LEAF ENCODING (must be byte-identical on-chain and in the off-chain Go tree builder, or every
///      claim fails ecrecover-style):
///
///          leaf = keccak256(bytes.concat(keccak256(abi.encode(account, cumulativeAmount))))
///
///      i.e. the OpenZeppelin double-hashed leaf (second-preimage safe). `abi.encode(account, amount)`
///      is the 32-byte left-padded address followed by the 32-byte big-endian amount (64 bytes total),
///      then keccak256, then keccak256 again. The tree is built with OZ-style SORTED-PAIR parent
///      hashing and verified here with `MerkleProof.verifyCalldata` (which sorts each pair).
contract MCGPRewardDistributor is Ownable, Pausable, ReentrancyGuard {
    using SafeERC20 for IERC20;

    /// @notice The MCGP token this distributor pays out. Immutable — set once at construction.
    IERC20 public immutable mcgp;

    /// @notice Current merkle root. Encodes CUMULATIVE per-account claimable amounts.
    bytes32 public merkleRoot;

    /// @notice Per-account cumulative amount already claimed (base units, 18 decimals).
    /// @dev Monotonically non-decreasing. `claim` pays exactly `cumulativeAmount - claimed[account]`.
    mapping(address => uint256) public claimed;

    /// @notice Total MCGP ever paid out across all claims (base units). Bookkeeping for ops/reconciler.
    uint256 public totalClaimed;

    event MerkleRootUpdated(bytes32 indexed oldRoot, bytes32 indexed newRoot);
    event Claimed(address indexed account, uint256 amount, uint256 cumulativeAmount);
    event Funded(address indexed from, uint256 amount);
    event Rescued(address indexed to, address indexed token, uint256 amount);

    /// @param mcgpToken   The MCGP ERC-20 to distribute. Must be non-zero.
    /// @param initialOwner The admin that publishes roots, pauses, and rescues unallocated funds.
    constructor(address mcgpToken, address initialOwner) Ownable(initialOwner) {
        require(mcgpToken != address(0), "MCGP: zero token");
        mcgp = IERC20(mcgpToken);
    }

    // ── Root management (admin-signed, low-frequency) ──────────────────────────

    /// @notice Publish a new cumulative merkle root for the current epoch.
    /// @dev Roots may be updated each epoch; each new root MUST encode amounts >= the prior root for
    ///      every account (cumulative). Lowering an account's cumulative in a new root cannot claw back
    ///      already-claimed funds (claim requires `cumulativeAmount > claimed[account]`), but would
    ///      simply make that account unable to claim until the cumulative exceeds what they've claimed.
    function setMerkleRoot(bytes32 root) external onlyOwner {
        bytes32 old = merkleRoot;
        merkleRoot = root;
        emit MerkleRootUpdated(old, root);
    }

    // ── Claim ──────────────────────────────────────────────────────────────────

    /// @notice Claim newly-vested MCGP: pays `cumulativeAmount - claimed[account]` to `account`.
    /// @param account          The reward recipient (proof is bound to this address).
    /// @param cumulativeAmount The account's TOTAL lifetime claimable MCGP per the current root (base units).
    /// @param proof            Merkle proof of the leaf against `merkleRoot`.
    ///
    /// @dev Permissionless caller: anyone may submit a valid proof, but funds always go to `account`,
    ///      so there is no value in a third party calling it (it only spends their gas on the user's behalf).
    ///      Replay-safe + monotonic: a repeated or stale (lower) cumulative reverts via the
    ///      `cumulativeAmount > claimed[account]` guard. Reverts cleanly if underfunded (SafeERC20 transfer).
    function claim(
        address account,
        uint256 cumulativeAmount,
        bytes32[] calldata proof
    ) external nonReentrant whenNotPaused {
        require(merkleRoot != bytes32(0), "MCGP: no root");

        uint256 alreadyClaimed = claimed[account];
        require(cumulativeAmount > alreadyClaimed, "MCGP: nothing to claim");

        bytes32 leaf = _leaf(account, cumulativeAmount);
        require(MerkleProof.verifyCalldata(proof, merkleRoot, leaf), "MCGP: bad proof");

        uint256 payable_ = cumulativeAmount - alreadyClaimed;

        // Effects before interaction (CEI) — also reentrancy-guarded.
        claimed[account] = cumulativeAmount;
        totalClaimed += payable_;

        // Reverts cleanly if the contract is underfunded.
        mcgp.safeTransfer(account, payable_);

        emit Claimed(account, payable_, cumulativeAmount);
    }

    /// @notice The OZ double-hashed, second-preimage-safe leaf for (account, cumulativeAmount).
    /// @dev MUST stay byte-identical to the off-chain Go tree builder.
    function _leaf(address account, uint256 cumulativeAmount) internal pure returns (bytes32) {
        return keccak256(bytes.concat(keccak256(abi.encode(account, cumulativeAmount))));
    }

    /// @notice Pure view of the leaf encoding — exposed so the backend can assert byte-identical output.
    function leafHash(address account, uint256 cumulativeAmount) external pure returns (bytes32) {
        return _leaf(account, cumulativeAmount);
    }

    /// @notice Convenience preview: how much `account` could claim against the current root.
    /// @dev Does NOT verify a proof — purely `cumulativeAmount - claimed`, clamped at 0. UI/ops helper.
    function claimable(address account, uint256 cumulativeAmount) external view returns (uint256) {
        uint256 c = claimed[account];
        return cumulativeAmount > c ? cumulativeAmount - c : 0;
    }

    // ── Treasury funding / rescue ───────────────────────────────────────────────

    /// @notice Pull `amount` MCGP from the caller into the distributor.
    /// @dev Optional helper — a plain `transfer` of MCGP to this address funds it just as well.
    function fund(uint256 amount) external {
        require(amount > 0, "MCGP: zero amount");
        mcgp.safeTransferFrom(msg.sender, address(this), amount);
        emit Funded(msg.sender, amount);
    }

    /// @notice Owner rescue/withdraw of tokens held by this contract.
    /// @dev Use to reclaim UNALLOCATED MCGP (e.g. over-funding) or any unrelated token sent here by
    ///      mistake. There is no on-chain notion of "allocated but unclaimed" (that lives off-chain in
    ///      the published root), so the owner is trusted not to rug pending claims — same trust model as
    ///      who controls the root. Sends to an explicit recipient for flexibility.
    function rescue(address token, address to, uint256 amount) external onlyOwner {
        require(to != address(0), "MCGP: zero recipient");
        require(amount > 0, "MCGP: zero amount");
        IERC20(token).safeTransfer(to, amount);
        emit Rescued(to, token, amount);
    }

    // ── Pause controls ──────────────────────────────────────────────────────────

    function pause() external onlyOwner { _pause(); }
    function unpause() external onlyOwner { _unpause(); }
}
