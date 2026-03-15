# TSA Admin Web Panel Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone React web admin panel for TSA Connect that replaces the in-app mobile admin flow, connecting to the existing Go/Gin API.

**Architecture:** Standalone SPA (`tsa-dev/tsa-admin/`) using React + Vite + Shadcn/ui + Tailwind. Communicates with the existing Go API at `tsa-dev/tsa-api-go/` via JWT-authenticated REST calls. RBAC with three roles: super_admin, admin, support.

**Tech Stack:** React 19, Vite, TypeScript, Shadcn/ui, Tailwind CSS, React Router v7, Tanstack Query v5, Axios, Lucide React icons

**Spec:** `docs/superpowers/specs/2026-03-15-tsa-admin-web-panel-design.md`

**Known implementation notes (address during execution):**
- `getUserFromContext` already exists in `handlers/common.go` — no need to create it
- `AuthProvider` wraps `BrowserRouter` in `app.tsx` — this is correct, `useAuth()` is available everywhere
- `.env` should NOT be committed; commit `.env.example` instead
- Shadcn init must configure primary color to `#2563eb` (Blue 600) via CSS variables
- Sidebar should fetch pending counts from `/admin/stats` and show badges on Approvals nav items
- Products page should include type and category filters in addition to status
- Products columns should include an actions column (view, edit, delete buttons)
- Users page should implement `onRowClick` to open a user detail slide-over
- Dashboard should include a "Recent Orders" table (last 10) below the stat cards
- `GetAdminStats` backend handler should include a revenue calculation if order totals are available

---

## File Structure

### Frontend (`tsa-dev/tsa-admin/`)

```
src/
├── api/
│   ├── client.ts          # Axios instance, interceptors, error handling
│   ├── auth.ts            # login(), logout(), getProfile()
│   ├── users.ts           # getUsers(), updateRole(), toggleStatus()
│   ├── products.ts        # getProducts(), toggleFeatured(), updateProduct(), deleteProduct()
│   ├── categories.ts      # getCategories(), createCategory(), updateCategory(), deleteCategory(), reorderCategories()
│   ├── orders.ts          # getOrders(), getOrderById(), updateOrderStatus()
│   ├── deposits.ts        # getDeposits(), approveDeposit(), rejectDeposit()
│   ├── verification.ts    # getVerifications(), approveVerification(), rejectVerification()
│   └── stats.ts           # getDashboardStats()
├── components/
│   ├── ui/                # Shadcn components (generated via CLI)
│   ├── layout/
│   │   ├── app-layout.tsx     # Shell: sidebar + header + content area
│   │   ├── sidebar.tsx        # Collapsible sidebar with nav groups
│   │   └── header.tsx         # Top header with breadcrumbs + page title
│   └── shared/
│       ├── data-table.tsx     # Reusable DataTable wrapper
│       ├── stat-card.tsx      # Dashboard stat card
│       ├── status-badge.tsx   # Colored status badges
│       ├── confirm-dialog.tsx # Confirmation dialog for destructive actions
│       └── page-header.tsx    # Page title + actions bar
├── pages/
│   ├── auth/
│   │   └── login.tsx
│   ├── dashboard/
│   │   └── index.tsx
│   ├── users/
│   │   ├── index.tsx          # Users list
│   │   └── columns.tsx        # DataTable column definitions
│   ├── products/
│   │   ├── index.tsx          # Products list
│   │   └── columns.tsx
│   ├── categories/
│   │   └── index.tsx          # Category tree + CRUD
│   ├── orders/
│   │   ├── index.tsx          # Orders list
│   │   ├── columns.tsx
│   │   └── [id].tsx           # Order detail
│   ├── advert-requests/
│   │   └── index.tsx          # Advert approval cards
│   ├── deposits/
│   │   ├── index.tsx          # Deposit requests list
│   │   └── columns.tsx
│   ├── verifications/
│   │   └── index.tsx          # Verification review
│   └── settings/
│       └── index.tsx          # Role management (super admin)
├── hooks/
│   ├── use-auth.ts            # Auth context consumer hook
│   └── use-permission.ts      # RBAC permission check hook
├── lib/
│   ├── utils.ts               # cn() helper, formatters
│   ├── permissions.ts         # Permission map constant
│   └── constants.ts           # Design tokens, status maps
├── types/
│   └── index.ts               # All TypeScript interfaces
├── contexts/
│   └── auth-context.tsx       # AuthProvider + context
├── app.tsx                    # Root component: providers, router, routes
├── main.tsx                   # Entry point
└── index.css                  # Tailwind imports + global styles
```

### Backend Changes (`tsa-dev/tsa-api-go/`)

```
internal/
├── config/
│   └── config.go              # MODIFY: Add AdminURL field
├── middleware/
│   ├── cors.go                # MODIFY: Support multiple origins
│   ├── admin_auth.go          # MODIFY: Add permission-based access
│   └── permission.go          # CREATE: Permission middleware
├── models/
│   ├── user.go                # MODIFY: Add RoleSupport constant
│   ├── order.go               # CREATE: Order model
│   └── deposit.go             # CREATE: Deposit model
├── handlers/
│   ├── admin_handler.go       # CREATE: Dashboard stats endpoint
│   ├── order_handler.go       # CREATE: Order list + status update
│   ├── deposit_handler.go     # CREATE: Deposit list + approve/reject
│   └── user_handler.go        # MODIFY: Add UpdateRole handler
└── routes/
    └── routes.go              # MODIFY: Wire missing + new routes
```

---

## Chunk 1: Backend Foundations

### Task 1: Add Support Role & Update CORS

**Files:**
- Modify: `tsa-dev/tsa-api-go/internal/models/user.go:120-125`
- Modify: `tsa-dev/tsa-api-go/internal/config/config.go:16-41`
- Modify: `tsa-dev/tsa-api-go/internal/middleware/cors.go`
- Modify: `tsa-dev/tsa-api-go/internal/middleware/admin_auth.go:100-115`

- [ ] **Step 1: Add RoleSupport constant to user model**

In `tsa-dev/tsa-api-go/internal/models/user.go`, add `RoleSupport` to the role constants:

```go
const (
	RoleUser       = "user"
	RoleAdmin      = "admin"
	RoleSuperAdmin = "super_admin"
	RoleMerchant   = "merchant"
	RoleSupport    = "support"
)
```

- [ ] **Step 2: Add AdminURL to config**

In `tsa-dev/tsa-api-go/internal/config/config.go`, add `AdminURL` field to the `Config` struct and load it:

```go
// In Config struct:
AdminURL string

// In Load():
AdminURL: getEnv("ADMIN_URL", "http://localhost:5173"),
```

- [ ] **Step 3: Update CORS to support multiple origins**

Replace `tsa-dev/tsa-api-go/internal/middleware/cors.go`:

```go
package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

func CORS(cfg *config.Config) gin.HandlerFunc {
	allowedOrigins := map[string]bool{
		cfg.FrontendURL: true,
		cfg.AdminURL:    true,
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if allowedOrigins[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
		}
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Requested-With")
		c.Header("Access-Control-Expose-Headers", "Content-Length")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
```

- [ ] **Step 4: Update AdminAuth to include support role**

In `tsa-dev/tsa-api-go/internal/middleware/admin_auth.go`, update the allowed roles:

```go
// Change line 101 from:
allowedRoles := []string{models.RoleAdmin, models.RoleMerchant}
// To:
allowedRoles := []string{models.RoleAdmin, models.RoleSuperAdmin, models.RoleMerchant, models.RoleSupport}
```

- [ ] **Step 5: Build to verify**

Run: `cd tsa-dev/tsa-api-go && go build ./...`
Expected: Build succeeds with no errors.

- [ ] **Step 6: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/models/user.go internal/config/config.go internal/middleware/cors.go internal/middleware/admin_auth.go
git commit -m "feat: add support role, multi-origin CORS, and update admin auth"
```

---

### Task 2: Wire Missing Routes

**Files:**
- Modify: `tsa-dev/tsa-api-go/internal/routes/routes.go:103-113`

- [ ] **Step 1: Add missing product and category routes**

In `tsa-dev/tsa-api-go/internal/routes/routes.go`, expand the product group to include the featured and category routes:

```go
// Product routes (public + auth + admin)
productGroup := api.Group("/products")
{
	productGroup.GET("/", h.GetMarketplaceProducts)
	productGroup.GET("/non-featured", adminAuth, h.GetNonFeaturedProducts)
	productGroup.GET("/:id", h.GetProductByID)
	productGroup.GET("/category/:id", h.GetProductsByCategory)
	productGroup.GET("/user", auth, h.GetUserProducts)
	productGroup.POST("/", adminAuth, h.CreateProduct)
	productGroup.PUT("/:id", adminAuth, h.UpdateProduct)
	productGroup.DELETE("/:id", adminAuth, h.DeleteProduct)
	productGroup.PATCH("/:id/featured", adminAuth, h.ToggleFeatured)
}

// Category routes
categoryGroup := api.Group("/products/category")
{
	categoryGroup.GET("/all", h.GetAllCategories)
	categoryGroup.GET("/tree", h.GetCategoryTree)
	categoryGroup.GET("/:categoryId", h.GetCategoryByID)
	categoryGroup.POST("/", adminAuth, h.CreateCategory)
	categoryGroup.PUT("/:categoryId", adminAuth, h.UpdateCategory)
	categoryGroup.DELETE("/:categoryId", adminAuth, h.DeleteCategory)
	categoryGroup.PATCH("/reorder", adminAuth, h.ReorderCategories)
}
```

- [ ] **Step 2: Build to verify**

Run: `cd tsa-dev/tsa-api-go && go build ./...`
Expected: Build succeeds. If any handler functions don't exist yet, add stubs in `handlers/handlers.go`.

- [ ] **Step 3: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/routes/routes.go
git commit -m "feat: wire missing product and category routes"
```

---

### Task 3: Create Order Model & Handler

**Files:**
- Create: `tsa-dev/tsa-api-go/internal/models/order.go`
- Create: `tsa-dev/tsa-api-go/internal/handlers/order_handler.go`
- Modify: `tsa-dev/tsa-api-go/internal/routes/routes.go`
- Modify: `tsa-dev/tsa-api-go/internal/config/database.go` (add to AutoMigrate)

- [ ] **Step 1: Create Order model**

Create `tsa-dev/tsa-api-go/internal/models/order.go`:

```go
package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

type OrderItem struct {
	ProductID uuid.UUID `json:"productId"`
	Name      string    `json:"name"`
	Quantity  int       `json:"quantity"`
	UnitPrice float64   `json:"unitPrice"`
	Total     float64   `json:"total"`
}

type Order struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	BuyerID         uuid.UUID      `gorm:"type:uuid;index" json:"buyerId"`
	SellerID        uuid.UUID      `gorm:"type:uuid;index" json:"sellerId"`
	Items           datatypes.JSON `gorm:"type:jsonb" json:"items"`
	Total           float64        `json:"total"`
	Currency        string         `gorm:"default:'NGN'" json:"currency"`
	Status          string         `gorm:"default:'pending'" json:"status"`
	ShippingAddress string         `json:"shippingAddress,omitempty"`
	Notes           string         `json:"notes,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

func (Order) TableName() string {
	return "orders"
}

const (
	OrderStatusPending    = "pending"
	OrderStatusProcessing = "processing"
	OrderStatusShipped    = "shipped"
	OrderStatusCompleted  = "completed"
	OrderStatusCancelled  = "cancelled"
)

// ValidNextStatuses returns valid transitions from the current status.
func ValidNextStatuses(current string) []string {
	switch current {
	case OrderStatusPending:
		return []string{OrderStatusProcessing, OrderStatusCancelled}
	case OrderStatusProcessing:
		return []string{OrderStatusShipped, OrderStatusCancelled}
	case OrderStatusShipped:
		return []string{OrderStatusCompleted, OrderStatusCancelled}
	case OrderStatusCompleted:
		return []string{}
	case OrderStatusCancelled:
		return []string{}
	default:
		return []string{}
	}
}

func (o *Order) GetItems() []OrderItem {
	var items []OrderItem
	if o.Items != nil {
		_ = json.Unmarshal(o.Items, &items)
	}
	return items
}

