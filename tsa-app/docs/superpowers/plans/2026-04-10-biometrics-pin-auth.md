# Biometrics / PIN Authentication Standardization — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix biometric login (refresh token in SecureStore survives logout), add reusable AuthorizationModal (biometric/PIN + OTP) for sensitive operations, shorten access token lifetime.

**Architecture:** Backend gets a `refresh_tokens` table + two new endpoints (`/auth/refresh`, `/auth/revoke`). Access tokens shrink to 1 hour; refresh tokens last 30 days. Frontend stores refresh token in SecureStore, uses it for biometric login. A new `AuthorizationModal` component lets users authorize sensitive ops with biometric/PIN or OTP.

**Tech Stack:** Go/Gin/GORM/PostgreSQL (backend), React Native/Expo/TypeScript (frontend), expo-local-authentication, expo-secure-store.

**Spec:** `docs/superpowers/specs/2026-04-10-biometrics-pin-auth-design.md`

---

## File Map

### Backend (`tsa-dev/tsa-api-go/`)

| Action | File | Responsibility |
|--------|------|----------------|
| Create | `internal/models/refresh_token.go` | RefreshToken GORM model |
| Modify | `internal/config/database.go` | Add RefreshToken to AutoMigrate |
| Modify | `internal/handlers/auth_handler.go` | Generate refresh token on login/signup, shorten access TTL |
| Create | `internal/handlers/refresh_handler.go` | Refresh + Revoke handlers |
| Modify | `internal/routes/routes.go` | Register new endpoints |

### Frontend (`tsa-dev/tsa-app/`)

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `services/localAuth.ts` | Refresh token SecureStore helpers |
| Modify | `components/services/api.ts` | Parse new response shape, add refresh/revoke calls, 401 interceptor |
| Modify | `components/onboarding/Login.tsx` | Fix biometric button to check SecureStore refresh token |
| Modify | `AuthContext/AuthContext.tsx` | Store refresh token on login, add logOutFull(), silent refresh in hydration |
| Create | `components/common/AuthorizationModal.tsx` | Reusable biometric/PIN/OTP authorization modal |
| Modify | `screens/sendFiat.tsx` | Replace OTP modal with AuthorizationModal |
| Modify | `app/wallet/instant-pay.tsx` | Add AuthorizationModal before wallet signing |
| Modify | `app/(dashboard)/settings.tsx` | Add "Sign Out of This Device" option |

---

## Task 1: RefreshToken Model (Backend)

**Files:**
- Create: `tsa-dev/tsa-api-go/internal/models/refresh_token.go`
- Modify: `tsa-dev/tsa-api-go/internal/config/database.go`

- [ ] **Step 1: Create the RefreshToken model**

Create `internal/models/refresh_token.go`:

```go
package models

import (
	"time"

	"github.com/google/uuid"
)

type RefreshToken struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID     uuid.UUID `gorm:"type:uuid;not null;index"`
	TokenHash  string    `gorm:"type:varchar(64);uniqueIndex;not null"`
	ExpiresAt  time.Time `gorm:"not null"`
	Revoked    bool      `gorm:"default:false"`
	DeviceInfo string    `gorm:"type:varchar(255)"`
	CreatedAt  time.Time
}
```

- [ ] **Step 2: Add RefreshToken to AutoMigrate**

In `internal/config/database.go`, add `&models.RefreshToken{}` to the `AutoMigrate` call:

```go
func AutoMigrate() error {
	if err := DB.AutoMigrate(
		&models.User{},
		&models.Asset{},
		&models.Product{},
		&models.Cart{},
		&models.Category{},
		&models.Portfolio{},
		&models.Transaction{},
		&models.Wallet{},
		&models.WalletTransaction{},
		&models.SupportedToken{},
		&models.VerificationLog{},
		&models.Order{},
		&models.Deposit{},
		&models.ServiceContactPayment{},
		&models.MerchantRequest{},
		&models.Notification{},
		&models.EmailVerification{},
		&models.PasswordReset{},
		&models.TPEarning{},
		&models.RefreshToken{},
	); err != nil {
		return err
	}
```

- [ ] **Step 3: Verify migration runs**

```bash
cd tsa-dev/tsa-api-go && go build ./cmd/server/
```

Expected: builds without errors. On next server start, GORM auto-creates the `refresh_tokens` table.

- [ ] **Step 4: Commit**

```bash
git add internal/models/refresh_token.go internal/config/database.go
git commit -m "feat: add RefreshToken model and auto-migration"
```

---

## Task 2: Refresh & Revoke Handlers (Backend)

**Files:**
- Create: `tsa-dev/tsa-api-go/internal/handlers/refresh_handler.go`
- Modify: `tsa-dev/tsa-api-go/internal/handlers/auth_handler.go`

- [ ] **Step 1: Add helper functions to auth_handler.go**

Add these functions at the bottom of `internal/handlers/auth_handler.go`, below the existing `generateToken` function:

```go
import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

func generateRefreshToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(h[:])
	return raw, hash, nil
}

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
```

- [ ] **Step 2: Shorten access token TTL**

In `generateToken` in `auth_handler.go`, change the expiry from 7 days to 1 hour:

