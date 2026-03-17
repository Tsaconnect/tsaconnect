# Agent Prompt: Update Mobile Checkout for Escrow Flow

## Context

You are updating the TSA Connect Expo React Native app (`tsa-app/`) to use the new escrow-based checkout. The existing checkout screen calls `cartService.checkout()` directly — you need to replace this with: create orders → prepare escrow txs → sign locally → submit signed txs. You also need to add an order list/detail screen for buyers to track orders and confirm receipt.

## Read These Files First

1. `tsa-app/CLAUDE.md` — project architecture, commands, patterns
2. `tsa-app/app/(dashboard)/(tabs)/(home)/checkout/index.tsx` — existing checkout screen (needs major update)
3. `tsa-app/app/(dashboard)/orderlist.tsx` — existing order list (thin wrapper, needs update)
4. `tsa-app/components/orders/orderList.tsx` — existing order list component
5. `tsa-app/services/walletApi.ts` — wallet API calls (prepareTx, submitTx pattern)
6. `tsa-app/services/wallet.ts` — local wallet: `signTransaction()`, `getPrivateKey()`
7. `tsa-app/components/services/cart.ts` — current cart service (getCartSummary, checkout)
8. `tsa-app/constants/api/config.ts` — API base URL
9. `tsa-app/constants/chains.ts` — chain configs (Sonic testnet = chainId 14601)
10. `tsa-app/constants/theme.js` — COLORS, SIZES, FONTS design tokens
11. `tsa-app/AuthContext/AuthContext.tsx` — auth context (currentUser, token)
12. `tsa-api-go/internal/handlers/checkout_handler.go` — backend API to understand request/response shapes
13. `tsa-api-go/internal/models/order.go` — Order model

## Backend API Endpoints

Base URL: same as existing API (see `constants/api/config.ts`)

### Checkout Flow
- `POST /api/orders` — Create orders from cart
  - Body: `{token: "USDC", buyerCity, buyerState, buyerCountry}`
  - Returns: `{orders: Order[]}`

- `POST /api/orders/:id/prepare-escrow` — Get unsigned txs
  - Returns: `{approveTx: UnsignedTx, createOrderTx: UnsignedTx, contractOrderId: string}`

- `POST /api/orders/:id/submit-escrow` — Submit signed txs
  - Body: `{approveTxHash: string, escrowTxHash: string}`
  - Returns: updated order

### Order Management (Buyer)
- `GET /api/orders` — List user's orders (buyer or seller)
  - Query: `?page=1&limit=20&status=escrowed&role=buyer`
  - Returns: `{orders: Order[], pagination: {...}}`

- `GET /api/orders/:id` — Order detail

- `POST /api/orders/:id/prepare-confirm` — Get unsigned confirm receipt tx
  - Returns: `{confirmTx: UnsignedTx}`

- `POST /api/orders/:id/submit-confirm` — Submit signed confirm tx
  - Body: `{txHash: string}`

- `POST /api/orders/:id/request-refund` — Request refund (escrowed orders)
  - Returns: `{refundTx: UnsignedTx}` (for escrowed) or status update (for delivered)

- `POST /api/orders/:id/deliver` — Seller marks delivered
  - Body: `{deliveryProofUrl: string}`

- `GET /api/orders/shipping-estimate` — Get shipping cost
  - Query: `?productId=X&buyerCity=Y&buyerState=Z&buyerCountry=W`

### UnsignedTx Shape (from backend)
```typescript
interface UnsignedTx {
  to: string;
  value: string;
  data: string;        // hex-encoded calldata
  nonce: number;
  gasPrice: string;
  gasLimit: number;
  chainId: string;
}
```

### Order Statuses
```
pending_payment → escrowed → delivered → completed
                                      → refund_requested → refunded
                → cancelled
```

## What To Build

### 1. Order API Service (`services/orderApi.ts`)

New file. Follow the same pattern as `walletApi.ts` (fetch-based with `getAuthHeaders()`).

