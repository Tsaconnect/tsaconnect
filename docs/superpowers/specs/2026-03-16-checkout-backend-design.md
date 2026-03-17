# Sub-project 5: Marketplace Checkout Backend

## Overview

Build the Go API backend for the marketplace checkout flow with smart contract escrow integration. The mobile app signs transactions locally; the server prepares unsigned txs, verifies on-chain state, and tracks order lifecycle.

## Decisions

- **Tx signing**: Server prepares unsigned txs, mobile signs locally (same pattern as existing wallet `prepare-tx` → `submit-tx`)
- **Shipping zones**: Pull seller location from User profile (`City`, `State`, `Country` fields already exist). Fallback to `same_country` rate if location missing.
- **Single escrow contract**: One deployed `ProductEscrow` at a known address (not per-order deployment)
- **Chain**: Sonic testnet only for MVP

## Deployed Contracts (Sonic Testnet)

```
MockUSDC:       0x9f8AfF2706F52Ddb02921E245ec95Ade96767379
MockMCGP:       0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7
ProductEscrow:  0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d
ServiceContact: 0x3d761F72f4369e072767E830eE8Ce4c3A2144e6f
```

## Model Changes

### Extend Order model (`internal/models/order.go`)

Replace the existing Order model with escrow-aware fields:

```go
type Order struct {
    ID                uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    BuyerID           uuid.UUID      `gorm:"type:uuid;index;not null" json:"buyerId"`
    SellerID          uuid.UUID      `gorm:"type:uuid;index;not null" json:"sellerId"`
    ProductID         uuid.UUID      `gorm:"type:uuid;not null" json:"productId"`
    Quantity          int            `gorm:"not null;default:1" json:"quantity"`
    Token             string         `gorm:"not null" json:"token"`                      // "USDC", "MCGP"
    ProductAmount     string         `gorm:"type:varchar(78);not null" json:"productAmount"` // wei string
    ShippingAmount    string         `gorm:"type:varchar(78);default:'0'" json:"shippingAmount"`
    PlatformFee       string         `gorm:"type:varchar(78);default:'0'" json:"platformFee"`
    TotalAmount       string         `gorm:"type:varchar(78);not null" json:"totalAmount"`
    ShippingZone      string         `json:"shippingZone"`                               // same_city, same_state, same_country, international
    ContractOrderID   string         `gorm:"type:varchar(66);index" json:"contractOrderId"` // bytes32 hex
    EscrowTxHash      string         `json:"escrowTxHash,omitempty"`
    ApproveTxHash     string         `json:"approveTxHash,omitempty"`
    ReleaseTxHash     string         `json:"releaseTxHash,omitempty"`
    BuyerUpline       string         `json:"buyerUpline,omitempty"`                      // upline wallet address
    Status            string         `gorm:"not null;default:'pending_payment';index" json:"status"`
    DeliveryProofURL  string         `json:"deliveryProofUrl,omitempty"`
    BuyerConfirmedAt  *time.Time     `json:"buyerConfirmedAt,omitempty"`
    SellerDeliveredAt *time.Time     `json:"sellerDeliveredAt,omitempty"`
    EscrowExpiresAt   *time.Time     `json:"escrowExpiresAt,omitempty"`
    CreatedAt         time.Time      `json:"createdAt"`
    UpdatedAt         time.Time      `json:"updatedAt"`
}
```

**Amounts as strings**: All amounts stored as wei strings (not float64) to avoid precision loss with big numbers. The API converts between human-readable and wei using token decimals.

### Order statuses

```
pending_payment → escrowed → delivered → completed
                                      → refund_requested → refunded
                         → refund_requested → refunded
                → cancelled
```

### Add shipping fields to Product model

```go
// Add to existing Product struct
ShippingSameCity      float64 `gorm:"default:0" json:"shippingSameCity"`
ShippingSameState     float64 `json:"shippingSameState,omitempty"`
ShippingSameCountry   float64 `json:"shippingSameCountry,omitempty"`
ShippingInternational float64 `json:"shippingInternational,omitempty"`
```

## New: Escrow Service (`internal/services/escrow_service.go`)

Wraps ProductEscrow contract interactions using go-ethereum ABI encoding. Methods:

- `PrepareCreateOrder(orderId, buyer, seller, token, productAmount, shippingAmount, upline)` → returns `UnsignedTx` JSON
- `PrepareApprove(token, owner, amount)` → returns `UnsignedTx` JSON (reuse existing `PrepareERC20Approve`)
- `PrepareConfirmReceipt(orderId, buyer)` → returns `UnsignedTx`
- `PrepareRequestRefund(orderId, buyer)` → returns `UnsignedTx`
- `PrepareCancelOrder(orderId, seller)` → returns `UnsignedTx`
- `PrepareAdminResolve(orderId, refundBuyer, admin)` → returns `UnsignedTx`
- `VerifyEscrowCreated(txHash)` → parses receipt logs for `OrderCreated` event, returns contractOrderId

The ABI for ProductEscrow should be compiled from the Solidity contract and stored as a JSON file at `internal/blockchain/abi/ProductEscrow.json`.

## New Config

Add to `config.go`:
```go
ProductEscrowAddress string  // env: PRODUCT_ESCROW_ADDRESS
ServiceContactAddress string // env: SERVICE_CONTACT_ADDRESS
```

Add to `.env`:
```
PRODUCT_ESCROW_ADDRESS=0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d
SERVICE_CONTACT_ADDRESS=0x3d761F72f4369e072767E830eE8Ce4c3A2144e6f
USDC_TOKEN_ADDRESS=0x9f8AfF2706F52Ddb02921E245ec95Ade96767379
MCGP_TOKEN_ADDRESS=0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7
```

