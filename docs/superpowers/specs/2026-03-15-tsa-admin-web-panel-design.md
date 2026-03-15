# TSA Admin Web Panel — Design Spec

**Date:** 2026-03-15
**Status:** Approved
**Scope:** Standalone web admin dashboard for TSA Connect marketplace

## Context

The TSA Connect mobile app (Expo/React Native) currently has an embedded admin flow at `app/admin/`. This is problematic: admin functionality ships to every user's device, admin tasks are desktop workflows forced into mobile UIs, and deploys are coupled to app store releases.

This spec describes a **standalone web admin panel** that replaces the in-app admin flow, connecting to the same Go/Gin API backend.

## Tech Stack

- **Frontend:** React + Vite + TypeScript
- **UI:** Shadcn/ui + Tailwind CSS
- **Routing:** React Router v7
- **Data fetching:** Tanstack Query (React Query)
- **HTTP client:** Axios
- **Design:** Clean White + Blue palette (#2563eb primary, #f8fafc background, #0f172a text)

## Project Structure

```
tsa-dev/
├── tsa-app/          # Existing mobile app (Expo)
├── tsa-api-go/       # Existing Go API
└── tsa-admin/        # NEW — Admin web panel
    ├── src/
    │   ├── api/              # API client + endpoint functions
    │   │   ├── client.ts     # Axios instance, interceptors, token handling
    │   │   ├── auth.ts       # login, logout
    │   │   ├── users.ts      # user CRUD, role management
    │   │   ├── products.ts   # products, featured, approvals
    │   │   ├── categories.ts # category CRUD
    │   │   ├── orders.ts     # order management
    │   │   ├── deposits.ts   # deposit request approvals
    │   │   └── verification.ts # identity verification approvals
    │   ├── components/
    │   │   ├── ui/           # Shadcn components (auto-generated)
    │   │   ├── layout/       # Sidebar, Header, Breadcrumbs
    │   │   └── shared/       # DataTable, StatusBadge, StatCard, etc.
    │   ├── pages/            # Route-level page components
    │   │   ├── auth/         # Login
    │   │   ├── dashboard/
    │   │   ├── users/
    │   │   ├── products/
    │   │   ├── categories/
    │   │   ├── orders/
    │   │   ├── deposits/
    │   │   ├── verifications/
    │   │   └── settings/
    │   ├── hooks/            # useAuth, usePermission, etc.
    │   ├── lib/              # utils, cn(), constants
    │   ├── types/            # TypeScript interfaces
    │   ├── contexts/         # AuthContext
    │   └── router.tsx        # React Router config with route guards
    ├── index.html
    ├── vite.config.ts
    ├── tailwind.config.ts
    ├── tsconfig.json
    └── package.json
```

## RBAC — Roles & Permissions

Three admin-panel roles (the "user" role from the mobile app cannot log into the admin panel — blocked at login):

| Feature | Support | Admin | Super Admin |
|---|---|---|---|
| View dashboard stats | Read-only | Full | Full |
| View users | Read-only | Read/Edit | Full CRUD |
| Manage roles | — | — | Full |
| Product approvals | View | Approve/Reject | Full |
| Category management | View | Create/Edit | Full CRUD + Delete |
| Order management | View/Update status | Full | Full |
| Deposit approvals | View | Approve/Reject | Full |
| Verification approvals | View | Approve/Reject | Full |
| Settings & config | — | — | Full |

### Implementation

- **Backend:** Extend `AdminAuth` middleware to check granular permissions derived from user role. Each route declares its required permission.
- **Frontend:** `usePermission(permission)` hook checks logged-in user's role against a shared permission map constant. Used for:
  - Route guards — redirect to dashboard if no access
  - UI elements — hide/disable actions the user can't perform

## Layout & Navigation

### Shell

- **Collapsible sidebar** (240px expanded, icon-only collapsed)
- **Top header** with breadcrumbs and page title
- **Global search** — deferred to a future iteration; placeholder input in header for now
- **Notification bell** — deferred to a future iteration; placeholder icon in header for now
- **User profile** at bottom of sidebar showing name and role
- **Viewport:** Desktop-first, minimum 1024px viewport. Tablet/mobile not required.

### Navigation Groups

**Overview:**
- Dashboard

**Management:**
- Users
- Products
- Categories
- Orders

**Approvals** (with badge counts for pending items):
- Advert Requests
- Deposits
- Verifications

**System:**
- Settings (super admin only)

## Pages

### Dashboard
- **Stats cards:** Total users, total products, pending approvals, revenue — fetched from `GET /api/admin/stats`
- **Recent orders table:** Last 10 orders
- **Pending actions widget:** Counts linking to each approval page
- Auto-refreshes every 60 seconds

### Users
- Paginated DataTable with search, filter by role, filter by status
- Row click → user detail (slide-over or page)
- Actions: edit role (super admin only), toggle active/inactive, view verification status
- API: `GET /api/users` (exists), new `PATCH /api/users/:id/role`

### Products
- Paginated DataTable with search, filter by category/type/status
- Inline quick actions: view, edit, delete
- Row click → product detail with images, seller info
- API: existing product endpoints

### Categories
- Tree view showing parent → children hierarchy
- Drag-to-reorder (updates `order` field)
- Inline edit, create modal, delete with confirmation
- API: existing category endpoints (routes need wiring in `routes.go`), new `PATCH /products/category/reorder` for bulk reorder

### Orders (new feature)

Orders are created when a user checks out a cart via `POST /api/cart/checkout` (exists). The admin panel provides read + status management only.

**Order data model:**
- `id`, `buyer` (user ref), `seller` (user ref), `items[]` (product ref, quantity, unit price), `total`, `currency`, `status`, `shippingAddress`, `notes`, `createdAt`, `updatedAt`
- **Status state machine:** `pending` → `processing` → `shipped` → `completed`. Any state can transition to `cancelled`. No backwards transitions.

**Page:**
- Paginated DataTable: order ID, buyer, seller, items, total, status, date
- Filter by status: pending, processing, completed, shipped, cancelled
- Row click → order detail with item breakdown, payment info, status timeline
- Actions: update status (next valid state only), add notes
- API: new `GET /api/orders` (admin), `PATCH /api/orders/:id/status`

### Advert Requests
- List of non-featured products awaiting approval
- Each card: product preview, seller info, submission date
- Actions: approve (toggle featured) or reject (update status)
- API: `GET /products/non-featured`, `PATCH /products/:id/featured` (handlers exist, routes need wiring)

### Deposits

Deposits are fiat funding requests from users to load their marketplace balance. A user submits a deposit request (amount + currency + proof of payment) via the mobile app. Admins review and approve/reject.

- DataTable: user, amount, currency (NGN), status, timestamp, proof of payment link
- Actions: approve/reject with optional note
- API: new `GET /api/deposits` (admin), `PATCH /api/deposits/:id/status`

### Verifications
- List of pending identity verifications
- Shows submitted documents (ID photo, facial photo)
- Actions: approve/reject with reason
- API: `POST /api/verification/approve/:id`, `POST /api/verification/reject/:id` (already exist)

### Settings (super admin only)
- Role management: view all admins, change roles
- App configuration (future)

## Auth Flow

1. Admin visits `/login` — email + password form
2. Calls `POST /api/auth/login` → returns JWT + role
3. If role is not `superadmin`, `admin`, or `support`, show "Access denied"
4. Store token in `sessionStorage` (survives page refresh, cleared on tab close); optional "remember me" checkbox persists to `localStorage` instead (survives browser close)
5. Redirect to `/dashboard`

### Session Management
- Axios interceptor catches 401 → clears auth state → redirects to `/login`
- Token attached via `Authorization: Bearer <token>` header on all requests

### Error Handling

All API errors are handled consistently:

- **401 Unauthorized** → clear auth, redirect to `/login`
- **403 Forbidden** → toast: "You don't have permission to perform this action"
- **422 Validation** → display field-level errors from `response.errors[]` on forms, or toast for non-form requests
- **500 Server Error** → toast: "Something went wrong. Please try again."
- **Network failure / timeout** → toast: "Network error. Check your connection."
- **Rate limited (429)** → toast: "Too many requests. Please wait."

Tanstack Query config: 1 retry on failure with exponential backoff, staleTime of 30 seconds for list queries.

### API Client
- Shared Axios instance in `api/client.ts` with auth interceptor
- Each domain module (`users.ts`, `products.ts`, etc.) exports typed async functions
- All GET requests use Tanstack Query for caching, background refetch, loading/error states
- Mutations via `useMutation` with cache invalidation on success

## Shared UI Patterns

All list/table pages follow a consistent pattern:

- **Shadcn DataTable** with sorting, pagination, column visibility toggles
- **Filter bar** with search input + dropdown filters
- **Toast notifications** (Shadcn Sonner) for success/error feedback
- **Confirmation dialogs** before destructive actions (delete, reject)
- **Loading skeletons** during initial data fetch
- **Empty states** with clear messaging and action buttons

## Backend Changes Required

### Route Wiring (handlers exist, routes missing in `routes.go`)
- `GET /products/non-featured` → `GetNonFeaturedProducts`
- `PATCH /products/:id/featured` → `ToggleFeatured`
- `GET /products/category/all` → `GetAllCategories`
- `POST /products/category` → `CreateCategory`
- `PUT /products/category/:id` → `UpdateCategory`
- `DELETE /products/category/:id` → `DeleteCategory`

### New Endpoints
- `GET /api/admin/stats` — aggregated dashboard statistics
- `GET /api/orders` (admin) — paginated order list with filters
- `PATCH /api/orders/:id/status` — update order status
- `PATCH /api/users/:id/role` — change user role (super admin only)
- `GET /api/deposits` (admin) — paginated deposit requests
- `PATCH /api/deposits/:id/status` — approve/reject deposit
- `PATCH /products/category/reorder` — bulk reorder categories (accepts ordered list of IDs)

### API Conventions

**Pagination:** All list endpoints use `?page=1&limit=20` query params and return:
```json
{
  "success": true,
  "data": [],
  "pagination": {
    "page": 1,
    "limit": 20,
    "total": 150,
    "totalPages": 8
  }
}
```
This matches the existing pattern in `GetMarketplaceProducts`.

### Middleware Changes
- Add CORS config for admin domain in `middleware/cors.go`
- Extend `AdminAuth` middleware to support per-route permission checks based on role

## Design Tokens

| Token | Value |
|---|---|
| Primary | `#2563eb` (Blue 600) |
| Primary hover | `#1d4ed8` (Blue 700) |
| Background | `#f8fafc` (Slate 50) |
| Surface | `#ffffff` |
| Border | `#e2e8f0` (Slate 200) |
| Text primary | `#0f172a` (Slate 900) |
| Text secondary | `#64748b` (Slate 500) |
| Text muted | `#94a3b8` (Slate 400) |
| Success | `#16a34a` (Green 600) |
| Warning | `#f59e0b` (Amber 500) |
| Danger | `#dc2626` (Red 600) |

## Deployment

- Static SPA — deployable to Vercel, Netlify, Cloudflare Pages, or any static host
- Environment variable `VITE_API_BASE_URL` points to the Go API
- Independent release cycle from mobile app and backend
