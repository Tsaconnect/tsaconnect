# Push Notifications — Expo (#27)

**Date:** 2026-06-02 · **Branch:** `feat/p2p-trading` · **Status:** approved; building.

## Goal
Deliver the app's existing server-side notification events out-of-app via Expo push, plus an
app-side local reminder for the P2P payment-window expiry. **No new notification points** — push
piggybacks on the backend's existing event→notification pipeline.

## Architecture (the crux)
`NotificationService.handleEvent` (subscribed to the bus `"*"`) already builds `{title, message,
type, data}` for every event, persists a `Notification`, and WS-pushes it. Push delivery hooks the
**same** path: for push-worthy event types, also send an Expo push to the user's registered device
tokens (respecting `MuteNotifications`). So "where to notify" = the existing event catalog; this
spec only adds the **transport** + the **device-token registry** + **deep-link routing**.

## Scope — which events push (curated)
Push counterparty-actionable + urgent events; skip self-triggered noise.

| Event | Push | Tap → route |
|---|---|---|
| `p2p.trade.*` (paid/releasing/released/cancelled/disputed/resolved/expired) | ✅ | `/p2p/trade/:tradeId` |
| `p2p.message.new` | ✅ | `/p2p/trade/:tradeId` |
| `p2p.deposit.confirmed` / `p2p.withdraw.confirmed` | ✅ | `/p2p/pool` |
| order milestones (placed/escrowed/shipped/delivered/refund*/cancel*/disputed/dispute_resolved) | ✅ | order detail |
| `transaction.received` / `transaction.completed` | ✅ | wallet |
| security.* (new device / locked / password / failed) | ✅ | settings |
| `verification.approved`/`rejected`, `merchant.approved`/`rejected`/`suspended` | ✅ | profile / merchant |
| self-actions: `p2p.ad.*`, `product.*`, `transaction.pending`, `verification.submitted`, `merchant.submitted` | ❌ | — |

A single `pushWorthy(type) bool` predicate in the backend gates it (one place to tune).

## Backend (tsa-api-go)
- **`models.DeviceToken`**: id, user_id (index), token (unique), platform (`ios|android`), last_seen,
  created/updated, deleted_at. Add to AutoMigrate.
- **Routes** (authed, under `/api/notifications`): `POST /device-tokens` `{token, platform}` (upsert,
  bump last_seen); `DELETE /device-tokens/:token` (logout/unregister).
- **`ExpoPushService`**: batched POST to `https://exp.host/--/api/v2/push/send`
  (`{to, title, body, data, sound, channelId, priority}`); parse the ticket/receipt response and
  **prune tokens that return `DeviceNotRegistered`**. Optional `EXPO_ACCESS_TOKEN` from config
  (dormant if unset — sends still work unauthenticated). Network failures are logged, never block.
- **Wire into `NotificationService.handleEvent`**: after the WS push, if `pushWorthy(type)` and
  `!user.MuteNotifications`, look up the user's device tokens and fire pushes (goroutine; same
  `{title, message, data}`; `data` carries `type` + ids for deep-linking).
- **Tests**: register upsert + unregister; `pushWorthy` curation table; payload build; dead-token
  prune on `DeviceNotRegistered` (mock the HTTP client); mute respected.

## App (tsa-app)
- **Deps**: `expo-notifications` + `expo-device`. Config: Android notification channel, the
  notifications plugin + EAS `projectId` for `getExpoPushTokenAsync`. **Native deps → requires a
  dev/EAS build; everything no-ops safely in Expo Go (guarded by `Device.isDevice` + try/catch).**
- **`notificationsApi`** (services): `registerDevice(token, platform)` / `unregisterDevice(token)`.
- **`usePushRegistration`** (in the authed provider tree): on login → request permission → get Expo
  token → register with backend; on logout → unregister. Idempotent; silent if denied/unavailable.
- **Handlers**: foreground `setNotificationHandler`; a response listener that deep-links via
  expo-router using a **pure `routeForNotification(data)` mapper** (unit-tested).
- **Local expiry reminder**: when a trade is `pending_payment`, schedule a local notification ~5 min
  before `expiresAt`; cancel it on pay/cancel/leave. App-side only (works offline).

## Out of scope
Rich/image push, notification preferences UI beyond the existing `MuteNotifications`, gasless,
per-event opt-outs, badge-count management. Easy follow-ons.

## Rollout
1. Backend: DeviceToken + routes + ExpoPushService + wire + tests → verify (`go build`/`go test`).
2. App: deps + config + hook + handlers + mapper test + local reminder → verify (tsc/eslint/jest).
3. User builds a dev client + tests on device (push needs a real build).
