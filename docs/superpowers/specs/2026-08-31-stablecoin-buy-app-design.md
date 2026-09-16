# Stablecoin buy — mobile app (tsa-app)

**Date:** 2026-08-31
**Status:** Draft — pending review
**Repo:** `tsa-dev/tsa-app` (React Native 0.81 + Expo 54 + expo-router v6)
**Backend:** shipped on `feat/it-stable-buy` — see `2026-08-30-stablecoin-buy-design.md`

---

## 1. Objective

Let a user buy USDC/USDT with naira from inside Instant Transfer, and watch the purchase
through to delivery. The backend shipped 30 commits on `feat/it-stable-buy`; there is currently
no app code that calls it, so buy shows MCGP only.

### 1.1 What the backend already provides

| Endpoint | Purpose |
|---|---|
| `POST /api/it/stable/buy/quote` | indicative price — `assetUnits`, `grossUnits`, `ratePerUnit`, `fiatMinor`, `expiresAt`, `indicative: true` |
| `POST /api/it/stable/buy` | create the intent → `{intent, payInto}` |
| `GET /api/it/stable/buy/chains?asset=` | chains this user can actually receive on |
| `POST /api/it/stable/buy/:id/cancel` | abandon an unpaid purchase |
| `GET /api/it/intents/:id` | poll status |

### 1.2 What makes this simpler than sell

**The app never touches a key.** No signing, no LiFi, no chain interaction, no gas. The user
picks an amount and a chain, pays into a bank account, and waits. That is the whole flow.

`sell-stable.tsx` grew to 1,574 lines because it owns quoting, bridging, authorisation, signing
and broadcast. Buy owns none of that. The target for `buy-stable.tsx` is **under ~500 lines**,
with money math and status mapping extracted as pure functions from the first commit rather
than after the file is already large.

### 1.3 Scope

**In:** NGN → USDC/USDT, delivery to the user's own address on a chain they hold one for, with
a live progress view and the ability to cancel an unpaid purchase.

**Out:** any currency but NGN (the backend pins this); saved amounts; buying to an address the
user types (see §5).

---

## 2. Structure

`buy.tsx` gains an asset picker exactly as `sell.tsx` did. Choosing USDC/USDT pushes a new
route which owns its own flow; the MCGP path is otherwise untouched.

```
app/wallet/it/
  buy.tsx           (modified)  asset picker → routes USDC/USDT away; MCGP unchanged
  buy-stable.tsx    (new)       resume → form → confirm → pay-into
  intent/[id].tsx   (modified)  renders buy hops alongside sell's
utils/
  stableBuyQuote.ts (new)       pure money math + status→hops. Unit-tested.
services/
  itApi.ts          (modified)  the four buy endpoints
```

Reuse rather than rebuild: `CopyableField` and `SummaryRow` already render a pay-into account
in `buy.tsx`, and `ITPayTo` already types one. The `payInto` object the buy endpoint returns is
the same shape.

---

## 3. Flow

**Step 0 — resume before anything else.** On mount, look for an existing unpaid purchase. If
one exists, go straight to the pay-into view with a **Cancel this purchase** action.

This is not a nicety. The backend permits one unpaid stablecoin buy per user
(`ErrStableBuyAlreadyOpen`), and an abandoned one blocks buying until it expires 24 hours
later. The cancel endpoint exists precisely so the app can offer a way out; without the app
surfacing it, the user is wedged with no self-service exit.

**Step 1 — form.** Asset, naira amount, chain. Chains come from `GET /stable/buy/chains`, which
returns only chains the user holds an address on — so a chain they cannot receive on never
appears. Base is absent for USDT, because Flutterwave rejects that pair.

**Step 2 — confirm.** Three things, and the middle one is the point:

```
You'll receive
  ≈ 98.45 USDC on POLYGON

Sent to your wallet
  0x742d…9f2A
  ⚠ Delivery is irreversible. Check this is your address.

Pay ₦165,025
```

**Step 3 — create** → `POST /it/stable/buy` → show `payInto` with `CopyableField`.

**Step 4 — poll** `router.replace('/wallet/it/intent/<id>')`.

There is no "I have paid" API call and none is needed: the Flutterwave webhook drives
everything. That button is navigation only, and must not imply the app has been told anything.

---

## 4. The price is an estimate

The quote is indicative — the binding price is struck when the naira actually lands, which is
what removes the rate lock and every "you paid after your quote expired" state.

