# Agent Prompt: Update Admin Orders Page for Escrow Flow

## Context

You are updating the TSA Connect admin dashboard (React + Vite + shadcn/ui) to support the new escrow-based order system. The admin panel is at `tsa-admin/`. The backend checkout API is already built and merged — your job is to update the frontend to match the new order model and add dispute resolution.

## Read These Files First

1. `tsa-admin/src/types/index.ts` — current types (Order needs updating)
2. `tsa-admin/src/lib/constants.ts` — status labels/colors (needs new statuses)
3. `tsa-admin/src/api/orders.ts` — current API functions (needs new endpoints)
4. `tsa-admin/src/pages/orders/index.tsx` — orders list page
5. `tsa-admin/src/pages/orders/[id].tsx` — order detail page (needs major update)
6. `tsa-admin/src/pages/orders/columns.tsx` — table columns (needs new fields)
7. `tsa-admin/src/components/shared/data-table.tsx` — reusable table component
8. `tsa-admin/src/components/shared/status-badge.tsx` — status badge component
9. `tsa-admin/src/lib/permissions.ts` — permission matrix
10. `tsa-admin/src/app.tsx` — router setup
11. `tsa-api-go/internal/handlers/checkout_handler.go` — backend API to understand response shapes
12. `tsa-api-go/internal/models/order.go` — Order model with new fields

## Backend API Endpoints

The admin uses these endpoints:

### Existing (update to match new response shape)
- `GET /api/admin/orders` — list all orders (paginated, filterable by status)
- `GET /api/orders/:id` — order detail (admin can view any order)

### New
- `POST /api/admin/orders/:id/resolve` — resolve dispute `{refundBuyer: bool}`

### Order Statuses (NEW — replaces old ones)
```
pending_payment → escrowed → delivered → completed
                                      → refund_requested → refunded
                → cancelled
escrowed → refund_requested → refunded (via admin resolve)
```

## What To Build

### 1. Update Order Type (`src/types/index.ts`)

Replace the existing `Order` and `OrderItem` interfaces:

```typescript
export interface Order {
  id: string;
  buyerId: string;
  sellerId: string;
  productId: string;
  quantity: number;
  token: string;              // "USDC" | "USDT" | "MCGP"
  productAmount: string;      // wei string
  shippingAmount: string;     // wei string
  platformFee: string;        // wei string
  totalAmount: string;        // wei string
  shippingZone: string;       // same_city | same_state | same_country | international
  status: OrderStatus;
  contractOrderId?: string;   // bytes32 hex
  escrowTxHash?: string;
  approveTxHash?: string;
  releaseTxHash?: string;
  buyerUpline?: string;
  deliveryProofUrl?: string;
  buyerConfirmedAt?: string;
  sellerDeliveredAt?: string;
  escrowExpiresAt?: string;
  notes?: string;
  createdAt: string;
  updatedAt: string;
}

export type OrderStatus =
  | 'pending_payment' | 'escrowed' | 'delivered'
  | 'completed' | 'refund_requested' | 'refunded' | 'cancelled';
```

Remove `OrderItem` interface (orders are now single-product).

### 2. Update Constants (`src/lib/constants.ts`)

Replace `ORDER_STATUS_LABELS` and `ORDER_STATUS_COLORS`:

```typescript
export const ORDER_STATUS_LABELS: Record<string, string> = {
  pending_payment: 'Pending Payment',
  escrowed: 'Escrowed',
  delivered: 'Delivered',
  completed: 'Completed',
  refund_requested: 'Refund Requested',
  refunded: 'Refunded',
  cancelled: 'Cancelled',
};

export const ORDER_STATUS_COLORS: Record<string, string> = {
  pending_payment: 'bg-amber-100 text-amber-700',
  escrowed: 'bg-blue-100 text-blue-700',
  delivered: 'bg-purple-100 text-purple-700',
  completed: 'bg-green-100 text-green-700',
  refund_requested: 'bg-orange-100 text-orange-700',
  refunded: 'bg-red-100 text-red-700',
  cancelled: 'bg-slate-100 text-slate-700',
};
```

### 3. Update API Functions (`src/api/orders.ts`)