```go
func generateToken(userID uuid.UUID, email, secret string) (string, error) {
	claims := jwt.MapClaims{
		"userId": userID.String(),
		"email":  email,
		"exp":    time.Now().Add(1 * time.Hour).Unix(),
		"iat":    time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}
```

- [ ] **Step 3: Modify Login handler to return refresh token**

In the `Login` handler in `auth_handler.go`, after `generateToken` succeeds, add refresh token creation. Find the success response block (around line 340-365) and replace it:

```go
		// Generate access token
		accessToken, err := generateToken(user.ID, user.Email, h.Config.JWTSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to generate token"})
			return
		}

		// Generate refresh token
		rawRefresh, hashedRefresh, err := generateRefreshToken()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to generate refresh token"})
			return
		}

		// Store refresh token
		rt := models.RefreshToken{
			UserID:    user.ID,
			TokenHash: hashedRefresh,
			ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		}
		if err := config.DB.Create(&rt).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to store refresh token"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "Login successful",
			"data": gin.H{
				"userId":             user.ID.String(),
				"role":               user.Role,
				"token":              accessToken,
				"accessToken":        accessToken,
				"refreshToken":       rawRefresh,
				"name":               user.Name,
				"email":              user.Email,
				"verificationStatus": user.VerificationStatus,
				"accountStatus":      user.AccountStatus,
				"emailVerified":      user.EmailVerified,
			},
		})
```

Note: `token` is kept for backwards compatibility alongside `accessToken`.

- [ ] **Step 4: Modify Signup handler to return refresh token**

Apply the same pattern to the Signup handler. After the existing `generateToken` call and before the success response, add:

```go
		rawRefresh, hashedRefresh, err := generateRefreshToken()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to generate refresh token"})
			return
		}
		rt := models.RefreshToken{
			UserID:    user.ID,
			TokenHash: hashedRefresh,
			ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		}
		if err := config.DB.Create(&rt).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to store refresh token"})
			return
		}
```

And add `"refreshToken": rawRefresh` and `"accessToken": accessToken` to the signup success response alongside the existing `"token"` field.

- [ ] **Step 5: Create refresh_handler.go**

Create `internal/handlers/refresh_handler.go`:

```go
package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"tsa-api-go/internal/config"
	"tsa-api-go/internal/models"
)

type refreshRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

func (h *Handlers) RefreshToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "refreshToken is required"})
		return
	}

	// Look up the refresh token by hash
	tokenHash := hashToken(req.RefreshToken)
	var stored models.RefreshToken
	result := config.DB.Where("token_hash = ? AND revoked = false AND expires_at > ?", tokenHash, time.Now()).First(&stored)
	if result.Error != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid or expired refresh token"})
		return
	}

	// Verify user exists and is active
	var user models.User
	if err := config.DB.First(&user, "id = ?", stored.UserID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "User not found"})
		return
	}
	if user.AccountStatus != models.AccountStatusActive {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Account is not active"})
		return
	}

	// Revoke the old refresh token (rotation)
	config.DB.Model(&stored).Update("revoked", true)

	// Generate new access token
	accessToken, err := generateToken(user.ID, user.Email, h.Config.JWTSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to generate token"})
		return
	}

	// Generate new refresh token
	rawRefresh, hashedRefresh, err := generateRefreshToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to generate refresh token"})
		return
	}
	rt := models.RefreshToken{
		UserID:    user.ID,
		TokenHash: hashedRefresh,
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}
	if err := config.DB.Create(&rt).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to store refresh token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Token refreshed",
		"data": gin.H{
			"accessToken":  accessToken,
			"refreshToken": rawRefresh,
			"userId":       user.ID.String(),
			"email":        user.Email,
			"role":         user.Role,
		},
	})
}

type revokeRequest struct {
	RefreshToken string `json:"refreshToken"`
}

func (h *Handlers) RevokeToken(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Authentication required"})
		return
	}

	var req revokeRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.RefreshToken == "" {
		// Revoke ALL refresh tokens for this user
		config.DB.Model(&models.RefreshToken{}).Where("user_id = ? AND revoked = false", user.ID).Update("revoked", true)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "All sessions revoked"})
		return
	}

	// Revoke specific token
	tokenHash := hashToken(req.RefreshToken)
	result := config.DB.Model(&models.RefreshToken{}).
		Where("token_hash = ? AND user_id = ? AND revoked = false", tokenHash, user.ID).
		Update("revoked", true)
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Token not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Token revoked"})
}
```

- [ ] **Step 6: Register new routes**

In `internal/routes/routes.go`, add inside the `authGroup` block:

```go
authGroup := api.Group("/auth")
{
	authGroup.POST("/signup", h.Signup)
	authGroup.POST("/login", h.Login)
	authGroup.POST("/refresh", h.RefreshToken)
	authGroup.POST("/revoke", auth, h.RevokeToken)
	authGroup.POST("/send-otp", auth, h.SendOTP)
	authGroup.POST("/verify-otp", auth, h.VerifyOTP)
	authGroup.POST("/resend-otp", auth, h.ResendOTP)
	authGroup.POST("/forgot-password", h.ForgotPassword)
	authGroup.POST("/reset-password", h.ResetPassword)
}
```