func (o *Order) SetItems(items []OrderItem) {
	data, _ := json.Marshal(items)
	o.Items = data
}
```

- [ ] **Step 2: Add Order to AutoMigrate**

In `tsa-dev/tsa-api-go/internal/config/database.go`, add `&models.Order{}` to the `AutoMigrate` call.

- [ ] **Step 3: Create order handler**

Create `tsa-dev/tsa-api-go/internal/handlers/order_handler.go`:

```go
package handlers

import (
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// GetOrders handles GET /api/orders - admin paginated order list.
func (h *Handlers) GetOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")
	search := c.Query("search")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Model(&models.Order{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if search != "" {
		query = query.Where("id::text ILIKE ?", "%"+search+"%")
	}

	var total int64
	query.Count(&total)

	var orders []models.Order
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch orders")
		return
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	utils.SuccessResponse(c, http.StatusOK, "Orders fetched successfully", gin.H{
		"orders": orders,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

// GetOrderByID handles GET /api/orders/:id - admin order detail.
func (h *Handlers) GetOrderByID(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Order fetched successfully", order)
}

// UpdateOrderStatus handles PATCH /api/orders/:id/status.
func (h *Handlers) UpdateOrderStatus(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var body struct {
		Status string `json:"status" binding:"required"`
		Notes  string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Status is required")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	// Validate state transition
	validNext := models.ValidNextStatuses(order.Status)
	allowed := false
	for _, s := range validNext {
		if s == body.Status {
			allowed = true
			break
		}
	}
	if !allowed {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid status transition from "+order.Status+" to "+body.Status)
		return
	}

	updates := map[string]interface{}{"status": body.Status}
	if body.Notes != "" {
		updates["notes"] = body.Notes
	}

	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order status")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Order status updated", gin.H{
		"orderId": order.ID,
		"status":  body.Status,
	})
}
```

- [ ] **Step 4: Add order routes**

In `tsa-dev/tsa-api-go/internal/routes/routes.go`, add:

```go
// Order routes (admin)
orderGroup := api.Group("/orders")
orderGroup.Use(adminAuth)
{
	orderGroup.GET("/", h.GetOrders)
	orderGroup.GET("/:id", h.GetOrderByID)
	orderGroup.PATCH("/:id/status", h.UpdateOrderStatus)
}
```

- [ ] **Step 5: Build to verify**

Run: `cd tsa-dev/tsa-api-go && go build ./...`
Expected: Build succeeds.

- [ ] **Step 6: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/models/order.go internal/handlers/order_handler.go internal/routes/routes.go internal/config/database.go
git commit -m "feat: add order model, handler, and routes for admin"
```

---

### Task 4: Create Deposit Model & Handler

**Files:**
- Create: `tsa-dev/tsa-api-go/internal/models/deposit.go`
- Create: `tsa-dev/tsa-api-go/internal/handlers/deposit_handler.go`
- Modify: `tsa-dev/tsa-api-go/internal/routes/routes.go`
- Modify: `tsa-dev/tsa-api-go/internal/config/database.go`

- [ ] **Step 1: Create Deposit model**

Create `tsa-dev/tsa-api-go/internal/models/deposit.go`:

```go
package models

import (
	"time"

	"github.com/google/uuid"
)

type Deposit struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID         uuid.UUID  `gorm:"type:uuid;index" json:"userId"`
	Amount         float64    `json:"amount"`
	Currency       string     `gorm:"default:'NGN'" json:"currency"`
	Status         string     `gorm:"default:'pending'" json:"status"`
	ProofURL       string     `json:"proofUrl,omitempty"`
	AdminNote      string     `json:"adminNote,omitempty"`
	ReviewedBy     *uuid.UUID `gorm:"type:uuid" json:"reviewedBy,omitempty"`
	ReviewedAt     *time.Time `json:"reviewedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

func (Deposit) TableName() string {
	return "deposits"
}

const (
	DepositStatusPending  = "pending"
	DepositStatusApproved = "approved"
	DepositStatusRejected = "rejected"
)
```

- [ ] **Step 2: Add Deposit to AutoMigrate**

In `tsa-dev/tsa-api-go/internal/config/database.go`, add `&models.Deposit{}` to the `AutoMigrate` call.

- [ ] **Step 3: Create deposit handler**

Create `tsa-dev/tsa-api-go/internal/handlers/deposit_handler.go`:

```go
package handlers

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// GetDeposits handles GET /api/deposits - admin paginated deposit list.
func (h *Handlers) GetDeposits(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Model(&models.Deposit{})
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var deposits []models.Deposit
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&deposits).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch deposits")
		return
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	utils.SuccessResponse(c, http.StatusOK, "Deposits fetched successfully", gin.H{
		"deposits": deposits,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

// UpdateDepositStatus handles PATCH /api/deposits/:id/status.
func (h *Handlers) UpdateDepositStatus(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid deposit ID")
		return
	}

	var body struct {
		Status string `json:"status" binding:"required"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Status is required")
		return
	}

	if body.Status != models.DepositStatusApproved && body.Status != models.DepositStatusRejected {
		utils.ErrorResponse(c, http.StatusBadRequest, "Status must be 'approved' or 'rejected'")
		return
	}

	var deposit models.Deposit
	if err := config.DB.First(&deposit, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Deposit not found")
		return
	}

	if deposit.Status != models.DepositStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Deposit has already been reviewed")
		return
	}

	user := getUserFromContext(c)
	now := time.Now()

	updates := map[string]interface{}{
		"status":      body.Status,
		"reviewed_by": user.ID,
		"reviewed_at": now,
	}
	if body.Note != "" {
		updates["admin_note"] = body.Note
	}

	if err := config.DB.Model(&deposit).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update deposit")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Deposit "+body.Status+" successfully", gin.H{
		"depositId": deposit.ID,
		"status":    body.Status,
	})
}
```

- [ ] **Step 4: Add deposit routes**

In `tsa-dev/tsa-api-go/internal/routes/routes.go`, add:

```go
// Deposit routes (admin)
depositGroup := api.Group("/deposits")
depositGroup.Use(adminAuth)
{
	depositGroup.GET("/", h.GetDeposits)
	depositGroup.PATCH("/:id/status", h.UpdateDepositStatus)
}
```

- [ ] **Step 5: Build to verify**

Run: `cd tsa-dev/tsa-api-go && go build ./...`
Expected: Build succeeds.

- [ ] **Step 6: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/models/deposit.go internal/handlers/deposit_handler.go internal/routes/routes.go internal/config/database.go
git commit -m "feat: add deposit model, handler, and routes for admin"
```

---

### Task 5: Admin Stats & User Role Endpoints

**Files:**
- Create: `tsa-dev/tsa-api-go/internal/handlers/admin_handler.go`
- Modify: `tsa-dev/tsa-api-go/internal/handlers/user_handler.go`
- Modify: `tsa-dev/tsa-api-go/internal/routes/routes.go`

- [ ] **Step 1: Create admin stats handler**

Create `tsa-dev/tsa-api-go/internal/handlers/admin_handler.go`:

```go
package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// GetAdminStats handles GET /api/admin/stats.
func (h *Handlers) GetAdminStats(c *gin.Context) {
	var totalUsers int64
	config.DB.Model(&models.User{}).Count(&totalUsers)

	var totalProducts int64
	config.DB.Model(&models.Product{}).Count(&totalProducts)

	var pendingVerifications int64
	config.DB.Model(&models.User{}).Where("verification_status = ?", models.VerificationStatusPending).Count(&pendingVerifications)

	var pendingDeposits int64
	config.DB.Model(&models.Deposit{}).Where("status = ?", models.DepositStatusPending).Count(&pendingDeposits)

	var pendingAdverts int64
	config.DB.Model(&models.Product{}).Where("is_featured = ?", false).Count(&pendingAdverts)

	var totalOrders int64
	config.DB.Model(&models.Order{}).Count(&totalOrders)

	utils.SuccessResponse(c, http.StatusOK, "Admin stats fetched", gin.H{
		"totalUsers":           totalUsers,
		"totalProducts":        totalProducts,
		"pendingVerifications": pendingVerifications,
		"pendingDeposits":      pendingDeposits,
		"pendingAdverts":       pendingAdverts,
		"totalOrders":          totalOrders,
	})
}
```

- [ ] **Step 2: Add UpdateRole handler to user_handler.go**

Add to `tsa-dev/tsa-api-go/internal/handlers/user_handler.go`:

```go
// UpdateUserRole handles PATCH /api/users/:id/role - super admin only.
func (h *Handlers) UpdateUserRole(c *gin.Context) {
	// Check caller is super_admin
	caller := getUserFromContext(c)
	if caller == nil || caller.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Only super admins can change roles")
		return
	}

	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid user ID")
		return
	}

	var body struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Role is required")
		return
	}

	validRoles := map[string]bool{
		models.RoleUser:       true,
		models.RoleAdmin:      true,
		models.RoleSuperAdmin: true,
		models.RoleMerchant:   true,
		models.RoleSupport:    true,
	}
	if !validRoles[body.Role] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid role")
		return
	}

	var user models.User
	if err := config.DB.First(&user, "id = ?", userID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "User not found")
		return
	}

	if err := config.DB.Model(&user).Update("role", body.Role).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update role")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Role updated successfully", gin.H{
		"userId": user.ID,
		"role":   body.Role,
	})
}
```

- [ ] **Step 3: Add admin and user role routes**

In `tsa-dev/tsa-api-go/internal/routes/routes.go`, add:

```go
// Admin routes
adminGroup := api.Group("/admin")
adminGroup.Use(adminAuth)
{
	adminGroup.GET("/stats", h.GetAdminStats)
}

// Add to existing userGroup:
userGroup.PATCH("/:id/role", h.UpdateUserRole)
```

- [ ] **Step 4: Build to verify**

Run: `cd tsa-dev/tsa-api-go && go build ./...`
Expected: Build succeeds.

- [ ] **Step 5: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/handlers/admin_handler.go internal/handlers/user_handler.go internal/routes/routes.go
git commit -m "feat: add admin stats endpoint and user role update"
```

---

## Chunk 2: Frontend Scaffold & Auth

### Task 6: Scaffold Vite + React + Shadcn Project

**Files:**
- Create: `tsa-dev/tsa-admin/` (entire project scaffold)

- [ ] **Step 1: Create Vite project**

```bash
cd tsa-dev
npm create vite@latest tsa-admin -- --template react-ts
cd tsa-admin
npm install
```

- [ ] **Step 2: Install dependencies**

```bash
npm install axios @tanstack/react-query react-router-dom@7 lucide-react sonner
npm install -D tailwindcss @tailwindcss/vite
```

- [ ] **Step 3: Set up Tailwind**

Replace `tsa-dev/tsa-admin/src/index.css` with:

```css
@import "tailwindcss";
```

Add Tailwind plugin to `tsa-dev/tsa-admin/vite.config.ts`:

```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
})
```

Update `tsa-dev/tsa-admin/tsconfig.json` to add path alias:

```json
{
  "compilerOptions": {
    "baseUrl": ".",
    "paths": {
      "@/*": ["./src/*"]
    }
  }
}
```

- [ ] **Step 4: Initialize Shadcn**

```bash
cd tsa-dev/tsa-admin
npx shadcn@latest init
```

Select: New York style, Slate base color, CSS variables enabled.

- [ ] **Step 5: Add core Shadcn components**

```bash
npx shadcn@latest add button input label card table badge dialog dropdown-menu separator skeleton toast sonner avatar tooltip sheet scroll-area select
```

- [ ] **Step 6: Verify dev server starts**

```bash
cd tsa-dev/tsa-admin
npm run dev
```

Expected: Dev server starts at `http://localhost:5173` with default Vite page.

- [ ] **Step 7: Commit**

```bash
cd tsa-dev/tsa-admin
git add .
git commit -m "feat: scaffold tsa-admin with Vite, React, Shadcn, Tailwind"
```

---

### Task 7: Types, Constants & API Client

**Files:**
- Create: `tsa-dev/tsa-admin/src/types/index.ts`
- Create: `tsa-dev/tsa-admin/src/lib/constants.ts`
- Create: `tsa-dev/tsa-admin/src/lib/permissions.ts`
- Create: `tsa-dev/tsa-admin/src/api/client.ts`
- Create: `tsa-dev/tsa-admin/src/api/auth.ts`

- [ ] **Step 1: Create TypeScript types**

Create `tsa-dev/tsa-admin/src/types/index.ts`:

```ts
export type Role = 'super_admin' | 'admin' | 'support' | 'user' | 'merchant';

export interface User {
  id: string;
  name: string;
  username: string;
  email: string;
  role: Role;
  phoneNumber: string;
  country: string;
  state?: string;
  city?: string;
  address: string;
  profilePhoto?: { url: string; publicId: string };
  referralCode?: string;
  verificationStatus: string;
  accountStatus: string;
  lastLogin?: string;
  walletAddress?: string;
  createdAt: string;
  updatedAt: string;
}

export interface Product {
  id: string;
  userId: string;
  name: string;
  description: string;
  price: number;
  stock: number;
  category?: string;
  categoryName?: string;
  images?: { id: string; url: string; publicId?: string; order: number }[];
  status: 'active' | 'inactive' | 'sold_out' | 'pending_review';
  type: 'Product' | 'Service';
  isFeatured: boolean;
  views: number;
  sales: number;
  createdAt: string;
  updatedAt: string;
}

export interface Category {
  id: string;
  title: string;
  description?: string;
  type: 'Product' | 'Service' | 'Both';
  parentCategory?: string;
  icon?: string;
  color: string;
  isActive: boolean;
  order: number;
  productCount: number;
  children?: Category[];
  createdAt?: string;
  updatedAt?: string;
}

export interface OrderItem {
  productId: string;
  name: string;
  quantity: number;
  unitPrice: number;
  total: number;
}

export interface Order {
  id: string;
  buyerId: string;
  sellerId: string;
  items: OrderItem[];
  total: number;
  currency: string;
  status: 'pending' | 'processing' | 'shipped' | 'completed' | 'cancelled';
  shippingAddress?: string;
  notes?: string;
  createdAt: string;
  updatedAt: string;
}

export interface Deposit {
  id: string;
  userId: string;
  amount: number;
  currency: string;
  status: 'pending' | 'approved' | 'rejected';
  proofUrl?: string;
  adminNote?: string;
  reviewedBy?: string;
  reviewedAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface AdminStats {
  totalUsers: number;
  totalProducts: number;
  pendingVerifications: number;
  pendingDeposits: number;
  pendingAdverts: number;
  totalOrders: number;
}

export interface PaginationMeta {
  page: number;
  limit: number;
  total: number;
  totalPages: number;
}

export interface ApiResponse<T = unknown> {
  success: boolean;
  message?: string;
  data?: T;
  errors?: { field: string; message: string }[];
}

export interface PaginatedResponse<T> {
  success: boolean;
  message?: string;
  data?: {
    pagination: PaginationMeta;
  } & Record<string, T[] | PaginationMeta>;
}
```

- [ ] **Step 2: Create constants**

Create `tsa-dev/tsa-admin/src/lib/constants.ts`:

```ts
export const ORDER_STATUS_LABELS: Record<string, string> = {
  pending: 'Pending',
  processing: 'Processing',
  shipped: 'Shipped',
  completed: 'Completed',
  cancelled: 'Cancelled',
};

export const ORDER_STATUS_COLORS: Record<string, string> = {
  pending: 'bg-amber-100 text-amber-700',
  processing: 'bg-blue-100 text-blue-700',
  shipped: 'bg-purple-100 text-purple-700',
  completed: 'bg-green-100 text-green-700',
  cancelled: 'bg-red-100 text-red-700',
};

export const DEPOSIT_STATUS_COLORS: Record<string, string> = {
  pending: 'bg-amber-100 text-amber-700',
  approved: 'bg-green-100 text-green-700',
  rejected: 'bg-red-100 text-red-700',
};

export const VERIFICATION_STATUS_COLORS: Record<string, string> = {
  pending: 'bg-amber-100 text-amber-700',
  in_review: 'bg-blue-100 text-blue-700',
  verified: 'bg-green-100 text-green-700',
  rejected: 'bg-red-100 text-red-700',
};

export const ROLE_LABELS: Record<string, string> = {
  super_admin: 'Super Admin',
  admin: 'Admin',
  support: 'Support',
  user: 'User',
  merchant: 'Merchant',
};
```

- [ ] **Step 3: Create permissions map**

Create `tsa-dev/tsa-admin/src/lib/permissions.ts`:

```ts
import type { Role } from '@/types';

export type Permission =
  | 'dashboard.view'
  | 'users.view'
  | 'users.edit'
  | 'users.delete'
  | 'users.manage_roles'
  | 'products.view'
  | 'products.approve'
  | 'products.edit'
  | 'products.delete'
  | 'categories.view'
  | 'categories.create'
  | 'categories.edit'
  | 'categories.delete'
  | 'orders.view'
  | 'orders.update_status'
  | 'deposits.view'
  | 'deposits.approve'
  | 'verifications.view'
  | 'verifications.approve'
  | 'settings.view';

const ROLE_PERMISSIONS: Record<string, Permission[]> = {
  super_admin: [
    'dashboard.view', 'users.view', 'users.edit', 'users.delete', 'users.manage_roles',
    'products.view', 'products.approve', 'products.edit', 'products.delete',
    'categories.view', 'categories.create', 'categories.edit', 'categories.delete',
    'orders.view', 'orders.update_status',
    'deposits.view', 'deposits.approve',
    'verifications.view', 'verifications.approve',
    'settings.view',
  ],
  admin: [
    'dashboard.view', 'users.view', 'users.edit',
    'products.view', 'products.approve', 'products.edit', 'products.delete',
    'categories.view', 'categories.create', 'categories.edit',
    'orders.view', 'orders.update_status',
    'deposits.view', 'deposits.approve',
    'verifications.view', 'verifications.approve',
  ],
  support: [
    'dashboard.view', 'users.view',
    'products.view',
    'categories.view',
    'orders.view', 'orders.update_status',
    'deposits.view',
    'verifications.view',
  ],
};

export function hasPermission(role: Role, permission: Permission): boolean {
  const permissions = ROLE_PERMISSIONS[role];
  if (!permissions) return false;
  return permissions.includes(permission);
}

export function canAccessRoute(role: Role, path: string): boolean {
  const routePermissions: Record<string, Permission> = {
    '/dashboard': 'dashboard.view',
    '/users': 'users.view',
    '/products': 'products.view',
    '/categories': 'categories.view',
    '/orders': 'orders.view',
    '/advert-requests': 'products.view',
    '/deposits': 'deposits.view',
    '/verifications': 'verifications.view',
    '/settings': 'settings.view',
  };

  const permission = routePermissions[path];
  if (!permission) return true;
  return hasPermission(role, permission);
}
```

- [ ] **Step 4: Create API client**

Create `tsa-dev/tsa-admin/src/api/client.ts`:

```ts
import axios from 'axios';
import { toast } from 'sonner';

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL || 'http://localhost:5000/api';

const client = axios.create({
  baseURL: API_BASE_URL,
  headers: { 'Content-Type': 'application/json' },
});

client.interceptors.request.use((config) => {
  const token =
    sessionStorage.getItem('authToken') ||
    localStorage.getItem('authToken');
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

client.interceptors.response.use(
  (response) => response,
  (error) => {
    if (!error.response) {
      toast.error('Network error. Check your connection.');
      return Promise.reject(error);
    }

    const { status, data } = error.response;

    switch (status) {
      case 401:
        sessionStorage.removeItem('authToken');
        localStorage.removeItem('authToken');
        sessionStorage.removeItem('user');
        window.location.href = '/login';
        break;
      case 403:
        toast.error("You don't have permission to perform this action");
        break;
      case 422:
        if (!data?.errors) {
          toast.error(data?.message || 'Validation error');
        }
        break;
      case 429:
        toast.error('Too many requests. Please wait.');
        break;
      default:
        if (status >= 500) {
          toast.error('Something went wrong. Please try again.');
        }
    }

    return Promise.reject(error);
  }
);

export default client;
```

- [ ] **Step 5: Create auth API module**

Create `tsa-dev/tsa-admin/src/api/auth.ts`:

```ts
import client from './client';
import type { ApiResponse, User } from '@/types';

export async function login(email: string, password: string) {
  const { data } = await client.post<ApiResponse<{ token: string; userId: string; role: string }>>('/auth/login', { email, password });
  return data;
}

export async function getProfile() {
  const { data } = await client.get<ApiResponse<User>>('/users/profile');
  return data;
}
```

- [ ] **Step 6: Create .env.example and .env files**

Create `tsa-dev/tsa-admin/.env.example` (committed):

```
VITE_API_BASE_URL=http://localhost:5000/api
```

Copy to `.env` (gitignored):

```bash
cp .env.example .env
```

Add `.env` to `tsa-dev/tsa-admin/.gitignore`.

- [ ] **Step 7: Commit**

```bash
cd tsa-dev/tsa-admin
git add src/types/ src/lib/ src/api/ .env.example .gitignore
git commit -m "feat: add types, constants, permissions, and API client"
```

---

### Task 8: Auth Context & Login Page

**Files:**
- Create: `tsa-dev/tsa-admin/src/contexts/auth-context.tsx`
- Create: `tsa-dev/tsa-admin/src/hooks/use-auth.ts`
- Create: `tsa-dev/tsa-admin/src/hooks/use-permission.ts`
- Create: `tsa-dev/tsa-admin/src/pages/auth/login.tsx`

- [ ] **Step 1: Create AuthContext**

Create `tsa-dev/tsa-admin/src/contexts/auth-context.tsx`:

```tsx
import { createContext, useState, useEffect, type ReactNode } from 'react';
import { getProfile } from '@/api/auth';
import type { User } from '@/types';

interface AuthState {
  user: User | null;
  token: string | null;
  isLoading: boolean;
  login: (token: string, rememberMe: boolean) => Promise<void>;
  logout: () => void;
}

export const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [token, setToken] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    const stored = sessionStorage.getItem('authToken') || localStorage.getItem('authToken');
    if (stored) {
      setToken(stored);
      getProfile()
        .then((res) => {
          if (res.success && res.data) {
            setUser(res.data);
          }
        })
        .catch(() => {
          sessionStorage.removeItem('authToken');
          localStorage.removeItem('authToken');
        })
        .finally(() => setIsLoading(false));
    } else {
      setIsLoading(false);
    }
  }, []);

  const login = async (newToken: string, rememberMe: boolean) => {
    if (rememberMe) {
      localStorage.setItem('authToken', newToken);
    } else {
      sessionStorage.setItem('authToken', newToken);
    }
    setToken(newToken);

    const res = await getProfile();
    if (res.success && res.data) {
      const allowedRoles = ['super_admin', 'admin', 'support'];
      if (!allowedRoles.includes(res.data.role)) {
        sessionStorage.removeItem('authToken');
        localStorage.removeItem('authToken');
        throw new Error('Access denied. Admin, support, or super admin role required.');
      }
      setUser(res.data);
    }
  };

  const logout = () => {
    sessionStorage.removeItem('authToken');
    localStorage.removeItem('authToken');
    sessionStorage.removeItem('user');
    setUser(null);
    setToken(null);
  };

  return (
    <AuthContext.Provider value={{ user, token, isLoading, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
}
```

- [ ] **Step 2: Create hooks**

Create `tsa-dev/tsa-admin/src/hooks/use-auth.ts`:

```ts
import { useContext } from 'react';
import { AuthContext } from '@/contexts/auth-context';

export function useAuth() {
  const context = useContext(AuthContext);
  if (!context) throw new Error('useAuth must be used within AuthProvider');
  return context;
}
```

Create `tsa-dev/tsa-admin/src/hooks/use-permission.ts`:

```ts
import { useAuth } from './use-auth';
import { hasPermission, type Permission } from '@/lib/permissions';

export function usePermission(permission: Permission): boolean {
  const { user } = useAuth();
  if (!user) return false;
  return hasPermission(user.role, permission);
}
```

- [ ] **Step 3: Create Login page**

Create `tsa-dev/tsa-admin/src/pages/auth/login.tsx`:

```tsx
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card';
import { useAuth } from '@/hooks/use-auth';
import { login as apiLogin } from '@/api/auth';
import { toast } from 'sonner';

export default function LoginPage() {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [rememberMe, setRememberMe] = useState(false);
  const [loading, setLoading] = useState(false);
  const { login } = useAuth();
  const navigate = useNavigate();

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);

    try {
      const res = await apiLogin(email, password);
      if (res.success && res.data) {
        await login(res.data.token, rememberMe);
        navigate('/dashboard', { replace: true });
      } else {
        toast.error(res.message || 'Login failed');
      }
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Login failed';
      toast.error(message);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="min-h-screen flex items-center justify-center bg-slate-50">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-lg bg-blue-600 text-white font-bold text-xl">
            T
          </div>
          <CardTitle className="text-2xl">TSA Admin</CardTitle>
          <CardDescription>Sign in to your admin account</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="email">Email</Label>
              <Input id="email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="admin@example.com" required />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password</Label>
              <Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
            </div>
            <div className="flex items-center gap-2">
              <input type="checkbox" id="remember" checked={rememberMe} onChange={(e) => setRememberMe(e.target.checked)} className="rounded border-slate-300" />
              <Label htmlFor="remember" className="text-sm font-normal">Remember me</Label>
            </div>
            <Button type="submit" className="w-full" disabled={loading}>
              {loading ? 'Signing in...' : 'Sign in'}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
```

- [ ] **Step 4: Commit**

```bash
cd tsa-dev/tsa-admin
git add src/contexts/ src/hooks/ src/pages/auth/
git commit -m "feat: add auth context, hooks, and login page"
```

---

### Task 9: Layout Shell & Router

**Files:**
- Create: `tsa-dev/tsa-admin/src/components/layout/sidebar.tsx`
- Create: `tsa-dev/tsa-admin/src/components/layout/header.tsx`
- Create: `tsa-dev/tsa-admin/src/components/layout/app-layout.tsx`
- Modify: `tsa-dev/tsa-admin/src/app.tsx` (add routes)
- Create: `tsa-dev/tsa-admin/src/app.tsx`
- Modify: `tsa-dev/tsa-admin/src/main.tsx`

- [ ] **Step 1: Create Sidebar**

Create `tsa-dev/tsa-admin/src/components/layout/sidebar.tsx`:

```tsx
import { NavLink, useLocation } from 'react-router-dom';
import { cn } from '@/lib/utils';
import { useAuth } from '@/hooks/use-auth';
import { canAccessRoute } from '@/lib/permissions';
import {
  LayoutDashboard, Users, Package, FolderTree, ShoppingCart,
  Megaphone, Wallet, ShieldCheck, Settings, ChevronLeft, LogOut,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Separator } from '@/components/ui/separator';
import { ROLE_LABELS } from '@/lib/constants';

interface SidebarProps {
  collapsed: boolean;
  onToggle: () => void;
}

const navGroups = [
  {
    label: 'Overview',
    items: [
      { path: '/dashboard', label: 'Dashboard', icon: LayoutDashboard },
    ],
  },
  {
    label: 'Management',
    items: [
      { path: '/users', label: 'Users', icon: Users },
      { path: '/products', label: 'Products', icon: Package },
      { path: '/categories', label: 'Categories', icon: FolderTree },
      { path: '/orders', label: 'Orders', icon: ShoppingCart },
    ],
  },
  {
    label: 'Approvals',
    items: [
      { path: '/advert-requests', label: 'Advert Requests', icon: Megaphone },
      { path: '/deposits', label: 'Deposits', icon: Wallet },
      { path: '/verifications', label: 'Verifications', icon: ShieldCheck },
    ],
  },
  {
    label: 'System',
    items: [
      { path: '/settings', label: 'Settings', icon: Settings },
    ],
  },
];

export function Sidebar({ collapsed, onToggle }: SidebarProps) {
  const { user, logout } = useAuth();
  const location = useLocation();

  return (
    <aside
      className={cn(
        'flex flex-col border-r border-slate-200 bg-white transition-all duration-200',
        collapsed ? 'w-16' : 'w-60'
      )}
    >
      {/* Logo */}
      <div className="flex h-14 items-center justify-between border-b border-slate-200 px-3">
        {!collapsed && (
          <div className="flex items-center gap-2">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-blue-600 text-white font-bold text-sm">T</div>
            <span className="font-semibold text-slate-900">TSA Admin</span>
          </div>
        )}
        <Button variant="ghost" size="icon" onClick={onToggle} className="h-8 w-8">
          <ChevronLeft className={cn('h-4 w-4 transition-transform', collapsed && 'rotate-180')} />
        </Button>
      </div>

      {/* Nav */}
      <nav className="flex-1 overflow-y-auto p-2">
        {navGroups.map((group) => {
          const visibleItems = group.items.filter(
            (item) => user && canAccessRoute(user.role, item.path)
          );
          if (visibleItems.length === 0) return null;

          return (
            <div key={group.label} className="mb-4">
              {!collapsed && (
                <p className="mb-1 px-3 text-[10px] font-medium uppercase tracking-wider text-slate-400">
                  {group.label}
                </p>
              )}
              {visibleItems.map((item) => (
                <NavLink
                  key={item.path}
                  to={item.path}
                  className={({ isActive }) =>
                    cn(
                      'flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors',
                      isActive
                        ? 'bg-blue-50 text-blue-600'
                        : 'text-slate-600 hover:bg-slate-50 hover:text-slate-900',
                      collapsed && 'justify-center px-2'
                    )
                  }
                >
                  <item.icon className="h-4 w-4 shrink-0" />
                  {!collapsed && <span>{item.label}</span>}
                </NavLink>
              ))}
            </div>
          );
        })}
      </nav>

      {/* User section */}
      <div className="border-t border-slate-200 p-3">
        {!collapsed && user && (
          <div className="mb-2 flex items-center gap-2">
            <div className="flex h-8 w-8 items-center justify-center rounded-full bg-blue-600 text-xs font-semibold text-white">
              {user.name.charAt(0).toUpperCase()}
            </div>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium text-slate-900">{user.name}</p>
              <p className="text-[10px] text-slate-400">{ROLE_LABELS[user.role] || user.role}</p>
            </div>
          </div>
        )}
        <Button variant="ghost" size={collapsed ? 'icon' : 'sm'} onClick={logout} className="w-full justify-start text-slate-600">
          <LogOut className="h-4 w-4" />
          {!collapsed && <span className="ml-2">Logout</span>}
        </Button>
      </div>
    </aside>
  );
}
```

- [ ] **Step 2: Create Header**

Create `tsa-dev/tsa-admin/src/components/layout/header.tsx`:

```tsx
import { useLocation } from 'react-router-dom';
import { Input } from '@/components/ui/input';
import { Bell, Search } from 'lucide-react';
import { Button } from '@/components/ui/button';

const PAGE_TITLES: Record<string, { title: string; breadcrumb: string }> = {
  '/dashboard': { title: 'Dashboard', breadcrumb: 'Overview' },
  '/users': { title: 'Users', breadcrumb: 'Management' },
  '/products': { title: 'Products', breadcrumb: 'Management' },
  '/categories': { title: 'Categories', breadcrumb: 'Management' },
  '/orders': { title: 'Orders', breadcrumb: 'Management' },
  '/advert-requests': { title: 'Advert Requests', breadcrumb: 'Approvals' },
  '/deposits': { title: 'Deposits', breadcrumb: 'Approvals' },
  '/verifications': { title: 'Verifications', breadcrumb: 'Approvals' },
  '/settings': { title: 'Settings', breadcrumb: 'System' },
};

export function Header() {
  const location = useLocation();
  const pageInfo = PAGE_TITLES[location.pathname] || { title: 'Page', breadcrumb: '' };

  return (
    <header className="flex h-14 items-center justify-between border-b border-slate-200 bg-white px-6">
      <div>
        <p className="text-[11px] text-slate-400">{pageInfo.breadcrumb}</p>
        <h1 className="text-base font-semibold text-slate-900">{pageInfo.title}</h1>
      </div>
      <div className="flex items-center gap-3">
        <div className="relative">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-slate-400" />
          <Input placeholder="Search..." className="w-52 pl-9 text-sm" disabled />
        </div>
        <Button variant="ghost" size="icon" className="relative" disabled>
          <Bell className="h-4 w-4 text-slate-500" />
        </Button>
      </div>
    </header>
  );
}
```

- [ ] **Step 3: Create AppLayout**

Create `tsa-dev/tsa-admin/src/components/layout/app-layout.tsx`:

```tsx
import { useState } from 'react';
import { Outlet } from 'react-router-dom';
import { Sidebar } from './sidebar';
import { Header } from './header';

export function AppLayout() {
  const [collapsed, setCollapsed] = useState(false);

  return (
    <div className="flex h-screen bg-slate-50">
      <Sidebar collapsed={collapsed} onToggle={() => setCollapsed(!collapsed)} />
      <div className="flex flex-1 flex-col min-w-0">
        <Header />
        <main className="flex-1 overflow-y-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Skip — Router is defined inline in app.tsx**

Routes are added directly in `app.tsx` using `<BrowserRouter>` + `<Routes>` (see Task 9, Step 6). No separate `router.tsx` file needed. When adding new pages, add `<Route>` elements inside the protected layout route in `app.tsx`.

- [ ] **Step 5: Create placeholder Dashboard page**

Create `tsa-dev/tsa-admin/src/pages/dashboard/index.tsx`:

```tsx
export default function DashboardPage() {
  return (
    <div>
      <h2 className="text-lg font-semibold text-slate-900">Dashboard</h2>
      <p className="text-sm text-slate-500">Welcome to the admin panel.</p>
    </div>
  );
}
```

- [ ] **Step 6: Create App root component**

Create `tsa-dev/tsa-admin/src/app.tsx`:

```tsx
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Toaster } from 'sonner';
import { AuthProvider } from '@/contexts/auth-context';
import { AppLayout } from '@/components/layout/app-layout';
import LoginPage from '@/pages/auth/login';
import DashboardPage from '@/pages/dashboard/index';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      staleTime: 30 * 1000,
    },
  },
});

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const token = sessionStorage.getItem('authToken') || localStorage.getItem('authToken');
  if (!token) return <Navigate to="/login" replace />;
  return <>{children}</>;
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <BrowserRouter>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/" element={<ProtectedRoute><AppLayout /></ProtectedRoute>}>
              <Route index element={<Navigate to="/dashboard" replace />} />
              <Route path="dashboard" element={<DashboardPage />} />
              {/* Additional routes added as pages are implemented */}
            </Route>
          </Routes>
        </BrowserRouter>
        <Toaster position="top-right" richColors />
      </AuthProvider>
    </QueryClientProvider>
  );
}
```

This ensures `AuthProvider` wraps the entire router tree so `useAuth()` works in all components.

- [ ] **Step 7: Update main.tsx entry point**

Replace `tsa-dev/tsa-admin/src/main.tsx`:

```tsx
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './app';
import './index.css';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>
);
```

- [ ] **Step 8: Verify dev server renders login page**

```bash
cd tsa-dev/tsa-admin && npm run dev
```

Open `http://localhost:5173` — should redirect to `/login` and show the login form.

