# P2P Self-Serve Dispute Wizard + Admin Console (#25)

**Date:** 2026-05-30 · **Branch:** `feat/p2p-trading` · builds on the TSA Signature design system + the existing dispute API (raise/confirm + adminResolve/confirm, already E2E-verified).

## Goal
Close the dispute MVP gap: a guided **self-serve dispute wizard** for users (reason →
evidence → review → submit) and an **in-app admin dispute console** (admins currently
resolve only via raw API). No contract change — uses the existing on-chain
raiseDispute/adminResolve.

## Current state (do not rebuild)
- Contract: `raiseDispute`, `adminResolve(releaseToBuyer)` — done, on-chain.
- Backend: `PrepareRaiseDispute`/`ConfirmRaiseDispute`, `PrepareAdminResolveDispute`/
  `ConfirmAdminResolveDispute`, `GET /api/admin/p2p/disputes[/:id]`, `GET /api/p2p/trades/:id/dispute`.
  `P2PDispute{reason, raised_by, raised_role, status, resolved_by, resolution, refunded_to_buyer, resolve_tx_hash}`.
- In-trade chat (`p2p_messages`) already exists — **this IS the negotiation window**; the wizard
  does not add a separate negotiation system.
- App: `app/p2p/trade/[tradeId].tsx` has a basic reason-text dispute modal; `services/p2p.ts`
  `tradesApi.raiseDispute/confirmRaiseDispute`, `adminApi.*`.

## Gap to fill
1. **Evidence storage (backend):** `P2PDispute` has no evidence field. Add `EvidenceURLs`
   (Cloudinary image URLs) captured at raise time + surfaced to the admin.
2. **User wizard (app):** replace the basic modal with a guided multi-step flow.
3. **Admin console (app):** list open disputes + a resolve screen (view trade + evidence + chat,
   then refund-buyer / refund-seller + note → prepare → broadcast → confirm). Admin-only.
4. **Primitives:** build the two deferred design-system pieces the wizard needs — `Wizard`
   (step header + slide + docked footer) and `PhotoPickerTile` (add/preview/remove + Cloudinary
   upload via the existing `expo-image-picker` dep).

## Design

### Backend (verifiable; ship first)
- `P2PDispute.EvidenceURLs string` `gorm:"type:text"` — JSON-encoded `[]string` of Cloudinary
  URLs (mirrors how proof images are already stored as URLs). Nullable/empty default.
  Add to the SQLite test DDL too (raw-DDL rig).
- `PrepareRaiseDispute(... , evidenceURLs []string)`: validate each is a URL (≤ a small cap, e.g.
  6 images), JSON-marshal onto the dispute row at creation. Handler accepts `evidenceUrls []string`
  in the dispute request body (optional). Keep the OpenTxHash guard + the prepare/confirm split.
- Expose `evidenceUrls` (decoded) in the dispute JSON returned by `GET /p2p/trades/:id/dispute`
  and the admin endpoints.
- No new on-chain anything. Reason picker values are a fixed enum surfaced by the app; backend
  stores the free-text/enum `reason` as today.
- Tests: dispute-with-evidence round-trips through PrepareRaiseDispute → stored → returned;
  evidence cap + URL validation rejected with ErrInvalidParams. Full-stack E2E unchanged (evidence optional).

### Primitives (`components/ui/`)
- **PhotoPickerTile**: `value: string[]` (URLs) + `onChange`; tiles show add (+) / previews /
  remove (×) + per-image upload spinner. Uses `expo-image-picker` (installed) + the app's existing
  Cloudinary upload helper. Caps at N.
- **Wizard**: `steps: {key,title}[]`, `current`, controlled; renders a step header (N of M +
  titles), the active step's children, and a `BottomActionBar` footer (Back / Next|Submit with
  per-step `canAdvance`). Reanimated slide between steps; reduced-motion aware.

### User wizard (`app/p2p/trade/[tradeId]` dispute entry → new flow)
Steps: **1 Reason** (`RadioGroup`: payment-not-received / wrong-amount / no-crypto-released /
other+text) → **2 Evidence** (`PhotoPickerTile`, optional, "add screenshots of your payment") →
**3 Review** (`SummaryCard` of reason + evidence count + a "funds stay frozen, admin reviews"
notice) → Submit: `raiseDispute(reason, evidenceUrls)` → sign+broadcast → `confirm`. On success,
route back to the trade (now `disputed`); the chat is the negotiation channel.

### Admin console (`app/admin/p2p-disputes`)
- **List** (`GET /admin/p2p/disputes`): AccentCard rows — trade amount/fiat, parties, raised role,
  reason, status pill (`kind="dispute"`), age. Filter open/resolved. Skeleton/empty/error states.
- **Detail/resolve**: trade summary (`SummaryCard`), evidence gallery, link into the trade chat,
  then a resolve action — `RadioGroup` (Release to buyer / Refund to seller) + note `Input` →
  `BottomActionBar` Resolve → `adminResolve` prepare → admin signs+broadcasts → confirm. Gated on
  admin/super_admin (the screen + the API both enforce).
- Register the route in the admin stack; reachable from the admin dashboard.

## Verification
- Backend: `go build`, services suite + new evidence tests, gofmt/lint.
- App: `tsc --noEmit` + eslint on touched files; gallery covers the new primitives. **Visual
  sign-off by the user** (both screens) — open PR like #13; the admin console especially.
- Full-stack E2E (`smoke-e2e-api.js`) extended so the dispute is raised WITH evidence and the
  admin resolve path still passes.

## Rollout
1. Backend evidence storage (+ tests) → PR → merge (fully verifiable).
2. Primitives (Wizard, PhotoPickerTile) + gallery → part of the app PR.
3. User wizard + admin console screens → app PR, left open for visual sign-off.

## Out of scope (MVP)
Formal SLA timers/auto-escalation; a separate negotiation-window system (chat serves);
dispute fees (reserved/no-op in the contract); collateral; push.