## API Endpoints

### Checkout Handler (`internal/handlers/checkout_handler.go`)

```
POST   /api/orders                           — Create order(s) from cart
POST   /api/orders/:id/prepare-escrow        — Prepare approve + createOrder unsigned txs
POST   /api/orders/:id/submit-escrow         — Submit signed escrow tx hash, verify on-chain
POST   /api/orders/:id/deliver               — Seller marks delivered (upload proof)
POST   /api/orders/:id/prepare-confirm       — Prepare unsigned confirmReceipt tx
POST   /api/orders/:id/submit-confirm        — Submit signed confirm tx, release funds
POST   /api/orders/:id/request-refund        — Buyer requests refund
POST   /api/orders/:id/cancel                — Seller cancels (before delivery)
GET    /api/orders                            — List user's orders (buyer or seller, filtered by status)
GET    /api/orders/:id                        — Order detail with escrow status
GET    /api/orders/shipping-estimate          — Calculate shipping cost by zone
POST   /api/admin/orders/:id/resolve          — Admin resolves dispute
```

### Checkout Flow (API perspective)

1. `POST /api/orders` — receives `{cartId, token, buyerCity, buyerState, buyerCountry}`. Groups cart items by seller, creates one Order per seller. Calculates shipping zone (buyer location vs seller's User profile), platform fee (10%, 0% for MCGP), total. Returns order summaries.

2. `POST /api/orders/:id/prepare-escrow` — returns two unsigned txs: `{approveTx, createOrderTx}`. The approve tx allows the escrow contract to spend buyer's tokens. The createOrder tx calls `ProductEscrow.createOrder()`.

3. `POST /api/orders/:id/submit-escrow` — receives `{approveTxHash, escrowTxHash}`. Verifies both txs on-chain (receipt status = success). Parses `OrderCreated` event to extract `contractOrderId`. Updates order status to `escrowed`, sets `escrowExpiresAt = now + 30 days`.

4. `POST /api/orders/:id/deliver` — seller uploads delivery proof image (Cloudinary), marks delivered.

5. `POST /api/orders/:id/prepare-confirm` → returns unsigned `confirmReceipt` tx.

6. `POST /api/orders/:id/submit-confirm` → receives signed tx hash, verifies on-chain, updates to `completed`.

7. `POST /api/orders/:id/request-refund` → buyer requests refund. If before delivery, prepares unsigned `requestRefund` tx. If after delivery, sets status to `refund_requested` for admin review.

8. `POST /api/admin/orders/:id/resolve` → admin resolves dispute, prepares and submits `adminResolve` tx.

### Shipping Zone Detection

```go
func detectShippingZone(buyerCity, buyerState, buyerCountry, sellerCity, sellerState, sellerCountry string) string {
    if buyerCountry == "" || sellerCountry == "" {
        return "same_country" // fallback
    }
    if !strings.EqualFold(buyerCountry, sellerCountry) {
        return "international"
    }
    if !strings.EqualFold(buyerState, sellerState) {
        return "same_country"
    }
    if !strings.EqualFold(buyerCity, sellerCity) {
        return "same_state"
    }
    return "same_city"
}
```

### Fee Calculation

```go
func calculatePlatformFee(productAmount *big.Int, token string, mcgpTokenAddress string) *big.Int {
    if token == mcgpTokenAddress {
        return big.NewInt(0) // 0% for MCGP
    }
    // 10% = productAmount * 1000 / 10000
    fee := new(big.Int).Mul(productAmount, big.NewInt(1000))
    fee.Div(fee, big.NewInt(10000))
    return fee
}
```

## Route Registration

Restructure order routes: remove admin-only restriction, add auth for buyer/seller endpoints, keep admin routes separate.

```go
// Order routes (authenticated users)
orderGroup := api.Group("/orders")
orderGroup.Use(auth)
{
    orderGroup.POST("", h.CreateOrderFromCart)
    orderGroup.GET("", h.GetUserOrders)
    orderGroup.GET("/:id", h.GetOrderDetail)
    orderGroup.GET("/shipping-estimate", h.GetShippingEstimate)
    orderGroup.POST("/:id/prepare-escrow", h.PrepareEscrow)
    orderGroup.POST("/:id/submit-escrow", h.SubmitEscrow)
    orderGroup.POST("/:id/deliver", h.MarkDelivered)
    orderGroup.POST("/:id/prepare-confirm", h.PrepareConfirm)
    orderGroup.POST("/:id/submit-confirm", h.SubmitConfirm)
    orderGroup.POST("/:id/request-refund", h.RequestRefund)
    orderGroup.POST("/:id/cancel", h.CancelOrder)
}

// Admin order routes
adminOrderGroup := api.Group("/admin/orders")
adminOrderGroup.Use(adminAuth)
{
    adminOrderGroup.GET("", h.GetAllOrders)
    adminOrderGroup.POST("/:id/resolve", h.AdminResolveDispute)
}
```

## Tests

Write Go tests for:
1. Order creation from cart with correct fee/shipping calculation
2. Shipping zone detection (all 4 zones + fallback)
3. MCGP 0% fee path
4. Order status transitions (valid + reject invalid)
5. Prepare escrow tx returns valid unsigned tx JSON
6. Multi-seller cart creates separate orders
7. Only buyer can confirm, only seller can deliver
8. Admin resolve requires admin role
