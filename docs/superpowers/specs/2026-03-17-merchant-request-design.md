# Sub-project 8: Merchant Request + Admin Approval

## Overview

"Become a Merchant" flow: users submit a merchant application from the mobile app, admins review and approve/reject in the admin panel. On approval, the user's role is automatically upgraded to `merchant`, granting access to the merchant portal.

## Decisions

- **Business type**: Metadata only — all approved merchants get the same access regardless of type
- **Business types**: `general_products`, `digital_products`, `p2p_merchant`, `service_provider`
- **Info collected**: Jiji-style — business name, description, address, city, state, country, phone, optional registration number
- **Notification**: No email — admin just sees requests in a queue in the admin panel
- **Re-apply**: Allowed immediately after rejection. Old request stays as `rejected`, new one is created
- **On approval**: Auto-update user role from `user` → `merchant`
- **Chain**: Sonic testnet only for MVP (no on-chain component for this feature)

## Model: MerchantRequest

New file: `internal/models/merchant_request.go`

```go
package models

import (
    "time"
    "github.com/google/uuid"
)

const (
    MerchantRequestStatusPending  = "pending"
    MerchantRequestStatusApproved = "approved"
    MerchantRequestStatusRejected = "rejected"
)

const (
    BusinessTypeGeneralProducts = "general_products"
    BusinessTypeDigitalProducts = "digital_products"
    BusinessTypeP2PMerchant     = "p2p_merchant"
    BusinessTypeServiceProvider = "service_provider"
)

type MerchantRequest struct {
    ID                 uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    UserID             uuid.UUID  `gorm:"type:uuid;index;not null" json:"userId"`
    BusinessType       string     `gorm:"not null" json:"businessType"`
    BusinessName       string     `gorm:"not null" json:"businessName"`
    BusinessDescription string    `gorm:"type:text" json:"businessDescription"`
    Address            string     `gorm:"not null" json:"address"`
    City               string     `gorm:"not null" json:"city"`
    State              string     `gorm:"not null" json:"state"`
    Country            string     `gorm:"not null" json:"country"`
    Phone              string     `gorm:"not null" json:"phone"`
    RegistrationNumber string     `json:"registrationNumber,omitempty"`
    Status             string     `gorm:"not null;default:'pending';index" json:"status"`
    AdminNote          string     `json:"adminNote,omitempty"`
    ReviewedBy         *uuid.UUID `gorm:"type:uuid" json:"reviewedBy,omitempty"`
    ReviewedAt         *time.Time `json:"reviewedAt,omitempty"`
    User               User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
    Reviewer           *User      `gorm:"foreignKey:ReviewedBy" json:"reviewer,omitempty"`
    CreatedAt          time.Time  `json:"createdAt"`
    UpdatedAt          time.Time  `json:"updatedAt"`
}

func (MerchantRequest) TableName() string {
    return "merchant_requests"
}
```

**AutoMigrate**: Add `&MerchantRequest{}` to the AutoMigrate call in `config/database.go`.

## API Endpoints

New file: `internal/handlers/merchant_request_handler.go`

All endpoints under `/api/merchant-requests`.

### 1. Submit Merchant Request

```
POST /api/merchant-requests
Auth: Bearer token (any authenticated user)
```

**Request body:**
```json
{
  "businessType": "general_products",
  "businessName": "Ojay Electronics",
  "businessDescription": "We sell quality electronics and gadgets",
  "address": "123 Market Street",
  "city": "Lagos",
  "state": "Lagos",
  "country": "Nigeria",
  "phone": "+2348012345678",
  "registrationNumber": "RC-123456"
}
```

**Validation:**
- `businessType` must be one of: `general_products`, `digital_products`, `p2p_merchant`, `service_provider`
- `businessName`, `address`, `city`, `state`, `country`, `phone` are required
- `registrationNumber` is optional
- User must not already be a merchant (`role != "merchant"`)
- User must not have an existing `pending` request

**Response (201):**
```json
{
  "success": true,
  "message": "Merchant request submitted successfully",
  "data": { ...merchantRequest }
}
```

**Errors:**
- 400: Validation errors, already a merchant, pending request exists
- 401: Not authenticated

### 2. Get My Merchant Request

```
GET /api/merchant-requests/my-request
Auth: Bearer token
```

Returns the user's most recent merchant request (any status). Returns `null` data if no request exists.

**Response (200):**
```json
{
  "success": true,
  "message": "Merchant request retrieved",
  "data": { ...merchantRequest }  // or null
}
```

### 3. Admin: List Merchant Requests

```
GET /api/admin/merchant-requests?status=pending&page=1&limit=20
Auth: Admin/Super Admin
```

**Query params:**
- `status` (optional): Filter by `pending`, `approved`, `rejected`. Default: all
- `page` (optional): Page number, default 1
- `limit` (optional): Items per page, default 20

**Response (200):**
```json
{
  "success": true,
  "message": "Merchant requests retrieved",
  "data": {
    "requests": [...],
    "total": 42,
    "page": 1,
    "limit": 20
  }
}
```