Note: `/refresh` is unauthenticated (uses refresh token). `/revoke` requires auth middleware.

- [ ] **Step 7: Build and verify**

```bash
cd tsa-dev/tsa-api-go && go build ./cmd/server/
```

Expected: builds without errors.

- [ ] **Step 8: Commit**

```bash
git add internal/handlers/refresh_handler.go internal/handlers/auth_handler.go internal/routes/routes.go
git commit -m "feat: add refresh token generation, /auth/refresh, /auth/revoke endpoints"
```

---

## Task 3: localAuth.ts — Refresh Token Helpers (Frontend)

**Files:**
- Modify: `tsa-dev/tsa-app/services/localAuth.ts`

- [ ] **Step 1: Add refresh token SecureStore functions**

Add at the bottom of `services/localAuth.ts`, before the closing of the file:

```typescript
// ── Refresh token (SecureStore – survives soft logout) ──

const REFRESH_TOKEN_KEY = 'tsa-refresh-token';

export async function storeRefreshToken(token: string): Promise<void> {
  await SecureStore.setItemAsync(REFRESH_TOKEN_KEY, token);
}

export async function getRefreshToken(): Promise<string | null> {
  return SecureStore.getItemAsync(REFRESH_TOKEN_KEY);
}

export async function clearRefreshToken(): Promise<void> {
  await SecureStore.deleteItemAsync(REFRESH_TOKEN_KEY);
}

export async function hasRefreshToken(): Promise<boolean> {
  const token = await SecureStore.getItemAsync(REFRESH_TOKEN_KEY);
  return !!token;
}
```

- [ ] **Step 2: Add authorization helper for the AuthorizationModal**

Add below the refresh token functions:

```typescript
// ── Sensitive operation authorization ──

export async function authorizeWithBiometric(): Promise<boolean> {
  const state = await getLockState();
  if (state.hasBiometric) {
    return authenticateWithBiometric();
  }
  return false;
}
```

- [ ] **Step 3: Commit**

```bash
git add tsa-dev/tsa-app/services/localAuth.ts
git commit -m "feat: add refresh token SecureStore helpers and authorization function to localAuth"
```

---

## Task 4: api.ts — Refresh/Revoke Calls + Response Parsing (Frontend)

**Files:**
- Modify: `tsa-dev/tsa-app/components/services/api.ts`

- [ ] **Step 1: Update SignupResponse interface to include refreshToken**

Find the `SignupResponse` interface (around line 13) and update it:

```typescript
export interface SignupResponse {
  userId: string;
  token: string;
  accessToken?: string;
  refreshToken?: string;
  role: string;
  emailVerified?: boolean;
  nextStep: 'email_verification' | 'identity_verification' | 'facial_verification' | 'complete';
}
```

- [ ] **Step 2: Update login() to store refresh token**

In the `login()` method (around line 458), after the existing token storage, add refresh token handling. Find the block `if (result.success && result.data?.token)` and update it:

```typescript
    if (result.success && result.data) {
      const accessToken = result.data.accessToken || result.data.token;
      this.setToken(accessToken);
      await AsyncStorage.setItem('authToken', accessToken);
      await AsyncStorage.setItem('userId', result.data.userId);
      await AsyncStorage.setItem('role', result.data.role);
      // Store refresh token in SecureStore
      if (result.data.refreshToken) {
        const { storeRefreshToken } = await import('../../services/localAuth');
        await storeRefreshToken(result.data.refreshToken);
      }
    }
```

- [ ] **Step 3: Update signup() to store refresh token**

Apply the same pattern to the `signup()` method (around line 417). Find its success block and add:

```typescript
      if (result.data.refreshToken) {
        const { storeRefreshToken } = await import('../../services/localAuth');
        await storeRefreshToken(result.data.refreshToken);
      }
```

- [ ] **Step 4: Add refreshSession() and revokeSession() methods**

Add these methods to the `APIService` class:

```typescript
  async refreshSession(): Promise<ApiResponse<{ accessToken: string; refreshToken: string }>> {
    try {
      const { getRefreshToken, storeRefreshToken } = await import('../../services/localAuth');
      const refreshToken = await getRefreshToken();
      if (!refreshToken) {
        return { success: false, message: 'No refresh token available' };
      }
      const response = await fetch(`${API_BASE_URL}/auth/refresh`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refreshToken }),
      });
      const result = await this.handleResponse<{ accessToken: string; refreshToken: string }>(response);
      if (result.success && result.data) {
        this.setToken(result.data.accessToken);
        await AsyncStorage.setItem('authToken', result.data.accessToken);
        await storeRefreshToken(result.data.refreshToken);
      }
      return result;
    } catch (error: any) {
      return { success: false, message: error.message || 'Failed to refresh session' };
    }
  }

  async revokeSession(): Promise<ApiResponse<any>> {
    try {
      const { getRefreshToken, clearRefreshToken } = await import('../../services/localAuth');
      const refreshToken = await getRefreshToken();
      const response = await fetch(`${API_BASE_URL}/auth/revoke`, {
        method: 'POST',
        headers: this.getHeaders(),
        body: JSON.stringify({ refreshToken: refreshToken || '' }),
      });
      const result = await this.handleResponse<any>(response);
      await clearRefreshToken();
      return result;
    } catch (error: any) {
      return { success: false, message: error.message || 'Failed to revoke session' };
    }
  }
```