- [ ] **Step 9: Commit**

```bash
cd tsa-dev/tsa-admin
git add src/components/layout/ src/app.tsx src/main.tsx src/pages/dashboard/
git commit -m "feat: add layout shell, sidebar, header, router, and login flow"
```

---

## Chunk 3: Dashboard & Shared Components

### Task 10: Shared Components

**Files:**
- Create: `tsa-dev/tsa-admin/src/components/shared/stat-card.tsx`
- Create: `tsa-dev/tsa-admin/src/components/shared/status-badge.tsx`
- Create: `tsa-dev/tsa-admin/src/components/shared/page-header.tsx`
- Create: `tsa-dev/tsa-admin/src/components/shared/confirm-dialog.tsx`
- Create: `tsa-dev/tsa-admin/src/components/shared/data-table.tsx`

- [ ] **Step 1: Create StatCard**

Create `tsa-dev/tsa-admin/src/components/shared/stat-card.tsx`:

```tsx
import { Card, CardContent } from '@/components/ui/card';
import type { LucideIcon } from 'lucide-react';

interface StatCardProps {
  title: string;
  value: string | number;
  icon: LucideIcon;
  description?: string;
}

export function StatCard({ title, value, icon: Icon, description }: StatCardProps) {
  return (
    <Card>
      <CardContent className="p-6">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm text-slate-500">{title}</p>
            <p className="mt-1 text-2xl font-bold text-slate-900">{value}</p>
            {description && <p className="mt-1 text-xs text-slate-400">{description}</p>}
          </div>
          <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-blue-50">
            <Icon className="h-5 w-5 text-blue-600" />
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
```

