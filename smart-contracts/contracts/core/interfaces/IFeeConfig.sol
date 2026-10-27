// SPDX-License-Identifier: MIT
pragma solidity 0.8.24;

/// @title IFeeConfig
/// @notice What the escrows read from the per-chain FeeConfig: the fee for a (feature, token),
///         the value limits for a token, and where fees are paid.
interface IFeeConfig {
    /// @notice A fee row. `flatFee` is in the token's own base units (chains differ in decimals).
    struct Rate {
        uint16 bps;
        uint128 flatFee;
        bool active;
    }

    /// @notice Value limits per token, in the token's base units. Both must be non-zero.
    struct Limits {
        uint128 maxPerOrder;
        uint128 maxOwed;
    }

    error FeeNotConfigured(bytes32 feature, address token);
    error LimitsNotConfigured(address token);

    /// @notice The active rate for `(feature, token)`. Reverts with FeeNotConfigured when unset or
    ///         inactive, so an unconfigured token can never be used (the rate table is the allowlist).
    function rateFor(bytes32 feature, address token) external view returns (Rate memory);

    /// @notice The limits for `token`. Reverts with LimitsNotConfigured when unset.
    function limitsFor(address token) external view returns (Limits memory);

    /// @notice Where every fee and rescued excess is paid.
    function feeRecipient() external view returns (address);
}