Each request includes preloaded `User` (name, email, username, phoneNumber, verificationStatus).

### 4. Admin: Approve Merchant Request

```
POST /api/admin/merchant-requests/:id/approve
Auth: Admin/Super Admin
```

**Request body:**
```json
{
  "note": "Verified business details"  // optional
}
```

**Actions:**
1. Set request `status` = `approved`, `reviewedBy` = admin user ID, `reviewedAt` = now, `adminNote` = note
2. Update user `role` = `merchant`

**Response (200):**
```json
{
  "success": true,
  "message": "Merchant request approved",
  "data": { ...updatedMerchantRequest }
}
```

**Errors:**
- 400: Request not in `pending` status
- 404: Request not found

### 5. Admin: Reject Merchant Request

```
POST /api/admin/merchant-requests/:id/reject
Auth: Admin/Super Admin
```

**Request body:**
```json
{
  "note": "Incomplete business information"  // required
}
```

**Actions:**
1. Set request `status` = `rejected`, `reviewedBy` = admin user ID, `reviewedAt` = now, `adminNote` = note

**Response (200):**
```json
{
  "success": true,
  "message": "Merchant request rejected",
  "data": { ...updatedMerchantRequest }
}
```

**Errors:**
- 400: Request not in `pending` status, note is required for rejection
- 404: Request not found

## Routes

In `routes.go`, add:

```go
// Merchant request routes (authenticated user)
merchantReqGroup := api.Group("/merchant-requests")
merchantReqGroup.Use(auth)
{
    merchantReqGroup.POST("", mrh.SubmitMerchantRequest)
    merchantReqGroup.GET("/my-request", mrh.GetMyMerchantRequest)
}

// Admin merchant request routes
adminMerchantReqGroup := api.Group("/admin/merchant-requests")
adminMerchantReqGroup.Use(adminAuth)
{
    adminMerchantReqGroup.GET("", mrh.ListMerchantRequests)
    adminMerchantReqGroup.POST("/:id/approve", mrh.ApproveMerchantRequest)
    adminMerchantReqGroup.POST("/:id/reject", mrh.RejectMerchantRequest)
}
```

Handler struct pattern — follows `ServiceContactHandler` / `CheckoutHandler` pattern with its own struct:

```go
type MerchantRequestHandler struct {
    DB *gorm.DB
}
```

Pass into `SetupRoutes` the same way as `CheckoutHandler` and `ServiceContactHandler`.

## Admin Panel Changes

### Permissions (`tsa-admin/src/lib/permissions.ts`)

Add to `Permission` type:
```typescript
| 'merchant_requests.view'
| 'merchant_requests.approve'
```

Add to `ROLE_PERMISSIONS`:
- `super_admin`: both `merchant_requests.view` and `merchant_requests.approve`
- `admin`: both `merchant_requests.view` and `merchant_requests.approve`
- `support`: `merchant_requests.view` only

Add to `canAccessRoute`:
```typescript
'/merchant-requests': 'merchant_requests.view',
```

### New Page: Merchant Requests

New route: `/merchant-requests`

**Components:**
- DataTable with columns: Business Name, Business Type, Applicant (user name + email), Status badge, Submitted date, Actions
- Status filter tabs: All | Pending | Approved | Rejected
- Click row or "View" button → detail dialog/sheet showing full application info
- Approve button (with optional note textarea)
- Reject button (with required note textarea)

**API calls:**
- `GET /api/admin/merchant-requests?status=...&page=...&limit=...`
- `POST /api/admin/merchant-requests/:id/approve`
- `POST /api/admin/merchant-requests/:id/reject`

**Add to sidebar navigation** (after existing items, before Settings).

## Mobile App Changes

### New Screen: `tsa-app/app/merchants/merchant-request.tsx`

Accessed from AppServices button at index 2 ("Apply as merchant for payment service").

**Flow:**
1. Check if user already has a pending/approved request via `GET /api/merchant-requests/my-request`
2. If `pending` → show status card: "Your application is under review"
3. If `approved` → show success card: "You're a merchant!" with link to merchant dashboard
4. If `rejected` → show rejection reason + "Apply Again" button
5. If no request → show application form

**Form fields:**
- Business Type (picker: General Products, Digital Products, P2P Merchant, Service Provider)
- Business Name (text input)
- Business Description (multiline text input)
- Address (text input)
- City (text input)
- State (text input)
- Country (text input)
- Phone (text input, pre-filled from user profile)
- Registration Number (text input, optional)
- Submit button

**API calls:**
- `GET /api/merchant-requests/my-request`
- `POST /api/merchant-requests`

### Wire Up AppServices Button

In `tsa-app/components/appservices/AppServices.tsx`, update the service at index 2 to navigate to `merchants/merchant-request` instead of showing "Service coming soon".

## Testing

- **Backend**: Unit tests for `merchant_request_handler.go` using SQLite in-memory DB (same pattern as `wallet_handler_test.go`)
- **Test cases**: Submit request, duplicate pending request rejected, already-merchant rejected, admin approve (verify role change), admin reject (verify note required), get my request, list with status filter, re-apply after rejection