- [ ] **Step 2: Create StatusBadge**

Create `tsa-dev/tsa-admin/src/components/shared/status-badge.tsx`:

```tsx
import { cn } from '@/lib/utils';

interface StatusBadgeProps {
  status: string;
  colorMap: Record<string, string>;
  labelMap?: Record<string, string>;
}

export function StatusBadge({ status, colorMap, labelMap }: StatusBadgeProps) {
  return (
    <span className={cn('inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium', colorMap[status] || 'bg-slate-100 text-slate-700')}>
      {labelMap?.[status] || status}
    </span>
  );
}
```

- [ ] **Step 3: Create PageHeader**

Create `tsa-dev/tsa-admin/src/components/shared/page-header.tsx`:

```tsx
import type { ReactNode } from 'react';

interface PageHeaderProps {
  title: string;
  description?: string;
  actions?: ReactNode;
}

export function PageHeader({ title, description, actions }: PageHeaderProps) {
  return (
    <div className="mb-6 flex items-center justify-between">
      <div>
        <h2 className="text-lg font-semibold text-slate-900">{title}</h2>
        {description && <p className="text-sm text-slate-500">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}
```

- [ ] **Step 4: Create ConfirmDialog**

Create `tsa-dev/tsa-admin/src/components/shared/confirm-dialog.tsx`:

```tsx
import { Button } from '@/components/ui/button';
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog';

interface ConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel?: string;
  variant?: 'default' | 'destructive';
  loading?: boolean;
  onConfirm: () => void;
}

export function ConfirmDialog({
  open, onOpenChange, title, description, confirmLabel = 'Confirm',
  variant = 'default', loading, onConfirm,
}: ConfirmDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={loading}>
            Cancel
          </Button>
          <Button variant={variant} onClick={onConfirm} disabled={loading}>
            {loading ? 'Please wait...' : confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
```

- [ ] **Step 5: Create DataTable**

Create `tsa-dev/tsa-admin/src/components/shared/data-table.tsx`:

```tsx
import {
  type ColumnDef, flexRender, getCoreRowModel, useReactTable,
} from '@tanstack/react-table';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { Button } from '@/components/ui/button';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { Skeleton } from '@/components/ui/skeleton';

interface DataTableProps<TData, TValue> {
  columns: ColumnDef<TData, TValue>[];
  data: TData[];
  page: number;
  totalPages: number;
  onPageChange: (page: number) => void;
  isLoading?: boolean;
  onRowClick?: (row: TData) => void;
}

export function DataTable<TData, TValue>({
  columns, data, page, totalPages, onPageChange, isLoading, onRowClick,
}: DataTableProps<TData, TValue>) {
  const table = useReactTable({ data, columns, getCoreRowModel: getCoreRowModel() });

  if (isLoading) {
    return (
      <div className="space-y-2">
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full" />
        ))}
      </div>
    );
  }

  return (
    <div>
      <div className="rounded-md border border-slate-200 bg-white">
        <Table>
          <TableHeader>
            {table.getHeaderGroups().map((headerGroup) => (
              <TableRow key={headerGroup.id}>
                {headerGroup.headers.map((header) => (
                  <TableHead key={header.id}>
                    {header.isPlaceholder ? null : flexRender(header.column.columnDef.header, header.getContext())}
                  </TableHead>
                ))}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {table.getRowModel().rows.length ? (
              table.getRowModel().rows.map((row) => (
                <TableRow
                  key={row.id}
                  className={onRowClick ? 'cursor-pointer hover:bg-slate-50' : ''}
                  onClick={() => onRowClick?.(row.original)}
                >
                  {row.getVisibleCells().map((cell) => (
                    <TableCell key={cell.id}>
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </TableCell>
                  ))}
                </TableRow>
              ))
            ) : (
              <TableRow>
                <TableCell colSpan={columns.length} className="h-24 text-center text-slate-500">
                  No results found.
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
      {totalPages > 1 && (
        <div className="flex items-center justify-between pt-4">
          <p className="text-sm text-slate-500">Page {page} of {totalPages}</p>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => onPageChange(page - 1)} disabled={page <= 1}>
              <ChevronLeft className="h-4 w-4" />
            </Button>
            <Button variant="outline" size="sm" onClick={() => onPageChange(page + 1)} disabled={page >= totalPages}>
              <ChevronRight className="h-4 w-4" />
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
```

Note: Install tanstack table: `npm install @tanstack/react-table`

- [ ] **Step 6: Commit**

```bash
cd tsa-dev/tsa-admin
npm install @tanstack/react-table
git add src/components/shared/ package.json package-lock.json
git commit -m "feat: add shared components — StatCard, StatusBadge, DataTable, ConfirmDialog, PageHeader"
```

---

### Task 11: Dashboard Page with Real Data

**Files:**
- Create: `tsa-dev/tsa-admin/src/api/stats.ts`
- Modify: `tsa-dev/tsa-admin/src/pages/dashboard/index.tsx`

- [ ] **Step 1: Create stats API module**

Create `tsa-dev/tsa-admin/src/api/stats.ts`:

```ts
import client from './client';
import type { ApiResponse, AdminStats } from '@/types';

export async function getDashboardStats() {
  const { data } = await client.get<ApiResponse<AdminStats>>('/admin/stats');
  return data;
}
```

- [ ] **Step 2: Build the Dashboard page**

Replace `tsa-dev/tsa-admin/src/pages/dashboard/index.tsx`:

```tsx
import { useQuery } from '@tanstack/react-query';
import { getDashboardStats } from '@/api/stats';
import { StatCard } from '@/components/shared/stat-card';
import { PageHeader } from '@/components/shared/page-header';
import { Users, Package, ShieldCheck, ShoppingCart, Megaphone, Wallet } from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { useNavigate } from 'react-router-dom';

export default function DashboardPage() {
  const navigate = useNavigate();
  const { data, isLoading } = useQuery({
    queryKey: ['admin-stats'],
    queryFn: getDashboardStats,
    refetchInterval: 60_000,
  });

  const stats = data?.data;

  const pendingActions = [
    { label: 'Advert Requests', count: stats?.pendingAdverts ?? 0, path: '/advert-requests', color: 'bg-red-50 text-red-600' },
    { label: 'Deposit Requests', count: stats?.pendingDeposits ?? 0, path: '/deposits', color: 'bg-amber-50 text-amber-600' },
    { label: 'Verifications', count: stats?.pendingVerifications ?? 0, path: '/verifications', color: 'bg-blue-50 text-blue-600' },
  ];

  return (
    <div>
      <PageHeader title="Dashboard" description="Overview of your marketplace" />

      {isLoading ? (
        <div className="grid grid-cols-4 gap-4 mb-6">
          {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-28" />)}
        </div>
      ) : (
        <div className="grid grid-cols-4 gap-4 mb-6">
          <StatCard title="Total Users" value={stats?.totalUsers ?? 0} icon={Users} />
          <StatCard title="Total Products" value={stats?.totalProducts ?? 0} icon={Package} />
          <StatCard title="Total Orders" value={stats?.totalOrders ?? 0} icon={ShoppingCart} />
          <StatCard title="Pending Approvals" value={(stats?.pendingVerifications ?? 0) + (stats?.pendingDeposits ?? 0) + (stats?.pendingAdverts ?? 0)} icon={ShieldCheck} description="Needs attention" />
        </div>
      )}

      <div className="grid grid-cols-3 gap-4">
        {pendingActions.map((action) => (
          <Card
            key={action.label}
            className="cursor-pointer hover:shadow-md transition-shadow"
            onClick={() => navigate(action.path)}
          >
            <CardContent className="flex items-center justify-between p-6">
              <span className="text-sm font-medium text-slate-700">{action.label}</span>
              <span className={`inline-flex h-8 w-8 items-center justify-center rounded-full text-sm font-bold ${action.color}`}>
                {action.count}
              </span>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  );
}
```

- [ ] **Step 3: Commit**

```bash
cd tsa-dev/tsa-admin
git add src/api/stats.ts src/pages/dashboard/
git commit -m "feat: add dashboard page with live stats from API"
```

---

## Chunk 4: Management Pages

### Task 12: Users Page

**Files:**
- Create: `tsa-dev/tsa-admin/src/api/users.ts`
- Create: `tsa-dev/tsa-admin/src/pages/users/columns.tsx`
- Create: `tsa-dev/tsa-admin/src/pages/users/index.tsx`
- Modify: `tsa-dev/tsa-admin/src/app.tsx` (add route)

- [ ] **Step 1: Create users API module**

Create `tsa-dev/tsa-admin/src/api/users.ts`:

```ts
import client from './client';
import type { ApiResponse, User } from '@/types';

export async function getUsers(params: { page?: number; limit?: number; search?: string; role?: string }) {
  const { data } = await client.get<ApiResponse<{ users: User[]; pagination: { page: number; limit: number; total: number; totalPages: number } }>>('/users', { params });
  return data;
}

export async function updateUserRole(userId: string, role: string) {
  const { data } = await client.patch<ApiResponse>(`/users/${userId}/role`, { role });
  return data;
}
```

- [ ] **Step 2: Create column definitions**

Create `tsa-dev/tsa-admin/src/pages/users/columns.tsx`:

```tsx
import type { ColumnDef } from '@tanstack/react-table';
import type { User } from '@/types';
import { StatusBadge } from '@/components/shared/status-badge';
import { ROLE_LABELS, VERIFICATION_STATUS_COLORS } from '@/lib/constants';

export const columns: ColumnDef<User>[] = [
  { accessorKey: 'name', header: 'Name' },
  { accessorKey: 'email', header: 'Email' },
  {
    accessorKey: 'role',
    header: 'Role',
    cell: ({ row }) => (
      <span className="text-sm font-medium">{ROLE_LABELS[row.original.role] || row.original.role}</span>
    ),
  },
  {
    accessorKey: 'verificationStatus',
    header: 'Verification',
    cell: ({ row }) => (
      <StatusBadge status={row.original.verificationStatus} colorMap={VERIFICATION_STATUS_COLORS} />
    ),
  },
  {
    accessorKey: 'accountStatus',
    header: 'Status',
    cell: ({ row }) => (
      <StatusBadge
        status={row.original.accountStatus}
        colorMap={{ active: 'bg-green-100 text-green-700', inactive: 'bg-slate-100 text-slate-700', suspended: 'bg-red-100 text-red-700' }}
      />
    ),
  },
  {
    accessorKey: 'createdAt',
    header: 'Joined',
    cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString(),
  },
];
```

- [ ] **Step 3: Create Users page**

Create `tsa-dev/tsa-admin/src/pages/users/index.tsx`:

```tsx
import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { getUsers } from '@/api/users';
import { DataTable } from '@/components/shared/data-table';
import { PageHeader } from '@/components/shared/page-header';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { columns } from './columns';
import { Search } from 'lucide-react';

export default function UsersPage() {
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState('');
  const [roleFilter, setRoleFilter] = useState('');

  const { data, isLoading } = useQuery({
    queryKey: ['users', page, search, roleFilter],
    queryFn: () => getUsers({ page, limit: 20, search: search || undefined, role: roleFilter || undefined }),
  });

  const users = data?.data?.users ?? [];
  const pagination = data?.data?.pagination;

  return (
    <div>
      <PageHeader title="Users" description="Manage all registered users" />

      <div className="mb-4 flex items-center gap-3">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-slate-400" />
          <Input
            placeholder="Search users..."
            value={search}
            onChange={(e) => { setSearch(e.target.value); setPage(1); }}
            className="pl-9"
          />
        </div>
        <Select value={roleFilter} onValueChange={(v) => { setRoleFilter(v === 'all' ? '' : v); setPage(1); }}>
          <SelectTrigger className="w-40">
            <SelectValue placeholder="All roles" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All roles</SelectItem>
            <SelectItem value="super_admin">Super Admin</SelectItem>
            <SelectItem value="admin">Admin</SelectItem>
            <SelectItem value="support">Support</SelectItem>
            <SelectItem value="merchant">Merchant</SelectItem>
            <SelectItem value="user">User</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <DataTable
        columns={columns}
        data={users}
        page={page}
        totalPages={pagination?.totalPages ?? 1}
        onPageChange={setPage}
        isLoading={isLoading}
      />
    </div>
  );
}
```