- [ ] **Step 5: Commit**

```bash
git add tsa-dev/tsa-app/components/services/api.ts
git commit -m "feat: add refresh/revoke API calls, store refresh token on login/signup"
```

---

## Task 5: AuthContext — Silent Refresh + Logout Changes (Frontend)

**Files:**
- Modify: `tsa-dev/tsa-app/AuthContext/AuthContext.tsx`

- [ ] **Step 1: Update logOut() to keep refresh token (soft logout)**

Replace the existing `logOut` function (around line 406) with:

```typescript
  async function logOut() {
    setToken("");
    setAuthenticated(false);
    setUsername("");
    apiClient.interceptors.request.use((config: any) => {
      config.headers.Authorization = "";
      return config;
    });
    await AsyncStorage.removeItem("token");
    await AsyncStorage.removeItem("authToken");
    api.clearToken();
    setCurrentUser(null);
    router.push("/login");
  }
```

Key change: does NOT call `clearRefreshToken()`. Refresh token stays in SecureStore.

- [ ] **Step 2: Add logOutFull() for complete device sign-out**

Add this function right after `logOut`:

```typescript
  async function logOutFull() {
    try {
      await api.revokeSession();
    } catch {}
    const { clearRefreshToken } = await import('../services/localAuth');
    await clearRefreshToken();
    await logOut();
  }
```

- [ ] **Step 3: Update hydrateAuth() with silent refresh**

Replace the existing `hydrateAuth` function (around line 421) with:

```typescript
  async function hydrateAuth() {
    try {
      const storedToken = await AsyncStorage.getItem("authToken") || await AsyncStorage.getItem("token");
      if (storedToken) {
        setToken(storedToken);
        setAuthenticated(true);
        await AsyncStorage.setItem("authToken", storedToken);
        await AsyncStorage.setItem("token", storedToken);
        apiClient.interceptors.request.use((config: any) => {
          config.headers.Authorization = storedToken;
          return config;
        });
        try {
          const response = await getCurrentUser();
          const userData = response.data?.data ?? response.data;
          setCurrentUser(userData);
          setEmailVerified(userData?.emailVerified ?? false);
        } catch (err: any) {
          // Access token expired — try silent refresh
          const refreshResult = await api.refreshSession();
          if (refreshResult.success && refreshResult.data) {
            const newToken = refreshResult.data.accessToken;
            setToken(newToken);
            setAuthenticated(true);
            await AsyncStorage.setItem("authToken", newToken);
            await AsyncStorage.setItem("token", newToken);
            apiClient.interceptors.request.use((config: any) => {
              config.headers.Authorization = "Bearer " + newToken;
              return config;
            });
            try {
              const response = await getCurrentUser();
              const userData = response.data?.data ?? response.data;
              setCurrentUser(userData);
              setEmailVerified(userData?.emailVerified ?? false);
            } catch {
              // Refresh also failed — clear access token but keep refresh for biometric login
              setAuthenticated(false);
              setToken("");
              await AsyncStorage.removeItem("token");
              await AsyncStorage.removeItem("authToken");
            }
          } else {
            // No refresh token or refresh failed — clear session
            setAuthenticated(false);
            setToken("");
            await AsyncStorage.removeItem("token");
            await AsyncStorage.removeItem("authToken");
          }
        }
      }
    } catch (err) {
      console.error("Error hydrating auth:", err);
    } finally {
      setIsHydrated(true);
    }
  }
```

- [ ] **Step 4: Export logOutFull in context**

Add `logOutFull` to the `AppContextType` interface:

```typescript
  logOutFull: () => Promise<void>;
```

Add default:

```typescript
  logOutFull: async () => {},
```

Add to provider value:

```typescript
  logOutFull,
```

- [ ] **Step 5: Commit**

```bash
git add tsa-dev/tsa-app/AuthContext/AuthContext.tsx
git commit -m "feat: add silent refresh in hydration, soft logout keeps refresh token, add logOutFull"
```

---

## Task 6: Fix Biometric Login Button (Frontend)

**Files:**
- Modify: `tsa-dev/tsa-app/components/onboarding/Login.tsx`

- [ ] **Step 1: Update the biometric check useEffect**

Replace the existing `useEffect` (lines 37-49) that checks for biometric availability:

```typescript
  // Check if user can use biometric/PIN login via stored refresh token
  useEffect(() => {
    (async () => {
      const { hasRefreshToken } = await import('../../services/localAuth');
      const hasToken = await hasRefreshToken();
      if (!hasToken) return;
      const lockOn = await isLockEnabled();
      const bioOn = await isBiometricEnabled();
      const bioAvail = await isBiometricAvailable();
      if (lockOn && bioOn && bioAvail) {
        setShowBiometric(true);
        setBioType(await getBiometricType());
      }
    })();
  }, []);
```

Key change: checks `hasRefreshToken()` (SecureStore) instead of `AsyncStorage.getItem("authToken")`.

- [ ] **Step 2: Update handleBiometricLogin to use refresh token**

Replace the existing `handleBiometricLogin` function (lines 51-69):

