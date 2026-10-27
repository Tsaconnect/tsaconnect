# Core contracts: security model

Scope: `FeeConfig`, `EscrowBase`, `OrderEscrow`, `P2PEscrow` (this directory). The legacy
contracts one level up (`ProductEscrowV2`, `ServiceContactV2`, `P2PEscrowV3`, `FeeManager`,
`MCGPRewardDistributor`) are being retired and are out of scope.

Solidity 0.8.24, OpenZeppelin 5.6.1 (upgradeable), UUPS proxies, EVM `cancun`.
Deployed on Sonic (146) and BSC (56). Stablecoins: 6 decimals on Sonic, 18 on BSC.

## What the contracts do

- **FeeConfig** (one per chain) holds fee rates per `(feature, token address)`, value limits per
  token, and the single fee recipient. It holds no funds. An unset or inactive rate reverts, so
  the rate table is also the token allowlist.
- **OrderEscrow** holds a buyer's payment for a product or paid service until delivery, then pays
  the seller minus the merchant fee.
- **P2PEscrow** holds sellers' pooled liquidity and locks it per trade against EIP-712 signed ad
  vouchers, releasing to the buyer minus the fee.

Money only ever moves to a party of the order or trade (buyer, seller, merchant) or to
`FeeConfig.feeRecipient()`. Cashback, TradePoints and referral shares are off-chain accounting and
never touch these contracts.

## Roles and powers

| Role | Contract | Power | Bound |
|---|---|---|---|
| `DEFAULT_ADMIN_ROLE` | all | grant/revoke roles; unpause | two-step transfer (`AccessControlDefaultAdminRules`), 0 delay |
| `UPGRADER_ROLE` | all | UUPS upgrade | **unbounded**: an upgrade can change any rule (accepted centralisation, see below) |
| `CONFIG_ROLE` | FeeConfig | set rates, limits, fee recipient | rate ≤ 1000 bps in FeeConfig; consumers apply tighter caps (orders ≤ 300, P2P ≤ 300 + 300, total ≤ 500); fees are snapshotted at creation so a change never touches open positions |
| `PAUSER_ROLE` | escrows | pause new exposure | pause blocks only create / deposit / open; every exit stays open; cannot unpause |
| `RESCUER_ROLE` | escrows | `rescue(token)` | only `balanceOf(this) − owed[token]`, always paid to the fee recipient |
| `RESOLVER_ROLE` | escrows | settle disputes | only the disputed order's or trade's own funds; OrderEscrow split bounded by the order total |
| seller's releaser | P2PEscrow | release that seller's trades | opt-in per seller (`setReleaser`); no global release power exists |

At deployment every role is granted in the initializers; the deployer holds none
(`deploy/core.js` asserts this). The admin is the platform owner's wallet (business decision D2,
2026-10-27): no multisig and no timelock for now.

## Invariants

Checked after every step by the stateful fuzz suites (`test/core/*.invariants.test.js`), with a
token that blacklists addresses like USDC/USDT:

OrderEscrow
- `token.balanceOf(escrow) ≥ owed[token]`.
- `owed[token] = Σ (product + shipping)` of non-terminal orders `+ Σ claimable`.
- Conservation: no token is created or lost; nobody outside buyer / seller / fee recipient gains.
- Terminal states (Released, Refunded, Resolved) are absorbing.
- The merchant fee is ≤ 3% of the product price and never changes after creation.
- Liveness: every open order reaches a terminal state without an admin, even while paused and with
  every payee blacklisted.

P2PEscrow
- `owed[token] = Σ available + Σ locked + Σ claimable ≤ token.balanceOf(escrow)`.
- `locked[seller] = Σ` amounts of that seller's open trades.
- `voucherFilled[h] ≤ maxAmount` and equals the amount of its trades not returned to the pool.
- Conservation, and strangers never gain; an admin can never release a trade.
- A seller can always withdraw their whole available balance, paused or not.
- Liveness: every open trade can be ended without an admin (timeout, or a party's dispute + backstop).

## Design rules

- ERC-7201 namespaced storage in every custom contract (`tsa.storage.<Name>`); no `__gap`.
- Implementations disable initializers in the constructor; proxies are created with their init
  calldata; there are no reinitializers in version 1. A future one must be role-gated and run only
  inside `upgradeToAndCall`.
- The ERC-2771 forwarder and the FeeConfig address are immutables of the implementation. There is
  no setter: a settable forwarder lets an admin impersonate any user.
- No Permit2 paths.
- `ReentrancyGuardTransient` on every function that moves tokens, plus checks-effects-interactions.
- Pulls check the balance delta and reject fee-on-transfer tokens.
- A payout never reverts an exit: if the token transfer fails (e.g. a blacklisted recipient), the
  amount becomes `claimable` for that recipient and stays in `owed`; they `claim` it to any address.
- Orders and trades are stored under `keccak256(opener, id)`, so nobody can take another user's id.

## Timeouts

| | OrderEscrow | P2PEscrow |
|---|---|---|
| Delivery / payment | `handlingDays` (1–30) per order; after it anyone can refund the buyer | 30 minutes to mark paid; after it anyone can cancel back to the seller |
| Confirmation | 7 days after delivery; after it anyone can release to the seller | seller's own auto-release window (0–7 days, opt-in) |
| Dispute backstop | 14 days; unresolved → buyer refunded | 14 days; unresolved → crypto back to the seller |

## Trust assumptions and known limitations

- **Single admin.** One wallet can upgrade every contract and change fees and the fee recipient.
  This is an accepted centralisation for the development phase; the plan is to freeze upgrades or
  move `UPGRADER_ROLE` behind a timelock after the audit (decision D1).
- **Resolvers decide disputes.** A resolver can choose any split of a disputed order's funds. The
  backstops exist for when no resolver acts.
- **Dispute default.** An unresolved order dispute refunds the buyer after 14 days, so resolver
  downtime favours buyers. Operations must monitor `OrderDisputed`.
- **Seller self-reporting.** "Delivered" is the seller's own claim; the buyer's protection is the
  7-day dispute window.
- **Voucher griefing (P2P).** Anyone can open trades against a public voucher and lock that
  liquidity for one payment window at a time.
- **Shared limits.** `maxOwed` is checked against each escrow's own `owed`, so it caps exposure per
  contract, not per chain.
- **Token assumptions.** Only plain ERC-20s without transfer fees or negative rebasing may be given
  a rate. Blacklisting is handled by deferred payouts; a token that blacklists the escrow itself
  would freeze everything in that token.