- [ ] **Step 4: Add Users route**

In `tsa-dev/tsa-admin/src/app.tsx`, add the import and route inside the protected layout `<Route>`:

```tsx
import UsersPage from '@/pages/users/index';
// Add child route:
<Route path="users" element={<UsersPage />} />
```

- [ ] **Step 5: Commit**

```bash
cd tsa-dev/tsa-admin
git add src/api/users.ts src/pages/users/
git commit -m "feat: add users management page with DataTable"
```

---

### Task 13: Products Page

**Files:**
- Create: `tsa-dev/tsa-admin/src/api/products.ts`
- Create: `tsa-dev/tsa-admin/src/pages/products/columns.tsx`
- Create: `tsa-dev/tsa-admin/src/pages/products/index.tsx`
- Modify: `tsa-dev/tsa-admin/src/app.tsx` (add route)

- [ ] **Step 1: Create products API module**

Create `tsa-dev/tsa-admin/src/api/products.ts`:

```ts
import client from './client';
import type { ApiResponse, Product } from '@/types';

export async function getProducts(params: { page?: number; limit?: number; search?: string; status?: string; type?: string }) {
  const { data } = await client.get<ApiResponse<{ products: Product[]; pagination: { page: number; limit: number; total: number; totalPages: number } }>>('/products', { params });
  return data;
}

export async function getNonFeaturedProducts() {
  const { data } = await client.get<ApiResponse<Product[]>>('/products/non-featured');
  return data;
}

export async function toggleFeatured(productId: string) {
  const { data } = await client.patch<ApiResponse>(`/products/${productId}/featured`);
  return data;
}

export async function updateProduct(productId: string, updates: Partial<Product>) {
  const { data } = await client.put<ApiResponse>(`/products/${productId}`, updates);
  return data;
}

export async function deleteProduct(productId: string) {
  const { data } = await client.delete<ApiResponse>(`/products/${productId}`);
  return data;
}
```

- [ ] **Step 2: Create column definitions**

Create `tsa-dev/tsa-admin/src/pages/products/columns.tsx`:

```tsx
import type { ColumnDef } from '@tanstack/react-table';
import type { Product } from '@/types';
import { StatusBadge } from '@/components/shared/status-badge';

const STATUS_COLORS: Record<string, string> = {
  active: 'bg-green-100 text-green-700',
  inactive: 'bg-slate-100 text-slate-700',
  sold_out: 'bg-red-100 text-red-700',
  pending_review: 'bg-amber-100 text-amber-700',
};

export const columns: ColumnDef<Product>[] = [
  { accessorKey: 'name', header: 'Name' },
  {
    accessorKey: 'price',
    header: 'Price',
    cell: ({ row }) => `₦${row.original.price.toLocaleString()}`,
  },
  { accessorKey: 'type', header: 'Type' },
  { accessorKey: 'stock', header: 'Stock' },
  {
    accessorKey: 'status',
    header: 'Status',
    cell: ({ row }) => <StatusBadge status={row.original.status} colorMap={STATUS_COLORS} />,
  },
  {
    accessorKey: 'isFeatured',
    header: 'Featured',
    cell: ({ row }) => row.original.isFeatured ? '⭐' : '—',
  },
  {
    accessorKey: 'createdAt',
    header: 'Created',
    cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString(),
  },
];
```

- [ ] **Step 3: Create Products page**

Create `tsa-dev/tsa-admin/src/pages/products/index.tsx`:

```tsx
import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { getProducts } from '@/api/products';
import { DataTable } from '@/components/shared/data-table';
import { PageHeader } from '@/components/shared/page-header';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { columns } from './columns';
import { Search } from 'lucide-react';

export default function ProductsPage() {
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState('');

  const { data, isLoading } = useQuery({
    queryKey: ['products', page, search, statusFilter],
    queryFn: () => getProducts({ page, limit: 20, search: search || undefined, status: statusFilter || undefined }),
  });

  const products = data?.data?.products ?? [];
  const pagination = data?.data?.pagination;

  return (
    <div>
      <PageHeader title="Products" description="Manage marketplace products" />

      <div className="mb-4 flex items-center gap-3">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-slate-400" />
          <Input placeholder="Search products..." value={search} onChange={(e) => { setSearch(e.target.value); setPage(1); }} className="pl-9" />
        </div>
        <Select value={statusFilter} onValueChange={(v) => { setStatusFilter(v === 'all' ? '' : v); setPage(1); }}>
          <SelectTrigger className="w-40">
            <SelectValue placeholder="All statuses" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            <SelectItem value="active">Active</SelectItem>
            <SelectItem value="inactive">Inactive</SelectItem>
            <SelectItem value="sold_out">Sold Out</SelectItem>
            <SelectItem value="pending_review">Pending Review</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <DataTable columns={columns} data={products} page={page} totalPages={pagination?.totalPages ?? 1} onPageChange={setPage} isLoading={isLoading} />
    </div>
  );
}
```

- [ ] **Step 4: Add Products route**

In `tsa-dev/tsa-admin/src/app.tsx`, add the import and route:

```tsx
import ProductsPage from '@/pages/products/index';
// Add child route:
<Route path="products" element={<ProductsPage />} />
```

- [ ] **Step 5: Commit**

```bash
cd tsa-dev/tsa-admin
git add src/api/products.ts src/pages/products/
git commit -m "feat: add products management page"
```

---

### Task 14: Categories, Orders, Advert Requests, Deposits, Verifications, Settings Pages

Each of these pages follows the exact same pattern as Users/Products. For brevity, each page needs:

1. An API module in `src/api/` with typed functions
2. Column definitions in `src/pages/<feature>/columns.tsx` (for table-based pages)
3. Page component in `src/pages/<feature>/index.tsx`
4. Route added to `src/app.tsx`

- [ ] **Step 1: Create remaining API modules**

Create `tsa-dev/tsa-admin/src/api/categories.ts`:

```ts
import client from './client';
import type { ApiResponse, Category } from '@/types';

export async function getCategories(params?: { type?: string; active?: boolean }) {
  const { data } = await client.get<ApiResponse<Category[]>>('/products/category/all', { params });
  return data;
}

export async function getCategoryTree() {
  const { data } = await client.get<ApiResponse<Category[]>>('/products/category/tree');
  return data;
}

export async function createCategory(categoryData: FormData) {
  const { data } = await client.post<ApiResponse<Category>>('/products/category', categoryData, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return data;
}

export async function updateCategory(id: string, categoryData: FormData) {
  const { data } = await client.put<ApiResponse<Category>>(`/products/category/${id}`, categoryData, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return data;
}

export async function deleteCategory(id: string) {
  const { data } = await client.delete<ApiResponse>(`/products/category/${id}`);
  return data;
}

export async function reorderCategories(orderedIds: string[]) {
  const { data } = await client.patch<ApiResponse>('/products/category/reorder', { ids: orderedIds });
  return data;
}
```

Create `tsa-dev/tsa-admin/src/api/orders.ts`:

```ts
import client from './client';
import type { ApiResponse, Order } from '@/types';

export async function getOrders(params: { page?: number; limit?: number; status?: string }) {
  const { data } = await client.get<ApiResponse<{ orders: Order[]; pagination: { page: number; limit: number; total: number; totalPages: number } }>>('/orders', { params });
  return data;
}

export async function getOrderById(id: string) {
  const { data } = await client.get<ApiResponse<Order>>(`/orders/${id}`);
  return data;
}

export async function updateOrderStatus(id: string, status: string, notes?: string) {
  const { data } = await client.patch<ApiResponse>(`/orders/${id}/status`, { status, notes });
  return data;
}
```

Create `tsa-dev/tsa-admin/src/api/deposits.ts`:

```ts
import client from './client';
import type { ApiResponse, Deposit } from '@/types';

export async function getDeposits(params: { page?: number; limit?: number; status?: string }) {
  const { data } = await client.get<ApiResponse<{ deposits: Deposit[]; pagination: { page: number; limit: number; total: number; totalPages: number } }>>('/deposits', { params });
  return data;
}

export async function updateDepositStatus(id: string, status: 'approved' | 'rejected', note?: string) {
  const { data } = await client.patch<ApiResponse>(`/deposits/${id}/status`, { status, note });
  return data;
}
```

Create `tsa-dev/tsa-admin/src/api/verification.ts`:

```ts
import client from './client';
import type { ApiResponse, User } from '@/types';

export async function getPendingVerifications(params: { page?: number; limit?: number }) {
  const { data } = await client.get<ApiResponse<{ users: User[]; pagination: { page: number; limit: number; total: number; totalPages: number } }>>('/users', {
    params: { ...params, verificationStatus: 'pending' },
  });
  return data;
}

export async function approveVerification(userId: string) {
  const { data } = await client.post<ApiResponse>(`/verification/approve/${userId}`);
  return data;
}

export async function rejectVerification(userId: string, reason?: string) {
  const { data } = await client.post<ApiResponse>(`/verification/reject/${userId}`, { reason });
  return data;
}
```

- [ ] **Step 2: Create Categories page**

Create `tsa-dev/tsa-admin/src/pages/categories/index.tsx`:

```tsx
import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getCategories, createCategory, updateCategory, deleteCategory } from '@/api/categories';
import { PageHeader } from '@/components/shared/page-header';
import { ConfirmDialog } from '@/components/shared/confirm-dialog';
import { StatusBadge } from '@/components/shared/status-badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog';
import { Card, CardContent } from '@/components/ui/card';
import { Plus, Pencil, Trash2, FolderTree } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { Category } from '@/types';

export default function CategoriesPage() {
  const queryClient = useQueryClient();
  const canCreate = usePermission('categories.create');
  const canEdit = usePermission('categories.edit');
  const canDelete = usePermission('categories.delete');

  const [typeFilter, setTypeFilter] = useState('');
  const [modalOpen, setModalOpen] = useState(false);
  const [editingCategory, setEditingCategory] = useState<Category | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Category | null>(null);
  const [form, setForm] = useState({ title: '', description: '', type: 'Product' as string, isActive: true });

  const { data, isLoading } = useQuery({
    queryKey: ['categories', typeFilter],
    queryFn: () => getCategories({ type: typeFilter || undefined, active: undefined }),
  });

  const categories = data?.data ?? [];

  const saveMutation = useMutation({
    mutationFn: (formData: FormData) =>
      editingCategory ? updateCategory(editingCategory.id, formData) : createCategory(formData),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['categories'] });
      setModalOpen(false);
      setEditingCategory(null);
      toast.success(editingCategory ? 'Category updated' : 'Category created');
    },
    onError: () => toast.error('Failed to save category'),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteCategory(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['categories'] });
      setDeleteTarget(null);
      toast.success('Category deleted');
    },
  });

  const handleSave = () => {
    const fd = new FormData();
    fd.append('title', form.title);
    fd.append('description', form.description);
    fd.append('type', form.type);
    fd.append('isActive', String(form.isActive));
    saveMutation.mutate(fd);
  };

  const openEdit = (cat: Category) => {
    setEditingCategory(cat);
    setForm({ title: cat.title, description: cat.description || '', type: cat.type, isActive: cat.isActive });
    setModalOpen(true);
  };

  const openCreate = () => {
    setEditingCategory(null);
    setForm({ title: '', description: '', type: 'Product', isActive: true });
    setModalOpen(true);
  };

  // Group categories by parent
  const parentCats = categories.filter((c: Category) => !c.parentCategory);
  const childCats = (parentId: string) => categories.filter((c: Category) => c.parentCategory === parentId);

  return (
    <div>
      <PageHeader
        title="Categories"
        description="Manage product and service categories"
        actions={canCreate ? <Button onClick={openCreate}><Plus className="mr-2 h-4 w-4" />Add Category</Button> : undefined}
      />

      <div className="mb-4">
        <Select value={typeFilter} onValueChange={(v) => setTypeFilter(v === 'all' ? '' : v)}>
          <SelectTrigger className="w-40"><SelectValue placeholder="All types" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All types</SelectItem>
            <SelectItem value="Product">Product</SelectItem>
            <SelectItem value="Service">Service</SelectItem>
            <SelectItem value="Both">Both</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {isLoading ? (
        <div className="space-y-2">{Array.from({ length: 5 }).map((_, i) => <div key={i} className="h-12 bg-slate-100 animate-pulse rounded" />)}</div>
      ) : (
        <div className="space-y-3">
          {parentCats.map((cat: Category) => (
            <Card key={cat.id}>
              <CardContent className="p-4">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3">
                    <FolderTree className="h-5 w-5 text-blue-600" />
                    <div>
                      <p className="font-medium text-slate-900">{cat.title}</p>
                      <p className="text-xs text-slate-500">{cat.type} · {cat.productCount} products</p>
                    </div>
                    <StatusBadge status={cat.isActive ? 'active' : 'inactive'} colorMap={{ active: 'bg-green-100 text-green-700', inactive: 'bg-slate-100 text-slate-700' }} />
                  </div>
                  <div className="flex gap-1">
                    {canEdit && <Button variant="ghost" size="icon" onClick={() => openEdit(cat)}><Pencil className="h-4 w-4" /></Button>}
                    {canDelete && <Button variant="ghost" size="icon" onClick={() => setDeleteTarget(cat)}><Trash2 className="h-4 w-4 text-red-500" /></Button>}
                  </div>
                </div>
                {childCats(cat.id).length > 0 && (
                  <div className="mt-3 ml-8 space-y-2">
                    {childCats(cat.id).map((child: Category) => (
                      <div key={child.id} className="flex items-center justify-between rounded border border-slate-100 p-2">
                        <span className="text-sm text-slate-700">{child.title}</span>
                        <div className="flex gap-1">
                          {canEdit && <Button variant="ghost" size="sm" onClick={() => openEdit(child)}><Pencil className="h-3 w-3" /></Button>}
                          {canDelete && <Button variant="ghost" size="sm" onClick={() => setDeleteTarget(child)}><Trash2 className="h-3 w-3 text-red-500" /></Button>}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {/* Create/Edit Modal */}
      <Dialog open={modalOpen} onOpenChange={setModalOpen}>
        <DialogContent>
          <DialogHeader><DialogTitle>{editingCategory ? 'Edit Category' : 'Create Category'}</DialogTitle></DialogHeader>
          <div className="space-y-4">
            <div><Label>Title</Label><Input value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} /></div>
            <div><Label>Description</Label><Input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} /></div>
            <div>
              <Label>Type</Label>
              <Select value={form.type} onValueChange={(v) => setForm({ ...form, type: v })}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="Product">Product</SelectItem>
                  <SelectItem value="Service">Service</SelectItem>
                  <SelectItem value="Both">Both</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setModalOpen(false)}>Cancel</Button>
            <Button onClick={handleSave} disabled={saveMutation.isPending}>{saveMutation.isPending ? 'Saving...' : 'Save'}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={() => setDeleteTarget(null)}
        title="Delete Category"
        description={`Are you sure you want to delete "${deleteTarget?.title}"? This cannot be undone.`}
        confirmLabel="Delete"
        variant="destructive"
        loading={deleteMutation.isPending}
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
      />
    </div>
  );
}
```

- [ ] **Step 3: Create Orders page + columns + detail page**

Create `tsa-dev/tsa-admin/src/pages/orders/columns.tsx`:

```tsx
import type { ColumnDef } from '@tanstack/react-table';
import type { Order } from '@/types';
import { StatusBadge } from '@/components/shared/status-badge';
import { ORDER_STATUS_COLORS, ORDER_STATUS_LABELS } from '@/lib/constants';

export const columns: ColumnDef<Order>[] = [
  { accessorKey: 'id', header: 'Order ID', cell: ({ row }) => row.original.id.slice(0, 8) + '...' },
  { accessorKey: 'total', header: 'Total', cell: ({ row }) => `₦${row.original.total.toLocaleString()}` },
  { accessorKey: 'status', header: 'Status', cell: ({ row }) => <StatusBadge status={row.original.status} colorMap={ORDER_STATUS_COLORS} labelMap={ORDER_STATUS_LABELS} /> },
  { accessorKey: 'createdAt', header: 'Date', cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString() },
];
```

Create `tsa-dev/tsa-admin/src/pages/orders/index.tsx`:

```tsx
import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { getOrders } from '@/api/orders';
import { DataTable } from '@/components/shared/data-table';
import { PageHeader } from '@/components/shared/page-header';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { columns } from './columns';

export default function OrdersPage() {
  const [page, setPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState('');
  const navigate = useNavigate();

  const { data, isLoading } = useQuery({
    queryKey: ['orders', page, statusFilter],
    queryFn: () => getOrders({ page, limit: 20, status: statusFilter || undefined }),
  });

  const orders = data?.data?.orders ?? [];
  const pagination = data?.data?.pagination;

  return (
    <div>
      <PageHeader title="Orders" description="View and manage all orders" />
      <div className="mb-4">
        <Select value={statusFilter} onValueChange={(v) => { setStatusFilter(v === 'all' ? '' : v); setPage(1); }}>
          <SelectTrigger className="w-40"><SelectValue placeholder="All statuses" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            <SelectItem value="pending">Pending</SelectItem>
            <SelectItem value="processing">Processing</SelectItem>
            <SelectItem value="shipped">Shipped</SelectItem>
            <SelectItem value="completed">Completed</SelectItem>
            <SelectItem value="cancelled">Cancelled</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DataTable columns={columns} data={orders} page={page} totalPages={pagination?.totalPages ?? 1} onPageChange={setPage} isLoading={isLoading} onRowClick={(row) => navigate(`/orders/${row.id}`)} />
    </div>
  );
}
```

Create `tsa-dev/tsa-admin/src/pages/orders/[id].tsx`:

```tsx
import { useParams, useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getOrderById, updateOrderStatus } from '@/api/orders';
import { PageHeader } from '@/components/shared/page-header';
import { StatusBadge } from '@/components/shared/status-badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { ORDER_STATUS_COLORS, ORDER_STATUS_LABELS } from '@/lib/constants';
import { Skeleton } from '@/components/ui/skeleton';
import { ArrowLeft } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';

const VALID_TRANSITIONS: Record<string, string[]> = {
  pending: ['processing', 'cancelled'],
  processing: ['shipped', 'cancelled'],
  shipped: ['completed', 'cancelled'],
  completed: [],
  cancelled: [],
};

export default function OrderDetailPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const canUpdate = usePermission('orders.update_status');

  const { data, isLoading } = useQuery({
    queryKey: ['order', id],
    queryFn: () => getOrderById(id!),
    enabled: !!id,
  });

  const mutation = useMutation({
    mutationFn: ({ status }: { status: string }) => updateOrderStatus(id!, status),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['order', id] });
      queryClient.invalidateQueries({ queryKey: ['orders'] });
      toast.success('Order status updated');
    },
  });

  const order = data?.data;
  const nextStatuses = order ? VALID_TRANSITIONS[order.status] || [] : [];

  if (isLoading) return <Skeleton className="h-64 w-full" />;
  if (!order) return <p className="text-slate-500">Order not found.</p>;

  return (
    <div>
      <Button variant="ghost" size="sm" onClick={() => navigate('/orders')} className="mb-4">
        <ArrowLeft className="mr-2 h-4 w-4" /> Back to Orders
      </Button>

      <PageHeader title={`Order ${order.id.slice(0, 8)}...`} />

      <div className="grid grid-cols-2 gap-4 mb-6">
        <Card>
          <CardHeader><CardTitle className="text-sm">Status</CardTitle></CardHeader>
          <CardContent>
            <StatusBadge status={order.status} colorMap={ORDER_STATUS_COLORS} labelMap={ORDER_STATUS_LABELS} />
            {canUpdate && nextStatuses.length > 0 && (
              <div className="mt-3 flex gap-2">
                {nextStatuses.map((s) => (
                  <Button key={s} size="sm" variant={s === 'cancelled' ? 'destructive' : 'default'} onClick={() => mutation.mutate({ status: s })} disabled={mutation.isPending}>
                    {ORDER_STATUS_LABELS[s]}
                  </Button>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle className="text-sm">Summary</CardTitle></CardHeader>
          <CardContent>
            <p className="text-2xl font-bold">₦{order.total.toLocaleString()}</p>
            <p className="text-xs text-slate-500">{order.currency} · {new Date(order.createdAt).toLocaleDateString()}</p>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader><CardTitle className="text-sm">Items</CardTitle></CardHeader>
        <CardContent>
          {order.items?.length ? (
            <div className="space-y-2">
              {order.items.map((item, i) => (
                <div key={i} className="flex justify-between border-b border-slate-100 pb-2 last:border-0">
                  <div>
                    <p className="text-sm font-medium">{item.name}</p>
                    <p className="text-xs text-slate-500">Qty: {item.quantity} × ₦{item.unitPrice.toLocaleString()}</p>
                  </div>
                  <p className="text-sm font-medium">₦{item.total.toLocaleString()}</p>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-sm text-slate-500">No items.</p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
```

- [ ] **Step 4: Create Advert Requests page**

Create `tsa-dev/tsa-admin/src/pages/advert-requests/index.tsx`:

```tsx
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getNonFeaturedProducts, toggleFeatured, updateProduct } from '@/api/products';
import { PageHeader } from '@/components/shared/page-header';
import { ConfirmDialog } from '@/components/shared/confirm-dialog';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { Check, X } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import { useState } from 'react';
import type { Product } from '@/types';

export default function AdvertRequestsPage() {
  const queryClient = useQueryClient();
  const canApprove = usePermission('products.approve');
  const [rejectTarget, setRejectTarget] = useState<Product | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['non-featured-products'],
    queryFn: getNonFeaturedProducts,
  });

  const products = data?.data ?? [];

  const approveMutation = useMutation({
    mutationFn: (id: string) => toggleFeatured(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['non-featured-products'] });
      queryClient.invalidateQueries({ queryKey: ['admin-stats'] });
      toast.success('Product approved as featured');
    },
  });

  const rejectMutation = useMutation({
    mutationFn: (id: string) => updateProduct(id, { status: 'inactive' } as Partial<Product>),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['non-featured-products'] });
      setRejectTarget(null);
      toast.success('Product rejected');
    },
  });

  return (
    <div>
      <PageHeader title="Advert Requests" description={`${products.length} products awaiting review`} />

      {isLoading ? (
        <div className="grid grid-cols-2 gap-4">{Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-40" />)}</div>
      ) : products.length === 0 ? (
        <p className="text-center text-slate-500 py-12">No pending advert requests.</p>
      ) : (
        <div className="grid grid-cols-2 gap-4">
          {(products as Product[]).map((product) => (
            <Card key={product.id}>
              <CardContent className="p-4">
                <div className="flex justify-between items-start mb-2">
                  <div>
                    <p className="font-medium text-slate-900">{product.name}</p>
                    <p className="text-sm text-slate-500">₦{product.price.toLocaleString()} · {product.type}</p>
                  </div>
                  <p className="text-xs text-slate-400">{new Date(product.createdAt).toLocaleDateString()}</p>
                </div>
                <p className="text-sm text-slate-600 mb-3 line-clamp-2">{product.description}</p>
                {canApprove && (
                  <div className="flex gap-2">
                    <Button size="sm" onClick={() => approveMutation.mutate(product.id)} disabled={approveMutation.isPending}>
                      <Check className="mr-1 h-3 w-3" /> Approve
                    </Button>
                    <Button size="sm" variant="destructive" onClick={() => setRejectTarget(product)}>
                      <X className="mr-1 h-3 w-3" /> Reject
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <ConfirmDialog
        open={!!rejectTarget}
        onOpenChange={() => setRejectTarget(null)}
        title="Reject Product"
        description={`Reject "${rejectTarget?.name}"? It will be marked inactive.`}
        confirmLabel="Reject"
        variant="destructive"
        loading={rejectMutation.isPending}
        onConfirm={() => rejectTarget && rejectMutation.mutate(rejectTarget.id)}
      />
    </div>
  );
}
```