```typescript
  const handleBiometricLogin = async () => {
    const success = await authenticateWithBiometric();
    if (!success) return;

    setLoading(true);
    try {
      const refreshResult = await api.refreshSession();
      if (refreshResult.success && refreshResult.data) {
        setAuthenticated(true);
        setAuthToken(refreshResult.data.accessToken);
        try {
          const response = await api.getProfile();
          if (response.success && response.data) {
            setCurrentUser(response.data);
            setEmailVerified(response.data.emailVerified ?? false);
          }
        } catch {}
        router.replace("/home");
      } else {
        // Refresh token expired or revoked
        const { clearRefreshToken } = await import('../../services/localAuth');
        await clearRefreshToken();
        setShowBiometric(false);
        setGeneralError("Session expired. Please sign in with your password.");
      }
    } catch (error) {
      setGeneralError("Failed to authenticate. Please try again.");
    } finally {
      setLoading(false);
    }
  };
```

Key change: calls `api.refreshSession()` to get a fresh access token instead of reading a stale one from AsyncStorage.

- [ ] **Step 3: Commit**

```bash
git add tsa-dev/tsa-app/components/onboarding/Login.tsx
git commit -m "fix: biometric login uses refresh token from SecureStore instead of expired access token"
```

---

## Task 7: AuthorizationModal Component (Frontend)

**Files:**
- Create: `tsa-dev/tsa-app/components/common/AuthorizationModal.tsx`

- [ ] **Step 1: Create the AuthorizationModal component**

Create `components/common/AuthorizationModal.tsx`:

