# P2P Marketplace

Peer-to-peer fiat ↔ crypto marketplace inside TSA Connect. **Non-custodial**: trades are escrowed on-chain (Sonic) via the `P2PEscrow` contract; the platform never holds user funds. Backend is Go (`tsa-api-go`), app is React Native + Expo (`tsa-app`).

## How it works (today)

- **Merchants** deposit crypto into a per-seller escrow **pool** and publish **sell ads** (gasless — ad creation is an off-chain EIP-712 voucher signature). Ads support **fixed** or **float** pricing.
- **Buyers** browse ads in **their own fiat currency** (defaulting from geolocation), filter by **payment method**, open a trade, pay fiat off-chain, mark paid (with proof), and the seller releases the crypto on-chain.
- **Disputes** are escalated in-trade and resolved by an admin (on-chain, non-custodial — funds can only go to one of the two trade parties).
- **Reputation**: every merchant has derived stats (completion rate, avg release time, order count) + 👍/👎 ratings, Verified/Trusted badges, and online presence, surfaced on ad rows, ad detail, and a seller profile. See [merchant-reputation.md](./merchant-reputation.md).

## Capabilities

| Area | Status |
|---|---|
| Buy-side browse, multi-currency | ✅ shipped |
| Payment-method providers (bank_transfer, opay, palmpay, kuda, interac, paypal, revolut, wise, skrill) + "All Payment Methods" filter | ✅ shipped |
| Fixed + float-rate ads (float gated to currencies with a market anchor) | ✅ shipped |
| Merchant reputation & trust (stats, ratings, tiers, presence) | ✅ shipped |
| Live payment-window countdown + post-trade rating prompt | ✅ shipped |
| Sell-side (user sells crypto to a merchant) | ⛔ not yet — needs the two-sided escrow (Phase 1) |
| Gasless trade actions (relayer / Permit2) | ⛔ planned (Phase 1) |
| Express auto-match, merchant collateral, mobile push | ⛔ planned |

## Architecture & roadmap

See [architecture.md](./architecture.md) for the current design, the forward direction (non-custodial + gasless + two-sided escrow), the phased roadmap, and open decisions (incl. the deferred fee model).

## Key code

- **Contract:** `smart-contracts/contracts/P2PEscrow.sol`
- **Backend:** `tsa-api-go/internal/services/p2p_trade_service.go` (trade lifecycle), `…/p2p_service.go` (rates), `…/p2p_merchant_stats_service.go` (reputation), `…/handlers/p2p_handler.go`, routes under `/api/p2p/*`.
- **App:** `tsa-app/app/p2p/*` (screens), `tsa-app/services/p2p.ts` (API client), `tsa-app/components/p2p/*`.
