# Seller bank payouts — runbook

A marketplace seller who sets a naira bank account (Merchant → Profile → Payout account) is paid
in naira when a buyer confirms an order. The **treasury** is the order's on-chain seller, so the
escrow releases to it; the payout machine then bridges Sonic → Base (LiFi), sells the USDC on
Paycrest for the quoted naira (refund address = treasury), deposits it, and marks the payout paid.
Code: `services/seller_payout_orchestrator.go` (machine), `seller_payout_plan.go` (checkout
pricing), `seller_escrow_calls.go` (treasury-signed markDelivered/cancelOrder),
`seller_payout_admin.go` (operator actions). Console: **Seller payouts** (super admin).

## What must be true before it does anything

All of these, else new orders are made the ordinary way (seller wallet) and nothing changes:

- `RAMP_TREASURY_PRIVATE_KEY` set (distinct from every other signer; raw keys in production need
  `ALLOW_RAW_SIGNER_KEYS=true`). `RAMP_TREASURY_CHAINS` includes `sonic` and `base` (default does).
  **Does not need `RAMP_CONVERSION_BUY`.**
- Paycrest enabled (the TSA Pay payout keys) and `TSA_PAY_FEE_PERCENT` set — the planner reads the
  sender fee from it; blank means no payout is ever offered.
- `PRODUCT_ESCROW_ADDRESS` set (it is auto-allowlisted as the treasury's one call target on Sonic).
- `INBOUND_ALERT_EMAILS` set, so a parked payout emails an operator (otherwise: log line only).
- Boot log shows `[seller-payout] enabled treasury=0x… bridge_bps=40`. `[seller-payout] dormant`
  names what is missing.

Tuning: `SELLER_PAYOUT_BRIDGE_BPS` (default 40) is the bridge cost the buyer's payout fee budgets;
`RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER` / `_PER_CHAIN` cap exposure (payouts count toward the
per-chain cap); `RAMP_CONVERSION_RETRY_MINUTES` / `RAMP_CONVERSION_LEG_CEILING_HOURS` bound bridge
retries and a pending leg.

## Deploy order

1. Fund the treasury: **gas** on Sonic (S) and Base (ETH), and USDC on Sonic only if you want float;
   payouts are funded by the buyers' own releases. Floors are alert-only.
2. Deploy the API (adds tables `seller_payouts`, `seller_payout_settings`, `seller_payout_changes`,
   columns on `orders`, `paycrest_order_records`, and the `uq_ramp_legs_one_live_deposit` index).
3. Ship the **app update (OTA) at once**: an old app cannot sign delivery for a payout order (the
   server signs it; the old app gets a refusal), and it has no payout-account screen.
4. Deploy the console (Netlify on push to `tsa-admin` main).
5. Do the live test below **before** telling sellers.

Rollback: the **switch** (console → Seller payouts → Turn off, or
`PUT /api/admin/seller-payouts/switch {"enabled":false,"note":"…"}`) stops new payout orders and every
send within a few seconds. Orders already made stay payout orders (the treasury is their on-chain
seller); they wait, and proofs keep running. Turn it back on to resume. The hard stop is unsetting
`RAMP_TREASURY_PRIVATE_KEY` and restarting (then a payout order cannot be delivered or cancelled until
it is back).

## Live test (a few dollars)

1. A test seller (KYC verified) sets a bank account **in their own name** (Payout account screen;
   the code is emailed).
2. A buyer pays a ~$5 order in USDC/USDT. Check the quote shows a **Payout fee** line.
3. Seller ships, then marks delivered — the server marks it on-chain (no wallet prompt for the seller).
4. Buyer confirms. Watch the payout in the console: received → bridging → bridged → ordered →
   depositing → deposited → paid. Expect a few minutes; check the naira reached the bank, and compare
   it with the quoted amount on the seller's order screen.
5. Also try: a cancel (request-cancel, then approve) on a second payout order; and the switch off/on.
6. Read the logs: `[seller-payout]` lines; any `ALERT seller_payout_*` is a problem to understand.

## Watching it

- Log alerts: `ALERT seller_payout_review_total` (parked), `seller_payout_deposit_uncertain_total`,
  `seller_payout_wallet_uncertain_total`, `ramp_treasury_*` and `key_lane_*` (treasury lanes), plus the
  worker alert `worker_failing_total … worker=seller-payout`.
- Emails to `INBOUND_ALERT_EMAILS` when a payout parks.
- Console → Seller payouts: the queue; "Needs you" lists parked payouts.

## A parked payout (`needs_review`)

The machine parks on anything it cannot prove or that is over a limit; it **never pays the seller's
wallet on its own and never retries something that may already have moved money.** Open it in the
console: *stopped in* (the status it was in), the reason, the legs, the Paycrest order.

| Stopped in | Typical reason | Do |
|---|---|---|
| awaiting_release / received | release proof refused, no lane, cap or kill switch before the bridge | Fix the cause. **Retry** (offered as safe). If it cannot be fixed (e.g. Paycrest has no venue for the size): **Pay seller's wallet** (sends the proven USDC from the treasury to the seller's wallet). **Cancel** only closes a payout that never held anything; once the treasury holds the funds it is refused. |
| awaiting_release | "the escrow resolved this order on-chain but the app never recorded it" | The buyer's confirm landed on-chain but never reached the server, so the treasury was paid with no payout watching. Find the release transaction on the explorer (the escrow's `OrderReleased` for the order id), set the order to `completed` with that `release_tx_hash` (SQL), then **Retry**. If the escrow refunded the buyer instead, mark the order `refunded` and **Cancel** the payout. |
| bridging | LiFi failed/partial, a broadcast bridge never confirmed, over the ceiling | Read the Sonic and Base txs. If the funds landed on Base, **Retry (I checked)** moves it on; if not, it needs a manual rescue. Never pay the wallet from the console here (refused). |
| bridged / selling | rate moved beyond the 0.5% the platform absorbs (parked as **safe**: nothing was opened), no venue in the window, "a Paycrest order may exist" | A shortfall: **Retry** once the rate recovers (it runs the sale again). "May exist": check Paycrest for an order with this payout. None: **Retry (I checked)** opens the sale again; one exists: retry adopts it, or let it complete and **Mark paid** once the seller is paid. |
| ordered / depositing | deposit unconfirmed, deposit did not reach Paycrest | Read the deposit tx on Base. If it never left: **Retry (I checked)**. If it left: wait for Paycrest and **Mark paid** when settled. (A sale that is about to lapse BEFORE any deposit is withdrawn and reopened by the machine itself.) |
| deposited | Paycrest refunded / expired / unmatched / failed | Money may be back in the treasury on Base. Recover it by hand, pay the seller by hand, then **Mark paid** with the reference. |