- [ ] **Step 5: Create Deposits page + columns**

Create `tsa-dev/tsa-admin/src/pages/deposits/columns.tsx`:

```tsx
import type { ColumnDef } from '@tanstack/react-table';
import type { Deposit } from '@/types';
import { StatusBadge } from '@/components/shared/status-badge';
import { DEPOSIT_STATUS_COLORS } from '@/lib/constants';

export const columns: ColumnDef<Deposit>[] = [
  { accessorKey: 'userId', header: 'User', cell: ({ row }) => row.original.userId.slice(0, 8) + '...' },
  { accessorKey: 'amount', header: 'Amount', cell: ({ row }) => `₦${row.original.amount.toLocaleString()}` },
  { accessorKey: 'currency', header: 'Currency' },
  { accessorKey: 'status', header: 'Status', cell: ({ row }) => <StatusBadge status={row.original.status} colorMap={DEPOSIT_STATUS_COLORS} /> },
  { accessorKey: 'createdAt', header: 'Date', cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString() },
];
```

Create `tsa-dev/tsa-admin/src/pages/deposits/index.tsx`:

```tsx
import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getDeposits, updateDepositStatus } from '@/api/deposits';
import { DataTable } from '@/components/shared/data-table';
import { PageHeader } from '@/components/shared/page-header';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Button } from '@/components/ui/button';
import { Check, X } from 'lucide-react';
import { columns as baseColumns } from './columns';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { ColumnDef } from '@tanstack/react-table';
import type { Deposit } from '@/types';

export default function DepositsPage() {
  const [page, setPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState('');
  const queryClient = useQueryClient();
  const canApprove = usePermission('deposits.approve');

  const { data, isLoading } = useQuery({
    queryKey: ['deposits', page, statusFilter],
    queryFn: () => getDeposits({ page, limit: 20, status: statusFilter || undefined }),
  });

  const mutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: 'approved' | 'rejected' }) => updateDepositStatus(id, status),
    onSuccess: (_, vars) => {
      queryClient.invalidateQueries({ queryKey: ['deposits'] });
      queryClient.invalidateQueries({ queryKey: ['admin-stats'] });
      toast.success(`Deposit ${vars.status}`);
    },
  });

  const actionsColumn: ColumnDef<Deposit> = {
    id: 'actions',
    header: 'Actions',
    cell: ({ row }) => {
      if (row.original.status !== 'pending' || !canApprove) return null;
      return (
        <div className="flex gap-1">
          <Button size="sm" variant="ghost" onClick={(e) => { e.stopPropagation(); mutation.mutate({ id: row.original.id, status: 'approved' }); }}>
            <Check className="h-4 w-4 text-green-600" />
          </Button>
          <Button size="sm" variant="ghost" onClick={(e) => { e.stopPropagation(); mutation.mutate({ id: row.original.id, status: 'rejected' }); }}>
            <X className="h-4 w-4 text-red-600" />
          </Button>
        </div>
      );
    },
  };

  const deposits = data?.data?.deposits ?? [];
  const pagination = data?.data?.pagination;

  return (
    <div>
      <PageHeader title="Deposit Requests" description="Review and approve deposit requests" />
      <div className="mb-4">
        <Select value={statusFilter} onValueChange={(v) => { setStatusFilter(v === 'all' ? '' : v); setPage(1); }}>
          <SelectTrigger className="w-40"><SelectValue placeholder="All statuses" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            <SelectItem value="pending">Pending</SelectItem>
            <SelectItem value="approved">Approved</SelectItem>
            <SelectItem value="rejected">Rejected</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DataTable columns={[...baseColumns, actionsColumn]} data={deposits} page={page} totalPages={pagination?.totalPages ?? 1} onPageChange={setPage} isLoading={isLoading} />
    </div>
  );
}
```

- [ ] **Step 6: Create Verifications page**

Create `tsa-dev/tsa-admin/src/pages/verifications/index.tsx`:

```tsx
import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getPendingVerifications, approveVerification, rejectVerification } from '@/api/verification';
import { PageHeader } from '@/components/shared/page-header';
import { ConfirmDialog } from '@/components/shared/confirm-dialog';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { StatusBadge } from '@/components/shared/status-badge';
import { VERIFICATION_STATUS_COLORS } from '@/lib/constants';
import { Check, X } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { User } from '@/types';

export default function VerificationsPage() {
  const queryClient = useQueryClient();
  const canApprove = usePermission('verifications.approve');
  const [rejectTarget, setRejectTarget] = useState<User | null>(null);
  const [page, setPage] = useState(1);

  const { data, isLoading } = useQuery({
    queryKey: ['verifications', page],
    queryFn: () => getPendingVerifications({ page, limit: 20 }),
  });

  const users = data?.data?.users ?? [];

  const approveMutation = useMutation({
    mutationFn: (id: string) => approveVerification(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['verifications'] });
      queryClient.invalidateQueries({ queryKey: ['admin-stats'] });
      toast.success('Verification approved');
    },
  });

  const rejectMutation = useMutation({
    mutationFn: (id: string) => rejectVerification(id, 'Rejected by admin'),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['verifications'] });
      setRejectTarget(null);
      toast.success('Verification rejected');
    },
  });

  return (
    <div>
      <PageHeader title="Verifications" description="Review identity verification requests" />

      {isLoading ? (
        <div className="space-y-3">{Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-24" />)}</div>
      ) : users.length === 0 ? (
        <p className="text-center text-slate-500 py-12">No pending verifications.</p>
      ) : (
        <div className="space-y-3">
          {(users as User[]).map((user) => (
            <Card key={user.id}>
              <CardContent className="flex items-center justify-between p-4">
                <div>
                  <p className="font-medium text-slate-900">{user.name}</p>
                  <p className="text-sm text-slate-500">{user.email}</p>
                  <StatusBadge status={user.verificationStatus} colorMap={VERIFICATION_STATUS_COLORS} />
                </div>
                {canApprove && (
                  <div className="flex gap-2">
                    <Button size="sm" onClick={() => approveMutation.mutate(user.id)} disabled={approveMutation.isPending}>
                      <Check className="mr-1 h-3 w-3" /> Approve
                    </Button>
                    <Button size="sm" variant="destructive" onClick={() => setRejectTarget(user)}>
                      <X className="mr-1 h-3 w-3" /> Reject
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <ConfirmDialog
        open={!!rejectTarget}
        onOpenChange={() => setRejectTarget(null)}
        title="Reject Verification"
        description={`Reject verification for "${rejectTarget?.name}"?`}
        confirmLabel="Reject"
        variant="destructive"
        loading={rejectMutation.isPending}
        onConfirm={() => rejectTarget && rejectMutation.mutate(rejectTarget.id)}
      />
    </div>
  );
}
```

- [ ] **Step 7: Create Settings page**

Create `tsa-dev/tsa-admin/src/pages/settings/index.tsx`:

```tsx
import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getUsers, updateUserRole } from '@/api/users';
import { PageHeader } from '@/components/shared/page-header';
import { DataTable } from '@/components/shared/data-table';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { ROLE_LABELS } from '@/lib/constants';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { ColumnDef } from '@tanstack/react-table';
import type { User } from '@/types';

export default function SettingsPage() {
  const queryClient = useQueryClient();
  const canManageRoles = usePermission('users.manage_roles');
  const [page, setPage] = useState(1);

  const { data, isLoading } = useQuery({
    queryKey: ['settings-users', page],
    queryFn: () => getUsers({ page, limit: 20 }),
  });

  const mutation = useMutation({
    mutationFn: ({ userId, role }: { userId: string; role: string }) => updateUserRole(userId, role),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings-users'] });
      toast.success('Role updated');
    },
  });

  const columns: ColumnDef<User>[] = [
    { accessorKey: 'name', header: 'Name' },
    { accessorKey: 'email', header: 'Email' },
    {
      accessorKey: 'role',
      header: 'Role',
      cell: ({ row }) => canManageRoles ? (
        <Select value={row.original.role} onValueChange={(v) => mutation.mutate({ userId: row.original.id, role: v })}>
          <SelectTrigger className="w-36"><SelectValue /></SelectTrigger>
          <SelectContent>
            {Object.entries(ROLE_LABELS).map(([value, label]) => (
              <SelectItem key={value} value={value}>{label}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <span>{ROLE_LABELS[row.original.role] || row.original.role}</span>
      ),
    },
  ];

  const users = data?.data?.users ?? [];
  const pagination = data?.data?.pagination;

  if (!canManageRoles) return <p className="text-slate-500">Access denied.</p>;

  return (
    <div>
      <PageHeader title="Settings" description="Manage user roles and permissions" />
      <DataTable columns={columns} data={users} page={page} totalPages={pagination?.totalPages ?? 1} onPageChange={setPage} isLoading={isLoading} />
    </div>
  );
}
```

- [ ] **Step 8: Wire all routes in app.tsx**

Add all page imports and `<Route>` elements to `tsa-dev/tsa-admin/src/app.tsx` inside the protected layout route:

```tsx
// Add imports:
import CategoriesPage from '@/pages/categories/index';
import OrdersPage from '@/pages/orders/index';
import OrderDetailPage from '@/pages/orders/[id]';
import AdvertRequestsPage from '@/pages/advert-requests/index';
import DepositsPage from '@/pages/deposits/index';
import VerificationsPage from '@/pages/verifications/index';
import SettingsPage from '@/pages/settings/index';

// Add inside the protected layout <Route> children:
<Route path="categories" element={<CategoriesPage />} />
<Route path="orders" element={<OrdersPage />} />
<Route path="orders/:id" element={<OrderDetailPage />} />
<Route path="advert-requests" element={<AdvertRequestsPage />} />
<Route path="deposits" element={<DepositsPage />} />
<Route path="verifications" element={<VerificationsPage />} />
<Route path="settings" element={<SettingsPage />} />
```

- [ ] **Step 9: Commit each page as completed**

Commit after each page is fully functional:

```bash
git add src/api/ src/pages/ src/app.tsx
git commit -m "feat: add categories, orders, deposits, verifications, advert requests, and settings pages"
```

---

## Chunk 5: Polish & Verification

### Task 15: Final Integration Testing

- [ ] **Step 1: Start the Go API**

```bash
cd tsa-dev/tsa-api-go && go run ./cmd/server
```

- [ ] **Step 2: Start the admin panel**

```bash
cd tsa-dev/tsa-admin && npm run dev
```

- [ ] **Step 3: Test login flow**

Open `http://localhost:5173`. Log in with an admin account. Verify redirect to dashboard. Verify sidebar shows correct nav items for role.

- [ ] **Step 4: Test each page loads and fetches data**

Navigate to each page via sidebar and verify:
- Dashboard shows real stats from API
- Users table loads with pagination
- Products table loads
- Categories tree renders
- Orders table loads
- Advert requests shows non-featured products
- Deposits table loads
- Verifications shows pending users
- Settings shows role management (super admin only)

- [ ] **Step 5: Test RBAC**

Log in as `support` role — verify Settings is hidden, approve/reject buttons are hidden on approval pages. Log in as `admin` — verify Settings is hidden but approve/reject works. Log in as `super_admin` — verify everything is accessible.

- [ ] **Step 6: Test error handling**

- Clear token manually → verify redirect to login on next API call
- Attempt forbidden action → verify toast "You don't have permission"

- [ ] **Step 7: Build for production**

```bash
cd tsa-dev/tsa-admin && npm run build
```

Expected: Build succeeds with no TypeScript errors.

- [ ] **Step 8: Final commit**

```bash
cd tsa-dev/tsa-admin
git add .
git commit -m "feat: complete TSA admin web panel v1"
```
