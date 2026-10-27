// SPDX-License-Identifier: MIT
pragma solidity 0.8.24;

import {Initializable} from "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import {UUPSUpgradeable} from "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import {AccessControlDefaultAdminRulesUpgradeable} from
    "@openzeppelin/contracts-upgradeable/access/extensions/AccessControlDefaultAdminRulesUpgradeable.sol";
import {IFeeConfig} from "./interfaces/IFeeConfig.sol";

/// @title FeeConfig
/// @notice One per chain: fee rates per (feature, token address), value limits per token, and the
///         single fee recipient. Holds no funds.
/// @dev    Escrows snapshot the fee when an order or trade is created, so a change here never
///         affects one already in flight; only the recipient is read at settlement.
contract FeeConfig is Initializable, UUPSUpgradeable, AccessControlDefaultAdminRulesUpgradeable, IFeeConfig {
    bytes32 public constant CONFIG_ROLE = keccak256("CONFIG_ROLE");
    bytes32 public constant UPGRADER_ROLE = keccak256("UPGRADER_ROLE");

    /// @notice Hard ceiling on any rate (10%). Consumers enforce their own, tighter caps too.
    uint16 public constant MAX_BPS = 1000;

    /// @custom:storage-location erc7201:tsa.storage.FeeConfig
    struct FeeConfigStorage {
        mapping(bytes32 feature => mapping(address token => Rate)) rates;
        mapping(address token => Limits) limits;
        address feeRecipient;
    }

    // keccak256(abi.encode(uint256(keccak256("tsa.storage.FeeConfig")) - 1)) & ~bytes32(uint256(0xff))
    bytes32 private constant STORAGE_SLOT = 0xf92ed99dca62b472a8cc3dc532b0e34ff1e69c4450411a2689c052982def7600;

    event RateSet(bytes32 indexed feature, address indexed token, uint16 bps, uint128 flat, bool active);
    event LimitsSet(address indexed token, uint128 maxPerOrder, uint128 maxOwed);
    event FeeRecipientSet(address indexed previous, address indexed current);

    error RateTooHigh(uint16 bps);
    error ZeroAddress();
    error ZeroLimit();
    error LengthMismatch();

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    /// @notice The configuration applied at initialization (same rules as the setters).
    struct InitialConfig {
        bytes32[] features;
        address[] tokens;
        Rate[] rates;
        address[] limitTokens;
        Limits[] limits;
    }

    /// @notice Set up roles and the initial configuration in one step, so the admin owns the
    ///         contract from its first block and the deployer never holds a role.
    /// @param admin     Holds every role (DEFAULT_ADMIN, CONFIG, UPGRADER).
    /// @param recipient The initial fee recipient.
    /// @param initial   Rate rows (feature, token, rate) and per-token limits.
    function initialize(address admin, address recipient, InitialConfig calldata initial) external initializer {
        if (admin == address(0)) revert ZeroAddress();
        __AccessControlDefaultAdminRules_init(0, admin);
        _grantRole(CONFIG_ROLE, admin);
        _grantRole(UPGRADER_ROLE, admin);
        _setFeeRecipient(recipient);
        if (initial.features.length != initial.tokens.length || initial.tokens.length != initial.rates.length) {
            revert LengthMismatch();
        }
        for (uint256 i; i < initial.features.length; ++i) {
            _setRate(initial.features[i], initial.tokens[i], initial.rates[i]);
        }
        if (initial.limitTokens.length != initial.limits.length) revert LengthMismatch();
        for (uint256 i; i < initial.limitTokens.length; ++i) {
            _setLimits(initial.limitTokens[i], initial.limits[i]);
        }
    }

    // ── Reads ──────────────────────────────────────────────────────────────────

    /// @inheritdoc IFeeConfig
    function rateFor(bytes32 feature, address token) external view returns (Rate memory r) {
        r = _s().rates[feature][token];
        if (!r.active) revert FeeNotConfigured(feature, token);
    }

    /// @inheritdoc IFeeConfig
    function limitsFor(address token) external view returns (Limits memory l) {
        l = _s().limits[token];
        if (l.maxPerOrder == 0) revert LimitsNotConfigured(token);
    }

    /// @inheritdoc IFeeConfig
    function feeRecipient() external view returns (address) {
        return _s().feeRecipient;
    }

    /// @notice The stored row, active or not (for dashboards; consumers use rateFor).
    function rawRate(bytes32 feature, address token) external view returns (Rate memory) {
        return _s().rates[feature][token];
    }

    // ── Writes (CONFIG_ROLE) ───────────────────────────────────────────────────

    function setRate(bytes32 feature, address token, Rate calldata r) external onlyRole(CONFIG_ROLE) {
        _setRate(feature, token, r);
    }

    /// @notice All-or-nothing batch: one bad row reverts the whole call.
    function setRates(bytes32[] calldata features, address[] calldata tokens, Rate[] calldata rs)
        external
        onlyRole(CONFIG_ROLE)
    {
        if (features.length != tokens.length || tokens.length != rs.length) revert LengthMismatch();
        for (uint256 i; i < features.length; ++i) {
            _setRate(features[i], tokens[i], rs[i]);
        }
    }

    function setLimits(address token, Limits calldata l) external onlyRole(CONFIG_ROLE) {
        _setLimits(token, l);
    }

    function setFeeRecipient(address recipient) external onlyRole(CONFIG_ROLE) {
        _setFeeRecipient(recipient);
    }

    // ── Internals ──────────────────────────────────────────────────────────────

    function _setRate(bytes32 feature, address token, Rate calldata r) private {
        if (token == address(0)) revert ZeroAddress();
        if (r.bps > MAX_BPS) revert RateTooHigh(r.bps);
        _s().rates[feature][token] = r;
        emit RateSet(feature, token, r.bps, r.flatFee, r.active);
    }

    function _setLimits(address token, Limits calldata l) private {
        if (token == address(0)) revert ZeroAddress();
        if (l.maxPerOrder == 0 || l.maxOwed == 0) revert ZeroLimit();
        _s().limits[token] = l;
        emit LimitsSet(token, l.maxPerOrder, l.maxOwed);
    }

    function _setFeeRecipient(address recipient) private {
        if (recipient == address(0)) revert ZeroAddress();
        FeeConfigStorage storage s = _s();
        emit FeeRecipientSet(s.feeRecipient, recipient);
        s.feeRecipient = recipient;
    }

    function _authorizeUpgrade(address) internal override onlyRole(UPGRADER_ROLE) {}

    function _s() private pure returns (FeeConfigStorage storage s) {
        assembly {
            s.slot := STORAGE_SLOT
        }
    }
}