```typescript
import React, { useState, useEffect } from 'react';
import {
  Modal, View, Text, TouchableOpacity, TextInput,
  ActivityIndicator, StyleSheet, Alert,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import {
  isBiometricAvailable, isBiometricEnabled, getBiometricType,
  authenticateWithBiometric, hasPin, verifyPin,
} from '../../services/localAuth';
import api from '../services/api';

const COLORS = {
  primary: '#D4AF37',
  gold: '#FFD700',
  dark: '#1a1a1a',
  gray: '#666',
  lightGray: '#e0e0e0',
  danger: '#DC3545',
  white: '#fff',
  overlay: 'rgba(0,0,0,0.5)',
};

interface AuthorizationModalProps {
  visible: boolean;
  title?: string;
  description?: string;
  onAuthorized: () => void;
  onCancel: () => void;
}

type AuthMethod = 'choose' | 'biometric' | 'pin' | 'otp';

export default function AuthorizationModal({
  visible,
  title = 'Authorize',
  description,
  onAuthorized,
  onCancel,
}: AuthorizationModalProps) {
  const [method, setMethod] = useState<AuthMethod>('choose');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  // Biometric state
  const [bioAvailable, setBioAvailable] = useState(false);
  const [bioType, setBioType] = useState('Fingerprint');

  // PIN state
  const [pinAvailable, setPinAvailable] = useState(false);
  const [pinValue, setPinValue] = useState('');
  const [pinAttempts, setPinAttempts] = useState(0);

  // OTP state
  const [otpValue, setOtpValue] = useState('');
  const [otpSent, setOtpSent] = useState(false);
  const [otpCountdown, setOtpCountdown] = useState(0);

  useEffect(() => {
    if (!visible) {
      setMethod('choose');
      setError('');
      setPinValue('');
      setOtpValue('');
      setOtpSent(false);
      setPinAttempts(0);
      setLoading(false);
      return;
    }
    (async () => {
      const [bioOk, bioOn, pinOk, type] = await Promise.all([
        isBiometricAvailable(),
        isBiometricEnabled(),
        hasPin(),
        getBiometricType(),
      ]);
      setBioAvailable(bioOk && bioOn);
      setPinAvailable(pinOk);
      setBioType(type);
    })();
  }, [visible]);

  // OTP countdown timer
  useEffect(() => {
    if (otpCountdown <= 0) return;
    const timer = setTimeout(() => setOtpCountdown(otpCountdown - 1), 1000);
    return () => clearTimeout(timer);
  }, [otpCountdown]);

  const handleBiometric = async () => {
    setError('');
    const success = await authenticateWithBiometric();
    if (success) {
      onAuthorized();
    } else {
      setError('Biometric verification failed. Try another method.');
    }
  };

  const handlePinSubmit = async (pin: string) => {
    setError('');
    setLoading(true);
    const valid = await verifyPin(pin);
    setLoading(false);
    if (valid) {
      onAuthorized();
    } else {
      const attempts = pinAttempts + 1;
      setPinAttempts(attempts);
      setPinValue('');
      if (attempts >= 3) {
        setError('Too many PIN attempts. Use OTP instead.');
        setMethod('otp');
        setPinAttempts(0);
      } else {
        setError(`Incorrect PIN. ${3 - attempts} attempts remaining.`);
      }
    }
  };

  const handleSendOtp = async () => {
    setError('');
    setLoading(true);
    const result = await api.sendOtp();
    setLoading(false);
    if (result.success) {
      setOtpSent(true);
      setOtpCountdown(60);
    } else {
      setError(result.message || 'Failed to send OTP');
    }
  };

  const handleVerifyOtp = async () => {
    setError('');
    setLoading(true);
    const result = await api.verifyOtp(otpValue);
    setLoading(false);
    if (result.success) {
      onAuthorized();
    } else {
      setOtpValue('');
      setError(result.message || 'Invalid OTP. Please try again.');
    }
  };

  const renderChoose = () => (
    <View style={styles.methodContainer}>
      {bioAvailable && (
        <TouchableOpacity style={styles.methodButton} onPress={handleBiometric}>
          <Ionicons
            name={bioType === 'Face ID' ? 'scan' : 'finger-print'}
            size={28}
            color={COLORS.primary}
          />
          <Text style={styles.methodText}>Use {bioType}</Text>
        </TouchableOpacity>
      )}
      {pinAvailable && (
        <TouchableOpacity style={styles.methodButton} onPress={() => setMethod('pin')}>
          <Ionicons name="keypad" size={28} color={COLORS.primary} />
          <Text style={styles.methodText}>Use PIN</Text>
        </TouchableOpacity>
      )}
      <TouchableOpacity
        style={styles.methodButton}
        onPress={() => { setMethod('otp'); handleSendOtp(); }}
      >
        <Ionicons name="mail" size={28} color={COLORS.primary} />
        <Text style={styles.methodText}>Use Email OTP</Text>
      </TouchableOpacity>
    </View>
  );

  const renderPin = () => (
    <View style={styles.inputContainer}>
      <Text style={styles.inputLabel}>Enter your 4-digit PIN</Text>
      <TextInput
        style={styles.pinInput}
        value={pinValue}
        onChangeText={(text) => {
          setPinValue(text);
          if (text.length === 4) handlePinSubmit(text);
        }}
        keyboardType="number-pad"
        maxLength={4}
        secureTextEntry
        autoFocus
        placeholder="••••"
        placeholderTextColor="#ccc"
      />
      {loading && <ActivityIndicator color={COLORS.primary} style={{ marginTop: 12 }} />}
      <TouchableOpacity onPress={() => setMethod('choose')} style={styles.backButton}>
        <Text style={styles.backText}>Try another method</Text>
      </TouchableOpacity>
    </View>
  );

  const renderOtp = () => (
    <View style={styles.inputContainer}>
      {!otpSent ? (
        <>
          <Text style={styles.inputLabel}>Sending verification code to your email...</Text>
          <ActivityIndicator color={COLORS.primary} />
        </>
      ) : (
        <>
          <Text style={styles.inputLabel}>Enter the 6-digit code sent to your email</Text>
          <TextInput
            style={styles.otpInput}
            value={otpValue}
            onChangeText={setOtpValue}
            keyboardType="number-pad"
            maxLength={6}
            autoFocus
            placeholder="000000"
            placeholderTextColor="#ccc"
          />
          <TouchableOpacity
            style={[styles.submitBtn, otpValue.length !== 6 && styles.submitBtnDisabled]}
            onPress={handleVerifyOtp}
            disabled={otpValue.length !== 6 || loading}
          >
            {loading ? (
              <ActivityIndicator color="#fff" size="small" />
            ) : (
              <Text style={styles.submitBtnText}>Verify</Text>
            )}
          </TouchableOpacity>
          <TouchableOpacity
            onPress={handleSendOtp}
            disabled={otpCountdown > 0 || loading}
            style={styles.resendBtn}
          >
            <Text style={[styles.resendText, otpCountdown > 0 && { color: '#ccc' }]}>
              {otpCountdown > 0 ? `Resend in ${otpCountdown}s` : 'Resend Code'}
            </Text>
          </TouchableOpacity>
        </>
      )}
      <TouchableOpacity onPress={() => setMethod('choose')} style={styles.backButton}>
        <Text style={styles.backText}>Try another method</Text>
      </TouchableOpacity>
    </View>
  );

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onCancel}>
      <View style={styles.overlay}>
        <View style={styles.modal}>
          <View style={styles.header}>
            <Text style={styles.title}>{title}</Text>
            <TouchableOpacity onPress={onCancel} hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}>
              <Ionicons name="close" size={24} color={COLORS.gray} />
            </TouchableOpacity>
          </View>

          {description && <Text style={styles.description}>{description}</Text>}

          {error !== '' && (
            <View style={styles.errorContainer}>
              <Ionicons name="alert-circle" size={16} color={COLORS.danger} />
              <Text style={styles.errorText}>{error}</Text>
            </View>
          )}

          {method === 'choose' && renderChoose()}
          {method === 'pin' && renderPin()}
          {method === 'otp' && renderOtp()}
        </View>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  overlay: {
    flex: 1,
    backgroundColor: COLORS.overlay,
    justifyContent: 'flex-end',
  },
  modal: {
    backgroundColor: COLORS.white,
    borderTopLeftRadius: 24,
    borderTopRightRadius: 24,
    padding: 24,
    paddingBottom: 40,
    minHeight: 300,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  title: {
    fontSize: 20,
    fontWeight: '700',
    color: COLORS.dark,
  },
  description: {
    fontSize: 14,
    color: COLORS.gray,
    marginBottom: 20,
  },
  errorContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#fff5f5',
    borderRadius: 8,
    padding: 10,
    marginBottom: 16,
  },
  errorText: {
    color: COLORS.danger,
    fontSize: 13,
    marginLeft: 8,
    flex: 1,
  },
  methodContainer: {
    gap: 12,
    marginTop: 8,
  },
  methodButton: {
    flexDirection: 'row',
    alignItems: 'center',
    padding: 16,
    borderRadius: 14,
    borderWidth: 1.5,
    borderColor: COLORS.lightGray,
    gap: 14,
  },
  methodText: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.dark,
  },
  inputContainer: {
    alignItems: 'center',
    marginTop: 8,
  },
  inputLabel: {
    fontSize: 14,
    color: COLORS.gray,
    marginBottom: 16,
    textAlign: 'center',
  },
  pinInput: {
    fontSize: 32,
    fontWeight: '700',
    textAlign: 'center',
    letterSpacing: 16,
    width: 200,
    borderBottomWidth: 2,
    borderBottomColor: COLORS.primary,
    paddingVertical: 8,
    color: COLORS.dark,
  },
  otpInput: {
    fontSize: 24,
    fontWeight: '600',
    textAlign: 'center',
    letterSpacing: 8,
    width: 240,
    borderBottomWidth: 2,
    borderBottomColor: COLORS.primary,
    paddingVertical: 8,
    color: COLORS.dark,
  },
  submitBtn: {
    backgroundColor: COLORS.primary,
    paddingVertical: 14,
    paddingHorizontal: 48,
    borderRadius: 12,
    marginTop: 20,
    alignItems: 'center',
  },
  submitBtnDisabled: {
    opacity: 0.5,
  },
  submitBtnText: {
    color: '#fff',
    fontSize: 16,
    fontWeight: '600',
  },
  resendBtn: {
    marginTop: 16,
  },
  resendText: {
    fontSize: 14,
    fontWeight: '500',
    color: COLORS.primary,
  },
  backButton: {
    marginTop: 20,
  },
  backText: {
    fontSize: 14,
    color: COLORS.primary,
    fontWeight: '500',
  },
});
```