```typescript
export async function createOrders(token: string, buyerCity?: string, buyerState?: string, buyerCountry?: string): Promise<ApiResponse<{orders: Order[]}>>
export async function getOrders(params: {page?: number, limit?: number, status?: string, role?: string}): Promise<ApiResponse<{orders: Order[], pagination: any}>>
export async function getOrderById(id: string): Promise<ApiResponse<Order>>
export async function prepareEscrow(orderId: string): Promise<ApiResponse<{approveTx: UnsignedTx, createOrderTx: UnsignedTx, contractOrderId: string}>>
export async function submitEscrow(orderId: string, approveTxHash: string, escrowTxHash: string): Promise<ApiResponse<Order>>
export async function prepareConfirm(orderId: string): Promise<ApiResponse<{confirmTx: UnsignedTx}>>
export async function submitConfirm(orderId: string, txHash: string): Promise<ApiResponse<Order>>
export async function requestRefund(orderId: string): Promise<ApiResponse<any>>
export async function getShippingEstimate(productId: string, buyerCity: string, buyerState: string, buyerCountry: string): Promise<ApiResponse<{zone: string, shippingCost: number}>>
```

### 2. Update Checkout Screen (`app/(dashboard)/(tabs)/(home)/checkout/index.tsx`)

Replace the current `handleCheckout` which calls `cartService.checkout()`.

**New flow** (multi-step with progress indicator):

**Step 1: Review & Select Token**
- Show cart items (existing UI — keep it)
- Add token selector: USDC / USDT / MCGP (pill buttons)
- Show shipping address (existing — keep it)
- Show shipping estimate (call `getShippingEstimate` per product)
- "Create Order" button

**Step 2: Sign & Pay (per order)**
- Show order summary (product, shipping, platform fee, total)
- Show token and amount to pay
- "Approve & Pay" button → multi-step process:
  1. Call `prepareEscrow(orderId)` → get unsigned txs
  2. Sign approve tx locally via `signTransaction(approveTx)`
  3. Broadcast approve tx (direct RPC via ethers provider, get txHash)
  4. Sign createOrder tx locally via `signTransaction(createOrderTx)`
  5. Broadcast createOrder tx (direct RPC, get txHash)
  6. Call `submitEscrow(orderId, approveTxHash, escrowTxHash)`
- Show loading states for each step: "Approving token...", "Creating escrow...", "Confirming..."
- On success: show checkmark, move to next order or completion

**Step 3: Confirmation**
- "Orders Created Successfully" with order IDs
- "View Orders" button → navigate to order list
- "Continue Shopping" button → navigate to home

**Important**: If the cart has items from multiple sellers, Step 1 creates multiple orders. Step 2 must iterate through each order (show progress: "Order 1 of 3").

**Transaction Signing Pattern** (follow `wallet/send.tsx`):
```typescript
import { signTransaction } from '@/services/wallet';
import { getProvider } from '@/services/wallet';

// 1. Get unsigned tx from backend
const { data } = await prepareEscrow(orderId);
const unsignedTx = data.approveTx;

// 2. Sign locally
const signedTx = await signTransaction({
  to: unsignedTx.to,
  value: unsignedTx.value,
  data: '0x' + unsignedTx.data,  // backend returns hex without 0x prefix
  nonce: unsignedTx.nonce,
  gasPrice: unsignedTx.gasPrice,
  gasLimit: unsignedTx.gasLimit,
  chainId: parseInt(unsignedTx.chainId),
});

// 3. Broadcast
const provider = getProvider('sonic');
const txResponse = await provider.broadcastTransaction(signedTx);
const receipt = await txResponse.wait();
const txHash = receipt.hash;
```

### 3. Update Order List Screen (`app/(dashboard)/orderlist.tsx` + `components/orders/orderList.tsx`)

Replace the existing order list with escrow-aware version.

**Features**:
- Tab filter: All / Pending / Escrowed / Delivered / Completed / Refunded
- Each order card shows:
  - Product name (if available) or "Order #abc123..."
  - Token + total amount (formatted from wei)
  - Status badge (colored)
  - Date
  - Tap to navigate to order detail

