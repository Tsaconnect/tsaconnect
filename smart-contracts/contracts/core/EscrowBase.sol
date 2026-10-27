// SPDX-License-Identifier: MIT
pragma solidity 0.8.24;

import {Initializable} from "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import {UUPSUpgradeable} from "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import {PausableUpgradeable} from "@openzeppelin/contracts-upgradeable/utils/PausableUpgradeable.sol";
import {ContextUpgradeable} from "@openzeppelin/contracts-upgradeable/utils/ContextUpgradeable.sol";
import {ERC2771ContextUpgradeable} from "@openzeppelin/contracts-upgradeable/metatx/ERC2771ContextUpgradeable.sol";
import {AccessControlDefaultAdminRulesUpgradeable} from
    "@openzeppelin/contracts-upgradeable/access/extensions/AccessControlDefaultAdminRulesUpgradeable.sol";
import {ReentrancyGuardTransient} from "@openzeppelin/contracts/utils/ReentrancyGuardTransient.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {IFeeConfig} from "./interfaces/IFeeConfig.sol";

/// @title EscrowBase
/// @notice What every escrow shares: roles, upgrades, pause, the trusted forwarder, token pulls
///         and pushes, and `owed` accounting with a rescue bounded to the excess.
/// @dev    Rules this base enforces for every escrow built on it:
///         - The forwarder and FeeConfig are immutables of the implementation. There is no setter:
///           a settable forwarder would let an admin impersonate any user.
///         - `owed[token]` is the sum every open position can still pay out, plus payouts that could
///           not be delivered and wait in `claimable`. Tokens above it are excess (stray transfers,
///           rounding) and are the only tokens `rescue` can move.
///         - A payout never reverts an exit: if the transfer fails (e.g. the stablecoin blacklisted
///           the recipient), the amount is credited to `claimable` and the recipient can `claim` it
///           to any address later. So no recipient can freeze another party's settlement.
///         - Pause blocks new exposure only (callers use `whenNotPaused` on create/deposit/open).
///           Exits never check it, so a pause cannot trap funds.
abstract contract EscrowBase is
    Initializable,
    UUPSUpgradeable,
    AccessControlDefaultAdminRulesUpgradeable,
    PausableUpgradeable,
    ERC2771ContextUpgradeable,
    ReentrancyGuardTransient
{
    using SafeERC20 for IERC20;

    bytes32 public constant UPGRADER_ROLE = keccak256("UPGRADER_ROLE");
    bytes32 public constant PAUSER_ROLE = keccak256("PAUSER_ROLE");
    bytes32 public constant RESCUER_ROLE = keccak256("RESCUER_ROLE");

    /// @notice Source of fee rates, value limits and the fee recipient.
    /// @custom:oz-upgrades-unsafe-allow state-variable-immutable
    IFeeConfig public immutable feeConfig;

    /// @custom:storage-location erc7201:tsa.storage.EscrowBase
    struct EscrowBaseStorage {
        mapping(address token => uint256) owed;
        mapping(address token => mapping(address account => uint256)) claimable;
    }

    // keccak256(abi.encode(uint256(keccak256("tsa.storage.EscrowBase")) - 1)) & ~bytes32(uint256(0xff))
    bytes32 private constant BASE_STORAGE_SLOT = 0x1688fc8116c81ba80875796025d382dd29dbf2c0be00a4644edd67c5d918a500;

    event Rescued(address indexed token, address indexed to, uint256 amount);
    event PayoutDeferred(address indexed token, address indexed account, uint256 amount);
    event Claimed(address indexed token, address indexed account, address indexed to, uint256 amount);

    error ZeroAddress();
    error AmountMismatch(uint256 expected, uint256 received);
    error OwedLimitExceeded(address token, uint256 owed, uint256 limit);
    error NothingToRescue(address token);
    error NothingToClaim(address token);

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor(address forwarder, IFeeConfig feeConfig_) ERC2771ContextUpgradeable(forwarder) {
        if (address(feeConfig_) == address(0)) revert ZeroAddress();
        feeConfig = feeConfig_;
        _disableInitializers();
    }

    /// @dev Grants `admin` every base role and `pausers` the pause role, all at initialization so
    ///      the deployer never holds a role. Two-step admin transfer, no delay (owner decision D2).
    function __EscrowBase_init(address admin, address[] calldata pausers) internal onlyInitializing {
        if (admin == address(0)) revert ZeroAddress();
        __AccessControlDefaultAdminRules_init(0, admin);
        __Pausable_init();
        _grantRole(UPGRADER_ROLE, admin);
        _grantRole(PAUSER_ROLE, admin);
        _grantRole(RESCUER_ROLE, admin);
        _grantAll(PAUSER_ROLE, pausers);
    }

    function _grantAll(bytes32 role, address[] calldata accounts) internal {
        for (uint256 i; i < accounts.length; ++i) {
            if (accounts[i] == address(0)) revert ZeroAddress();
            _grantRole(role, accounts[i]);
        }
    }

    // ── Admin ──────────────────────────────────────────────────────────────────

    /// @notice Stop new exposure. Exits stay open.
    function pause() external onlyRole(PAUSER_ROLE) {
        _pause();
    }

    /// @notice Only the default admin can resume, so a leaked pauser key can't undo a pause.
    function unpause() external onlyRole(DEFAULT_ADMIN_ROLE) {
        _unpause();
    }

    /// @notice Send tokens held above what open positions are owed to the fee recipient.
    function rescue(address token) external onlyRole(RESCUER_ROLE) nonReentrant {
        uint256 balance = IERC20(token).balanceOf(address(this));
        uint256 owedNow = _baseStorage().owed[token];
        if (balance <= owedNow) revert NothingToRescue(token);
        uint256 excess = balance - owedNow;
        address to = feeConfig.feeRecipient();
        IERC20(token).safeTransfer(to, excess);
        emit Rescued(token, to, excess);
    }

    /// @notice Total open obligations in `token`, including undelivered payouts.
    function owed(address token) public view returns (uint256) {
        return _baseStorage().owed[token];
    }

    /// @notice A payout that could not be delivered to `account`, waiting to be claimed.
    function claimable(address token, address account) external view returns (uint256) {
        return _baseStorage().claimable[token][account];
    }

    /// @notice Collect your undelivered payouts in `token`, sent to `to` (any address you choose,
    ///         e.g. a new wallet if the old one was blacklisted). Never paused.
    function claim(address token, address to) external nonReentrant {
        if (to == address(0)) revert ZeroAddress();
        address account = _msgSender();
        EscrowBaseStorage storage s = _baseStorage();
        uint256 amount = s.claimable[token][account];
        if (amount == 0) revert NothingToClaim(token);
        s.claimable[token][account] = 0;
        s.owed[token] -= amount;
        IERC20(token).safeTransfer(to, amount);
        emit Claimed(token, account, to, amount);
    }

    // ── Token movement ───────────────────────────────────────────────────────────

    /// @dev Pull exactly `amount`; a fee-on-transfer or rebasing token fails the delta check.
    function _pull(address token, address from, uint256 amount) internal {
        uint256 before = IERC20(token).balanceOf(address(this));
        IERC20(token).safeTransferFrom(from, address(this), amount);
        uint256 received = IERC20(token).balanceOf(address(this)) - before;
        if (received != amount) revert AmountMismatch(amount, received);
    }

    /// @dev Pay out an amount already removed from `owed`. If the transfer fails, the amount goes
    ///      back into `owed` as a claimable balance instead of reverting the caller's exit.
    function _push(address token, address to, uint256 amount) internal {
        if (amount == 0) return;
        if (!IERC20(token).trySafeTransfer(to, amount)) {
            EscrowBaseStorage storage s = _baseStorage();
            s.claimable[token][to] += amount;
            s.owed[token] += amount;
            emit PayoutDeferred(token, to, amount);
        }
    }

    /// @dev Add an obligation, enforcing the FeeConfig owed limit for `token`.
    function _addOwed(address token, uint256 amount, uint256 maxOwed) internal {
        EscrowBaseStorage storage s = _baseStorage();
        uint256 next = s.owed[token] + amount;
        if (next > maxOwed) revert OwedLimitExceeded(token, next, maxOwed);
        s.owed[token] = next;
    }

    function _subOwed(address token, uint256 amount) internal {
        _baseStorage().owed[token] -= amount;
    }

    // ── Plumbing ─────────────────────────────────────────────────────────────────

    function _authorizeUpgrade(address) internal override onlyRole(UPGRADER_ROLE) {}

    function _baseStorage() private pure returns (EscrowBaseStorage storage s) {
        assembly {
            s.slot := BASE_STORAGE_SLOT
        }
    }

    function _msgSender() internal view override(ContextUpgradeable, ERC2771ContextUpgradeable) returns (address) {
        return ERC2771ContextUpgradeable._msgSender();
    }

    function _msgData() internal view override(ContextUpgradeable, ERC2771ContextUpgradeable) returns (bytes calldata) {
        return ERC2771ContextUpgradeable._msgData();
    }

    function _contextSuffixLength()
        internal
        view
        override(ContextUpgradeable, ERC2771ContextUpgradeable)
        returns (uint256)
    {
        return ERC2771ContextUpgradeable._contextSuffixLength();
    }
}
