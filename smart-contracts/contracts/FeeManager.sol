// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";

import "./interfaces/IFeeManager.sol";

/// @title FeeManager
/// @notice On-chain SOURCE OF TRUTH (write side / CQRS) for every fee rate, one instance per chain.
///         See docs/superpowers/specs/2026-06-08-configurable-feemanager-design.md §3 / §3.1.
///
/// @dev    Holds NO funds and needs NO pause: it only stores `(feature, token) -> Rate` rows. The
///         escrows that consume it LIVE-READ via `IFeeManager.rateFor` and are independently pausable;
///         the backend keeps a read replica refreshed on confirmed `RateSet` events (never a hot-path
///         `eth_call`). The only authoritative write is `setRate` / `setRates` (A1 — no escrow-side
///         rate setter to drift).
///
///         Owner is intended to be a Gnosis Safe multisig (optionally wrapped behind a short
///         TimelockController) which is ALSO the UUPS upgrade authority (`_authorizeUpgrade onlyOwner`).
///         The timelock/Safe is wired at deploy time, not in this contract — here it is plain `onlyOwner`.
///         `MAX_BPS` is enforced IN the contract so a fat-finger (e.g. 100%) is impossible on-chain.
contract FeeManager is Initializable, UUPSUpgradeable, OwnableUpgradeable, IFeeManager {
    /// @notice A configurable fee rate for a `(feature, token)` pair.
    struct Rate {
        uint16 bps;        // basis points; 0 allowed for backend-charged "rate=0" cases
        uint64 flatUSD;    // flat component in 1e6 units (e.g. 100000 == $0.10)
        bool   mcgpExempt; // when the token is MCGP this feature charges 0
        bool   active;     // false = feature disabled / not configured
    }

    /// @notice Hard ceiling on `bps` (10%): a fat-finger guard enforced in BOTH setters.
    /// @dev    A `constant`, so changing the ceiling is a deliberate UUPS upgrade — not an admin tx.
    uint16 public constant MAX_BPS = 1000;

    /// @notice `keccak256(abi.encode(feature, tokenSymbol))` => Rate. An unset key returns the zero Rate.
    mapping(bytes32 => Rate) private rates;

    /// @notice Emitted on every successful set (single or batched) — drives the off-chain replica refresh
    ///         and the drift reconciler. `key` mirrors the storage key for indexed lookups.
    event RateSet(
        bytes32 indexed key,
        string feature,
        string token,
        uint16 bps,
        uint64 flatUSD,
        bool mcgpExempt,
        bool active
    );

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    /// @notice Initialize the proxy.
    /// @param initialOwner The admin (intended: a Gnosis Safe multisig) — owns rate writes and upgrades.
    function initialize(address initialOwner) external initializer {
        __Ownable_init(initialOwner);
    }

    function _authorizeUpgrade(address) internal override onlyOwner {}

    // ── Key derivation ──────────────────────────────────────────────────────────

    /// @notice The storage key for a `(feature, token)` pair. Byte-identical on-chain and off-chain.
    function rateKey(string memory feature, string memory token) public pure returns (bytes32) {
        return keccak256(abi.encode(feature, token));
    }

    // ── Reads ─────────────────────────────────────────────────────────────────

    /// @inheritdoc IFeeManager
    /// @dev An UNSET key returns `(0, 0, false, false)` — i.e. `active == false`, signalling
    ///      "not configured" so consumers fall back rather than charge 0 silently.
    function rateFor(string calldata feature, string calldata token)
        external
        view
        returns (uint16 bps, uint64 flatUSD, bool mcgpExempt, bool active)
    {
        Rate storage r = rates[rateKey(feature, token)];
        return (r.bps, r.flatUSD, r.mcgpExempt, r.active);
    }

    // ── Writes (owner-only, MAX_BPS enforced) ───────────────────────────────────

    /// @notice Set a single `(feature, token)` rate. Reverts if `r.bps > MAX_BPS`.
    function setRate(string calldata feature, string calldata token, Rate calldata r) external onlyOwner {
        _setRate(feature, token, r);
    }

    /// @notice Batch-set rates. Arrays must be equal-length; each `bps` must be `<= MAX_BPS`.
    /// @dev    Reverts the whole batch on any invalid bps so a misconfigured row can't land partially.
    function setRates(
        string[] calldata features,
        string[] calldata tokens,
        Rate[] calldata rs
    ) external onlyOwner {
        require(features.length == tokens.length && tokens.length == rs.length, "length mismatch");
        for (uint256 i = 0; i < features.length; i++) {
            _setRate(features[i], tokens[i], rs[i]);
        }
    }

    function _setRate(string calldata feature, string calldata token, Rate calldata r) internal {
        require(r.bps <= MAX_BPS, "rate too high");
        bytes32 key = rateKey(feature, token);
        rates[key] = r;
        emit RateSet(key, feature, token, r.bps, r.flatUSD, r.mcgpExempt, r.active);
    }

    /// @dev Storage gap for future state in UUPS upgrades. Keep the layout append-only above this.
    uint256[49] private __gap;
}
