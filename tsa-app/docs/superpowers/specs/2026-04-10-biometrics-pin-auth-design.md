# Biometrics / PIN Authentication — Standardization Design

**Date:** 2026-04-10
**Status:** Approved

## Problem

The app has partial biometrics/PIN support — app lock and settings work, but two critical flows are broken:

1. **Login always requires password** — Biometric login checks for an access token in AsyncStorage, but logout deletes that token. The biometric button never appears after logout.
2. **Sensitive operations use OTP only** — Fiat send uses a hardcoded OTP modal. Crypto transfers (instant-pay) have no authorization at all. Biometrics/PIN is not offered for transaction authorization.

## Solution

Introduce refresh tokens (backend + frontend), store them in SecureStore gated by biometrics, and create a reusable AuthorizationModal for sensitive operations.

---

## Section 1: Backend — Refresh Token Infrastructure

**Location:** `tsa-dev/tsa-api-go/`

### 1.1 Database

Add `refresh_tokens` table:

| Column | Type | Description |
|--------|------|-------------|
| id | UUID | Primary key |
| user_id | UUID | FK → users |
| token_hash | VARCHAR(64) | SHA-256 hash of the refresh token |
| expires_at | TIMESTAMP | 30 days from creation |
| revoked | BOOLEAN | Default false |
| created_at | TIMESTAMP | Creation time |
| device_info | VARCHAR(255) | Optional — device identifier for multi-device management |

Index on `token_hash` for fast lookup. Index on `user_id` for revocation queries.

### 1.2 Token Lifetimes

- **Access token:** 1 hour (down from 7 days)
- **Refresh token:** 30 days

### 1.3 New Endpoints

**`POST /api/auth/refresh`** (unauthenticated)
- Request: `{ "refreshToken": "string" }`
- Validates refresh token against DB (hash lookup, not expired, not revoked)
- Returns new access token + rotated refresh token (old one revoked)
- Response: `{ "success": true, "data": { "accessToken": "...", "refreshToken": "..." } }`
- Errors: 401 if token invalid/expired/revoked

**`POST /api/auth/revoke`** (authenticated)
- Request: `{ "refreshToken": "string" }` (optional — if omitted, revokes all for user)
- Revokes the specified refresh token (or all tokens for the user)
- Response: `{ "success": true, "message": "Token revoked" }`
- Used by "Sign Out of This Device" in settings

### 1.4 Modified Endpoints

**`POST /api/auth/login`** — response changes:
```json
{
  "success": true,
  "data": {
    "userId": "...",
    "role": "user",
    "accessToken": "jwt-string",
    "refreshToken": "opaque-string",
    "name": "...",
    "email": "...",
    "verificationStatus": "...",
    "accountStatus": "...",
    "emailVerified": true
  }
}
```

Key change: `token` → `accessToken`, new `refreshToken` field.

**`POST /api/auth/signup`** — same response shape change.

### 1.5 Refresh Token Format

- Opaque random string (64 hex chars via `crypto/rand`)
- Stored in DB as SHA-256 hash (never store raw token server-side)
- Rotation on use: each refresh call invalidates the old token and issues a new one

---

## Section 2: Frontend — Token Storage & Biometric Login

**Key files:** `AuthContext.tsx`, `Login.tsx`, `localAuth.ts`, `api.ts`, `index.tsx`

### 2.1 Token Storage Strategy

| Token | Storage | Survives logout | Biometric-gated |
|-------|---------|-----------------|-----------------|
| Access token | In-memory (AuthContext) + AsyncStorage (hydration) | No | No |
| Refresh token | SecureStore (`tsa-refresh-token`) | Yes (soft logout) | Yes (on login) |

### 2.2 Login Flow (Login.tsx)

**Current (broken):**
1. Check `AsyncStorage.getItem("authToken")` → gone after logout → no biometric button

**New:**
1. On mount, call `hasRefreshToken()` (checks SecureStore)
2. If refresh token exists + `isBiometricEnabled()` + `isBiometricAvailable()` → show biometric button
3. If refresh token exists + `hasPin()` but no biometric → show PIN login option
4. Biometric/PIN success → `getRefreshToken()` from SecureStore → `POST /auth/refresh` → get new access token → hydrate AuthContext → navigate to `/home`
5. If refresh call fails (401) → clear stored refresh token → show "Session expired, please sign in with your password"

**Password login** continues to work as before — now also stores the refresh token in SecureStore.

### 2.3 Logout (AuthContext.tsx)

**Soft logout (`logOut()`):**
- Clears access token from memory + AsyncStorage
- Clears `isAuthenticated`, `currentUser` state
- Does NOT clear refresh token from SecureStore
- Does NOT call `/auth/revoke`
- Next login → biometric available

**Full logout (`logOutFull()`):**
- Calls `POST /auth/revoke` (revokes refresh token server-side)
- Clears refresh token from SecureStore
- Clears access token from memory + AsyncStorage
- Clears all auth state
- Next login → password required (no biometric option)

### 2.4 Auth Hydration (AuthContext.tsx — hydrateAuth)

**Current:** Checks AsyncStorage for access token → validates with getProfile() → clears everything if expired.

**New:**
1. Check AsyncStorage for access token
2. If found → validate with `getProfile()`
   - Success → hydrate as normal
   - 401 → try silent refresh: `getRefreshToken()` from SecureStore → `POST /auth/refresh` → store new access token → retry `getProfile()`
   - Refresh also fails → clear access token, set `isAuthenticated = false` (but keep refresh token for biometric login)