### 4. New Order Detail Screen (`app/(dashboard)/orderdetail.tsx`)

New screen. Shows full order info with action buttons based on status.

**Sections**:
- Status badge (large, colored)
- Product info (name, quantity, image if available)
- Amounts (product, shipping, platform fee, total — formatted from wei)
- Shipping zone
- Blockchain info (contract order ID, tx hashes — truncated with copy)
- Timeline (created, escrowed, delivered, confirmed dates)

**Action Buttons** (based on status + user role):
- `escrowed` (buyer): Nothing to do yet, show "Waiting for delivery"
- `delivered` (buyer): "Confirm Receipt" button → calls `prepareConfirm` → sign → broadcast → `submitConfirm`
- `delivered` (buyer): "Request Refund" button → calls `requestRefund`
- `escrowed` (buyer): "Request Refund" button → calls `requestRefund` → sign → broadcast
- `completed`: Show "Completed" with checkmark

**Confirm Receipt Flow** (same signing pattern as checkout):
1. User taps "Confirm Receipt"
2. Show confirmation dialog: "This will release funds to the seller. Are you sure?"
3. Call `prepareConfirm(orderId)` → get unsigned tx
4. Sign locally → broadcast → get txHash
5. Call `submitConfirm(orderId, txHash)`
6. Show success

### 5. Add Route for Order Detail

In `app/(dashboard)/_layout.tsx`, add the order detail screen to the drawer navigator so it's accessible.

Alternatively, add it as a stack screen within the existing layout. Follow the pattern used by other detail screens (e.g., product details).

### 6. Navigation Updates

- Checkout success → "View Orders" → navigate to `/(dashboard)/orderlist`
- Order list → tap order → navigate to order detail
- Drawer menu already has "Orders" item pointing to `orderlist`

## Design Guidelines

Follow the existing app's visual style:
- **Colors**: Primary `#8B5A2B` (brown), background gradient `['#FDF8F3', '#FAF0E6']`, accent `#D9B68B`
- **Cards**: White background, `borderColor: '#E8D5C0'`, `borderRadius: 16`, subtle shadow
- **Buttons**: `LinearGradient` with `['#8B5A2B', '#6B4226']`
- **Icons**: Ionicons
- **Typography**: Section titles `fontSize: 16, fontWeight: '600', color: '#4A2C1A'`
- **Status badges**: Colored pills matching status

## Wei Amount Formatting Helper

Add a utility function (can go in `services/orderApi.ts` or a new `utils/format.ts`):

```typescript
export function formatTokenAmount(weiStr: string, token: string): string {
  const decimals = token === 'MCGP' ? 18 : 6;
  try {
    const value = BigInt(weiStr);
    const divisor = BigInt(10 ** decimals);
    const whole = value / divisor;
    const frac = value % divisor;
    const fracStr = frac.toString().padStart(decimals, '0').slice(0, token === 'MCGP' ? 4 : 2);
    return `${whole}.${fracStr} ${token}`;
  } catch {
    return `${weiStr} ${token}`;
  }
}
```

## What NOT To Do

- Do NOT modify the backend API
- Do NOT modify smart contracts
- Do NOT change the wallet service (`services/wallet.ts`) — it's already complete
- Do NOT add new npm dependencies unless absolutely necessary
- Do NOT change the existing cart flow (adding to cart, cart page) — only change checkout
- Do NOT implement seller-side screens (delivery marking) — that's a separate task
- Do NOT use Redux or Zustand — use existing Context pattern or local state

## Verification

```bash
cd tsa-app
npx expo start       # Must start without errors
```

Test on simulator/emulator:
1. Add product to cart → go to checkout → see token selector
2. Select USDC → see shipping estimate → create order
3. Sign approve tx → sign escrow tx → see "Order Escrowed" confirmation
4. Go to order list → see the new order with "Escrowed" status
5. Tap order → see order detail with amounts and blockchain info
