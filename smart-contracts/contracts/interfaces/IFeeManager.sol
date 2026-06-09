// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IFeeManager
/// @notice Minimal consumer interface the escrows import to LIVE-READ fee rates from the
///         per-chain FeeManager (the single on-chain source of truth for every fee rate).
/// @dev See docs/superpowers/specs/2026-06-08-configurable-feemanager-design.md §3.1 (A1 live-read).
///
///      The `token` argument is the token *symbol* string (e.g. "USDT"), not the address —
///      matching the FeeManager key and the off-chain `fee_config` key so on-chain and
///      off-chain resolve to identical rates. Escrows resolve the symbol from the order's
///      stored token (`tokenSymbol[order.token]`); the caller cannot forge it.
interface IFeeManager {
    /// @notice The configured rate for a `(feature, token)` pair.
    /// @param feature The fee feature key (e.g. "p2p_platform", "withdrawal").
    /// @param token   The token SYMBOL (e.g. "USDT", or "*" for the wildcard/global row).
    /// @return bps        Basis points (0 allowed). Always <= MAX_BPS by construction of the setters.
    /// @return flatUSD    Flat component in 1e6 units (e.g. 100000 == $0.10).
    /// @return mcgpExempt When true and the token is MCGP, this feature charges 0.
    /// @return active     False = feature disabled / not configured (an UNSET key returns all-false).
    function rateFor(string calldata feature, string calldata token)
        external
        view
        returns (uint16 bps, uint64 flatUSD, bool mcgpExempt, bool active);
}