Every action needs a note and is audited (`p2p_audit_logs`, actions `seller_payout.*`).
**Mark paid** needs a reference (bank transfer or Paycrest order id) and is refused while a Paycrest sale or a transfer of the payout is still alive (the seller could be paid twice); tick the confirmation only when you know Paycrest will not also pay. After **Cancel**, what the treasury
holds for that order is yours to return by hand. Do not send from the treasury address by hand: it
collides with the server's nonces (see the signer lane runbook in `CLAUDE.md`).

## Known limits

- Naira only, bank accounts only (mobile money and other currencies need a name check first).
- The seller's account must be in their KYC legal name or an active TSA Pay merchant's business name.
- MCGP orders and contact-service fees are wallet-only.
- Buy-side exposure and payout exposure share the per-chain cap; the payout machine does not count
  converted-buy exposure when it checks (buys are off in production).
- Paused (switch off) time and time spent parked are not counted against a payout's retry windows; a refused sale waits two minutes between attempts.
- A payout order made before an escrow redeploy or a treasury key change cannot be delivered or cancelled by the
  server (the treasury is no longer the order's on-chain seller or the escrow is not allowlisted): only a buyer
  refund request ends it. Do not rotate the treasury key or redeploy the escrow while payout orders are open.
- A payout order's seller-only actions (markDelivered, cancelOrder) are signed by the treasury; if the
  treasury is killed or cannot pay gas they answer 503 until it is back.