The screen shows **`≈ 98.45 USDC`** and nothing more. The `≈` carries the signal; an
explanatory paragraph on every purchase is noise for a difference most users will never
notice. The response's `indicative` flag is what the app keys off, so if the backend ever
returns a firm quote the prefix can drop without a UI rewrite.

---

## 5. The destination address is always shown

The confirm step displays the **full destination address and chain**, with an irreversibility
warning.

This is a safety control, not decoration. The two address sources differ:

- **EVM chains** resolve from `users.wallet_address` — server-held, and genuinely cannot be
  wrong.
- **Solana** resolves from `wallet_addresses`, which the user populated themselves via
  `POST /api/wallet/nonevm/register`. That endpoint validates the value for **shape only**, and
  there is no server-side derivation to verify it against.

So on the Solana path the address *is* user-supplied and the server cannot check it. A user
socially engineered into registering an attacker's address would have their purchased
stablecoin delivered there, and nothing in the backend can detect it. **This screen is the only
remaining place a person could notice**, which is why it shows the address on every chain
rather than only where it is unverifiable — an inconsistent warning teaches people to ignore it.

---

## 6. Progress screen

`intent/[id].tsx` renders buy hops when the intent's direction is buy and its asset is not MCGP:

| Backend status | Shown as |
|---|---|
| `pending_payment` | Waiting for your transfer · **Cancel** |
| `payment_received` | Payment received |
| `awaiting_funds` | Confirming your payment |
| `converting` | Buying USDC |
| `delivering` | Sending to your wallet |
| `settled` | Delivered ✓ |
| `buy_awaiting_rate` | Needs review — we've been notified |
| `cancelled` / `failed` | with the reason |

`buy_awaiting_rate` is a status string the app has never seen. Left unmapped it renders a blank
progress screen — and it is precisely the state where the user's naira is held pending an
operator, so a blank screen there is the worst possible moment for one. **A test enumerates
every backend buy status**, so an unmapped one fails CI rather than shipping.

Polling follows the existing pattern: fetch on focus, ~10s while focused and non-terminal, stop
on terminal. No websocket — the backend emits no events for this flow.

---

## 7. Error handling

Everything before payment is safe, because no money has moved. A failed quote, an unsupported
chain, a rejected create — each surfaces a message and returns to the form.

Three need real handling:

| Condition | Behaviour |
|---|---|
| User holds no address on any chain | Cannot buy. Direct them to wallet setup, not a raw 400. |
| `stable_buy_already_open` | Should not surface — step 0 resumes instead — but map it to the resume view if it does. |
| Not configured (503) | "Buying is temporarily unavailable", not a crash. |

The backend returns stable machine-readable codes (`stable_buy_below_minimum`,
`stable_buy_no_va`, `stable_buy_no_wallet`, `stable_unsupported_chain_asset`,
`stable_buy_unsupported_currency`). Branch on those, never on message text.

---

## 8. Money handling

- Fiat crosses the wire as **minor-unit integer strings** and stays a string until formatted.
- Asset amounts cross as **6-decimal base-unit strings**. `sell.tsx` hardcodes 18 decimals in
  two places; using that here would be wrong by 10¹².
- The app never re-derives the price. It displays what the backend quoted, and the progress
  screen displays what the backend settled.

---

## 9. Testing

The repo has no `@testing-library/react-native`; its convention is that money math lives in
pure modules so it can be tested at all.

- **`utils/stableBuyQuote.ts`** — naira/kobo formatting, base-unit → display conversion,
  minimum checks, display strings. Pure, unit-tested, no React.
- **`statusToHops(status)`** — a test enumerating every backend buy status, so an unmapped one
  fails rather than rendering blank.
- **Service tests** for the four endpoints, mirroring the existing sell-side tests.
- Screens are verified by reading; their logic lives in the pure modules above.

---

## 10. Known-open risks

| Risk | State |
|---|---|
| **Delivery has never been exercised** | `type:"crypto"` has never run on any chain, and no conversion has reached `SUCCESSFUL`. The first real buy is the canary for both. |
| **Solana address is unverifiable** | §5. The confirm screen is the only control; a socially-engineered registration cannot be detected server-side. |
| **Buy activates wherever Flutterwave is configured** | `HasStableBuy()` has no dedicated opt-out, so shipping this app against a configured backend makes buy live. |
| **`buy_awaiting_rate` needs operator action** | The app can only say "we've been notified". There is no user-facing recovery, by design. |