- [ ] **Step 2: Commit**

```bash
git add tsa-dev/tsa-app/components/common/AuthorizationModal.tsx
git commit -m "feat: add AuthorizationModal component with biometric/PIN/OTP options"
```

---

## Task 8: Integrate AuthorizationModal in sendFiat (Frontend)

**Files:**
- Modify: `tsa-dev/tsa-app/screens/sendFiat.tsx`

- [ ] **Step 1: Add import and state**

Add the import at the top of `sendFiat.tsx`:

```typescript
import AuthorizationModal from '../components/common/AuthorizationModal';
```

Add state (alongside the existing `showOTPModal` state around line 174):

```typescript
const [showAuthModal, setShowAuthModal] = useState(false);
```

- [ ] **Step 2: Replace OTP trigger with AuthorizationModal trigger**

Find `handleConfirmPayment` (around line 342) where it currently does `setShowOTPModal(true)`. Replace the OTP modal trigger:

Change `setShowOTPModal(true)` to `setShowAuthModal(true)`.

- [ ] **Step 3: Add authorized callback**

Add a handler that runs after authorization succeeds — this should contain the logic that currently lives in `handleOTPSubmit` (the actual transaction submission):

```typescript
const handleAuthorized = async () => {
  setShowAuthModal(false);
  // Proceed with the existing transaction submission logic
  // (move the transaction submission code from handleOTPSubmit here)
};
```

- [ ] **Step 4: Replace OTP modal JSX with AuthorizationModal**

Find the `OTPModal` component usage in the JSX (around line 676-738) and replace it with:

```typescript
<AuthorizationModal
  visible={showAuthModal}
  title="Authorize Payment"
  description={`Send ${formatCurrency(amount)} to ${recipientName}`}
  onAuthorized={handleAuthorized}
  onCancel={() => setShowAuthModal(false)}
/>
```

Remove the old `OTPModal` component and its associated `otp` state, `handleOTPSubmit`, and `showOTPModal` state if they are no longer used.

- [ ] **Step 5: Commit**

```bash
git add tsa-dev/tsa-app/screens/sendFiat.tsx
git commit -m "feat: replace OTP modal with AuthorizationModal in fiat send"
```

---

## Task 9: Integrate AuthorizationModal in instant-pay (Frontend)

**Files:**
- Modify: `tsa-dev/tsa-app/app/wallet/instant-pay.tsx`

- [ ] **Step 1: Add import and state**

Add at the top of `instant-pay.tsx`:

```typescript
import AuthorizationModal from '../../components/common/AuthorizationModal';
```

Add state:

```typescript
const [showAuthModal, setShowAuthModal] = useState(false);
```

- [ ] **Step 2: Gate transaction submission behind authorization**

Find `handleSend` (around line 117). Currently it calls `prepareSendTransaction` directly. Change it to first show the authorization modal:

Replace the current `handleSend`:

```typescript
const handleSendRequest = () => {
  if (!requireKycVerified() || !resolvedUser) return;
  setShowAuthModal(true);
};

const handleSend = async () => {
  setShowAuthModal(false);
  setScreen('sending');
  setError('');
  try {
    const r = await prepareSendTransaction(selectedToken, resolvedUser!.walletAddress, amount, activeChain.chainId);
    if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare');
    const signed = await signTransaction({
      to: r.data.to, data: r.data.data, value: r.data.value,
      gasLimit: r.data.gasLimit, gasPrice: r.data.gasPrice,
      nonce: r.data.nonce, chainId: r.data.chainId,
    });
    const res = await submitTransaction(signed, 'instant_pay', selectedToken, resolvedUser!.walletAddress, amount, activeChain.chainId);
    if (res.success && res.data) { setTxHash(res.data.txHash); setScreen('success'); }
    else throw new Error(res.message || 'Failed');
  } catch (e: any) { setError(e.message || 'Failed.'); setScreen('failure'); }
};
```

- [ ] **Step 3: Update the send button to use handleSendRequest**

Find the "Send" button on the confirm screen (around line 220) and change its `onPress` from `handleSend` to `handleSendRequest`.

- [ ] **Step 4: Add AuthorizationModal to JSX**

Add before the closing `</>` or `</View>`:

```typescript
<AuthorizationModal
  visible={showAuthModal}
  title="Authorize Transfer"
  description={`Send ${amount} ${selectedToken} to @${resolvedUser?.username}`}
  onAuthorized={handleSend}
  onCancel={() => setShowAuthModal(false)}
/>
```

- [ ] **Step 5: Commit**

```bash
git add tsa-dev/tsa-app/app/wallet/instant-pay.tsx
git commit -m "feat: add AuthorizationModal before crypto transfers in instant-pay"
```

---

## Task 10: Settings — Sign Out of This Device (Frontend)

**Files:**
- Modify: `tsa-dev/tsa-app/app/(dashboard)/settings.tsx`

- [ ] **Step 1: Import logOutFull from auth context**

Update the `useAuth` destructure (around line 32):

```typescript
const { logOut, logOutFull, currentUser } = useAuth();
```

- [ ] **Step 2: Add handleFullLogout function**

Add alongside the existing `handleLogout` (around line 121):

```typescript
const handleFullLogout = () => {
  Alert.alert(
    "Sign Out of This Device",
    "This will remove biometric login. You'll need your password to sign in again.",
    [
      { text: "Cancel", style: "cancel" },
      {
        text: "Sign Out",
        style: "destructive",
        onPress: async () => {
          await logOutFull();
        },
      },
    ]
  );
};
```

- [ ] **Step 3: Add the button in the settings UI**

Find the existing logout button in the JSX (the security or account section) and add a new option below it:

```typescript
<TouchableOpacity style={styles.dangerButton} onPress={handleFullLogout}>
  <Ionicons name="log-out-outline" size={20} color="#DC3545" />
  <Text style={styles.dangerButtonText}>Sign Out of This Device</Text>
</TouchableOpacity>
```

Add the styles:

```typescript
dangerButton: {
  flexDirection: 'row',
  alignItems: 'center',
  padding: 16,
  gap: 12,
  borderTopWidth: 1,
  borderTopColor: '#f0f0f0',
},
dangerButtonText: {
  fontSize: 15,
  fontWeight: '500',
  color: '#DC3545',
},
```

- [ ] **Step 4: Commit**

```bash
git add tsa-dev/tsa-app/app/(dashboard)/settings.tsx
git commit -m "feat: add Sign Out of This Device option that revokes refresh token"
```

---

## Task 11: End-to-End Verification

- [ ] **Step 1: Start the backend**

```bash
cd tsa-dev/tsa-api-go && go run ./cmd/server/
```

Verify: `refresh_tokens` table is created (check server logs for GORM migration output).

- [ ] **Step 2: Test login returns refresh token**

```bash
curl -X POST http://localhost:5000/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"test@example.com","password":"testpass"}'
```

Verify: response includes `accessToken`, `refreshToken`, and legacy `token` fields.

- [ ] **Step 3: Test refresh endpoint**

```bash
curl -X POST http://localhost:5000/api/auth/refresh \
  -H 'Content-Type: application/json' \
  -d '{"refreshToken":"<token-from-step-2>"}'
```

Verify: returns new `accessToken` and `refreshToken`.

- [ ] **Step 4: Test revoke endpoint**

```bash
curl -X POST http://localhost:5000/api/auth/revoke \
  -H 'Authorization: Bearer <access-token>' \
  -H 'Content-Type: application/json' \
  -d '{"refreshToken":"<token-from-step-3>"}'
```

Verify: returns success. Using the revoked refresh token should return 401.

- [ ] **Step 5: Test frontend biometric login flow**

1. Log in with password → verify refresh token stored
2. Log out (soft) → verify biometric button appears on login screen
3. Tap biometric → verify it calls `/auth/refresh` and logs in
4. Log out full → verify biometric button does NOT appear

- [ ] **Step 6: Test AuthorizationModal**

1. Navigate to fiat send → confirm payment → verify AuthorizationModal appears with biometric/PIN/OTP options
2. Navigate to instant-pay → send → verify AuthorizationModal appears before signing
3. Test each method: biometric, PIN, OTP

- [ ] **Step 7: Commit any fixes**

```bash
git add -A
git commit -m "fix: end-to-end verification fixes for biometrics/PIN auth"
```