3. If no access token → set `isAuthenticated = false`, let index.tsx route to login

### 2.5 API Client — Auto-refresh Interceptor (api.ts)

Add an axios response interceptor:
- On 401 response → attempt silent refresh using stored refresh token
- If refresh succeeds → retry original request with new access token
- If refresh fails → trigger logout, route to login
- Prevents user-facing errors when access token expires mid-session

---

## Section 3: AuthorizationModal — Sensitive Operation Auth

**New file:** `components/common/AuthorizationModal.tsx`

### 3.1 Props

```typescript
interface AuthorizationModalProps {
  visible: boolean;
  title?: string;              // Default: "Authorize"
  description?: string;        // e.g. "Send ₦50,000 to John Doe"
  onAuthorized: () => void;    // Called on successful verification
  onCancel: () => void;        // Called on dismiss
}
```

### 3.2 UI Flow

1. Modal opens with title + description
2. Shows available authorization methods:
   - **Biometric button** (if biometric enabled + available): "Use Face ID" / "Use Fingerprint"
   - **PIN button** (if PIN is set): "Use PIN"
   - **OTP button** (always available): "Use Email OTP"
3. User taps their choice:

**Biometric path:**
- Native biometric prompt → success → `onAuthorized()`
- Failure → show error, user can retry or pick another method

**PIN path:**
- Show 4-digit PIN input inline in modal
- Verify against SecureStore (`verifyPin()`)
- Success → `onAuthorized()`
- Failure → show error, allow retry (max 3 attempts, then lock out to OTP)

**OTP path:**
- Call `POST /auth/send-otp` → show 6-digit OTP input
- User enters code → `POST /auth/verify-otp`
- Success → `onAuthorized()`
- Failure → show error, allow retry/resend

### 3.3 Integration Points

**sendFiat.tsx** (fiat withdrawal):
- Replace existing OTP modal with `AuthorizationModal`
- `onAuthorized` → proceed with `submitFiatSend()` call

**instant-pay.tsx** (crypto transfer):
- Add `AuthorizationModal` before wallet signing
- `onAuthorized` → proceed with `prepareSendTransaction()` + signing

**Future sensitive operations** use the same component.

---

## Section 4: localAuth.ts Additions

### 4.1 New Functions

```typescript
// Refresh token management (SecureStore)
storeRefreshToken(token: string): Promise<void>
getRefreshToken(): Promise<string | null>
clearRefreshToken(): Promise<void>
hasRefreshToken(): Promise<boolean>

// Authorization helper for AuthorizationModal
authorizeWithBiometricOrPin(): Promise<{ success: boolean; method: 'biometric' | 'pin' | 'none' }>
```

### 4.2 Storage Keys

New SecureStore key: `tsa-refresh-token`

Existing keys unchanged:
- `tsa-app-pin` (SecureStore)
- `tsa-biometric-enabled` (AsyncStorage)
- `tsa-pin-enabled` (AsyncStorage)
- `tsa-lock-enabled` (AsyncStorage)

---

## Section 5: Settings Changes

**File:** `app/(dashboard)/settings.tsx`

### 5.1 Logout Options

- **"Log Out"** — calls `logOut()` (soft). Biometric login available next time.
- **"Sign Out of This Device"** — calls `logOutFull()`. Requires password next time. Confirm dialog: "This will remove biometric login. You'll need your password to sign in again."

### 5.2 No Other Changes

Biometric toggle, PIN setup, Change PIN — all work correctly today. No modifications needed.

---

## File Change Summary

### Backend (tsa-api-go)

| Action | File | Change |
|--------|------|--------|
| Create | `internal/models/refresh_token.go` | RefreshToken model |
| Create | `migrations/XXX_add_refresh_tokens.sql` | New table |
| Create | `internal/handlers/refresh_handler.go` | Refresh + revoke handlers |
| Modify | `internal/handlers/auth_handler.go` | Return refresh token on login/signup, shorten access TTL |
| Modify | `internal/routes/routes.go` | Add `/auth/refresh` and `/auth/revoke` routes |

### Frontend (tsa-app)

| Action | File | Change |
|--------|------|--------|
| Modify | `services/localAuth.ts` | Add refresh token helpers + authorizeWithBiometricOrPin |
| Modify | `components/onboarding/Login.tsx` | Check SecureStore for refresh token, fix biometric button |
| Modify | `AuthContext/AuthContext.tsx` | Store refresh token on login, add logOutFull(), auto-refresh in hydration |
| Modify | `components/services/api.ts` | Update login/signup response handling, add 401 interceptor, add refresh/revoke calls |
| Create | `components/common/AuthorizationModal.tsx` | Reusable biometric/PIN/OTP authorization modal |
| Modify | `screens/sendFiat.tsx` | Replace OTP modal with AuthorizationModal |
| Modify | `app/wallet/instant-pay.tsx` | Add AuthorizationModal before wallet signing |
| Modify | `app/(dashboard)/settings.tsx` | Add "Sign Out of This Device" option |

---

## Migration Note

The login/signup response field rename (`token` → `accessToken` + new `refreshToken`) is a breaking change. The backend should support both `token` (legacy) and `accessToken` (new) in the response during migration. The frontend update should land first (reading both fields), then the backend can drop the legacy `token` field later.

## Out of Scope

- Multi-device refresh token management UI (view/revoke sessions)
- Per-user PIN keys (current single-user-per-device model is fine)
- Backend rate limiting on `/auth/refresh` (can add later)
- Biometric as a registration step (users set it up in settings after account creation)