```typescript
// Admin: list all orders
export async function getAdminOrders(params: { page?: number; limit?: number; status?: string; search?: string }) {
  const { data } = await client.get<ApiResponse<{ orders: Order[]; pagination: PaginationMeta }>>('/admin/orders', { params });
  return data;
}

// Get single order (works for admin)
export async function getOrderById(id: string) {
  const { data } = await client.get<ApiResponse<Order>>(`/orders/${id}`);
  return data;
}

// Admin: resolve dispute
export async function resolveDispute(id: string, refundBuyer: boolean) {
  const { data } = await client.post<ApiResponse>(`/admin/orders/${id}/resolve`, { refundBuyer });
  return data;
}
```

Remove the old `updateOrderStatus` function.

### 4. Update Table Columns (`src/pages/orders/columns.tsx`)

New columns:
- **Order ID** — truncated UUID
- **Token** — USDC/USDT/MCGP badge
- **Total** — formatted from wei string (divide by 10^decimals, show as human-readable)
- **Status** — StatusBadge with new colors
- **Shipping Zone** — same_city/same_state etc.
- **Date** — createdAt formatted

Add a helper to format wei amounts:
```typescript
function formatWei(weiStr: string, token: string): string {
  const decimals = token === 'MCGP' ? 18 : 6;
  const value = BigInt(weiStr);
  const divisor = BigInt(10 ** decimals);
  const whole = value / divisor;
  const frac = value % divisor;
  const fracStr = frac.toString().padStart(decimals, '0').slice(0, token === 'MCGP' ? 4 : 2);
  return `${whole}.${fracStr}`;
}
```

### 5. Update Orders List Page (`src/pages/orders/index.tsx`)

- Use `getAdminOrders` instead of `getOrders`
- Update status filter dropdown with new statuses
- Add search input (searches by order ID)
- Keep pagination

### 6. Update Order Detail Page (`src/pages/orders/[id].tsx`)

This is the biggest change. The page should show:

**Status Card** (top):
- Large status badge
- Escrow expiry countdown (if escrowed, show "Expires in X days")

**Order Info Card**:
- Product ID (link if possible)
- Quantity
- Token (USDC/USDT/MCGP)
- Shipping zone

**Amounts Card**:
- Product amount (formatted from wei)
- Shipping amount
- Platform fee (10% or 0% for MCGP)
- Total amount (bold)

**Parties Card**:
- Buyer ID
- Seller ID
- Buyer upline (if present)

**Blockchain Card** (only show fields that exist):
- Contract Order ID (truncated hex, copy button)
- Approve Tx Hash (link to Sonic explorer)
- Escrow Tx Hash (link to Sonic explorer)
- Release Tx Hash (link to Sonic explorer)

**Timeline Card**:
- Created at
- Seller delivered at (with delivery proof image if URL exists)
- Buyer confirmed at
- Escrow expires at

**Actions Card** (admin only):
- If status is `refund_requested`: Show "Resolve Dispute" section with two buttons:
  - "Refund Buyer" (destructive variant) — calls `resolveDispute(id, true)`
  - "Release to Seller" (default variant) — calls `resolveDispute(id, false)`
- Use a `ConfirmDialog` before executing either action

**Remove**: The old items list and the old status transition buttons (admin no longer manually transitions statuses — the escrow flow handles it).

### 7. Sonic Explorer Links

Use `https://testnet.sonicscan.org/tx/{txHash}` for transaction links. Display as truncated hash with external link icon.

## Patterns To Follow

- Use `useQuery` / `useMutation` from TanStack Query (same as existing pages)
- Use `toast.success()` / `toast.error()` from sonner for notifications
- Use `usePermission('orders.resolve_dispute')` for admin actions (add this permission to `permissions.ts` for admin and super_admin roles)
- Use existing shared components: `DataTable`, `PageHeader`, `StatusBadge`, `ConfirmDialog`
- Use shadcn/ui `Card`, `Button`, `Select`, `Separator`, `Skeleton`
- Use Lucide icons

## What NOT To Do

- Do NOT modify the backend API
- Do NOT add new npm dependencies (everything needed is already installed)
- Do NOT create new shared components unless absolutely necessary — reuse existing ones
- Do NOT add wallet signing or blockchain interaction — admin only views data and resolves disputes
- Do NOT remove other admin pages

## Verification

```bash
cd tsa-admin
npm run build    # Must compile with no TypeScript errors
npm run dev      # Must render without runtime errors
```

Check:
1. Orders list loads with new status filter options
2. Order detail shows all new fields (amounts, blockchain, timeline)
3. Dispute resolution dialog works on `refund_requested` orders
4. Wei amounts display correctly (e.g., "100.00 USDC" not "100000000")
