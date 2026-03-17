# Agent Prompt: Build Marketplace Checkout Backend (Sub-project 5)

## Context

You are building the backend checkout flow for TSA Connect, a marketplace platform. The Go/Gin API backend is at `tsa-api-go/`. Smart contracts (ProductEscrow.sol, ServiceContact.sol) are deployed on Sonic testnet. Your job is to build the API layer that connects the marketplace cart to on-chain escrow.

## Read These Files First (in order)

1. `tsa-api-go/CLAUDE.md` — project architecture, commands, patterns
2. `docs/superpowers/specs/2026-03-16-checkout-backend-design.md` — the full spec for this task
3. `docs/superpowers/specs/2026-03-12-wallet-contracts-checkout-design.md` (lines 210-332) — original requirements
4. `smart-contracts/contracts/ProductEscrow.sol` — the escrow contract you're integrating with
5. `tsa-api-go/internal/models/order.go` — existing Order model (needs extending)
6. `tsa-api-go/internal/models/product.go` — existing Product model (needs shipping fields)
7. `tsa-api-go/internal/models/user.go` — User model (has City, State, Country for shipping zone)
8. `tsa-api-go/internal/handlers/wallet_handler.go` — existing pattern for prepare-tx / submit-tx
9. `tsa-api-go/internal/blockchain/token.go` — UnsignedTx struct, PrepareERC20Approve, PrepareERC20Transfer
10. `tsa-api-go/internal/services/blockchain_service.go` — BlockchainService with multi-chain client registry
11. `tsa-api-go/internal/config/config.go` — Config struct and env loading
12. `tsa-api-go/internal/routes/routes.go` — current route registration
13. `tsa-api-go/internal/handlers/cart_handler.go` — existing cart/checkout logic (ConvertToOrder)
14. `tsa-api-go/internal/handlers/order_handler.go` — existing order handlers (admin only)

## Deployed Contracts (Sonic Testnet)

```
MockUSDC:       0x9f8AfF2706F52Ddb02921E245ec95Ade96767379  (6 decimals)
MockMCGP:       0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7  (18 decimals)
ProductEscrow:  0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d
ServiceContact: 0x3d761F72f4369e072767E830eE8Ce4c3A2144e6f
```

## What To Build

### 1. Config updates (`internal/config/config.go`)

Add env vars:
- `PRODUCT_ESCROW_ADDRESS` (default: `0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d`)
- `SERVICE_CONTACT_ADDRESS` (default: `0x3d761F72f4369e072767E830eE8Ce4c3A2144e6f`)

Update `.env` with:
```
PRODUCT_ESCROW_ADDRESS=0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d
SERVICE_CONTACT_ADDRESS=0x3d761F72f4369e072767E830eE8Ce4c3A2144e6f
USDC_TOKEN_ADDRESS=0x9f8AfF2706F52Ddb02921E245ec95Ade96767379
MCGP_TOKEN_ADDRESS=0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7
```

### 2. Model changes

**Extend Order model** (`internal/models/order.go`):
- Replace the existing Order struct with the escrow-aware version from the spec
- All amounts as `string` (wei values) — NOT float64
- New statuses: `pending_payment`, `escrowed`, `delivered`, `completed`, `refund_requested`, `refunded`, `cancelled`
- New fields: `ProductID`, `Quantity`, `Token`, `ProductAmount`, `ShippingAmount`, `PlatformFee`, `TotalAmount`, `ShippingZone`, `ContractOrderID`, `EscrowTxHash`, `ApproveTxHash`, `ReleaseTxHash`, `BuyerUpline`, `DeliveryProofURL`, `BuyerConfirmedAt`, `SellerDeliveredAt`, `EscrowExpiresAt`
- Update `ValidNextStatuses()` for new status flow

**Extend Product model** (`internal/models/product.go`):
- Add shipping rate fields: `ShippingSameCity` (default 0), `ShippingSameState`, `ShippingSameCountry`, `ShippingInternational` (all float64)

### 3. Escrow service (`internal/services/escrow_service.go`)

New file. Uses go-ethereum ABI encoding to prepare unsigned transactions for ProductEscrow contract calls.

**Compile the ProductEscrow ABI**: Read the Solidity contract, extract the function signatures, and create a Go ABI string constant (same pattern as `erc20ABI` in `blockchain/token.go`). Include these functions:
- `createOrder(bytes32,address,address,uint256,uint256,address)`
- `markDelivered(bytes32)`
- `confirmReceipt(bytes32)`
- `requestRefund(bytes32)`
- `cancelOrder(bytes32)`
- `adminResolve(bytes32,bool)`

And these events:
- `OrderCreated(bytes32,address,address,address,uint256,uint256,uint256)`

**EscrowService struct**: Holds `*blockchain.EVMClient`, escrow contract address, config.

**Methods**:
- `PrepareCreateOrder(orderId [32]byte, buyer, seller, token string, productAmount, shippingAmount *big.Int, upline string) ([]byte, error)` — encodes `createOrder()` call data, builds UnsignedTx
- `PrepareConfirmReceipt(orderId [32]byte, buyer string) ([]byte, error)`
- `PrepareRequestRefund(orderId [32]byte, buyer string) ([]byte, error)`
- `PrepareCancelOrder(orderId [32]byte, seller string) ([]byte, error)`
- `PrepareAdminResolve(orderId [32]byte, refundBuyer bool, admin string) ([]byte, error)`
- `VerifyEscrowCreated(txHash string) (contractOrderId [32]byte, err error)` — fetches receipt, parses OrderCreated event log
- `GenerateOrderID(dbOrderID uuid.UUID) [32]byte` — deterministic bytes32 from UUID (e.g., keccak256 of UUID bytes)

### 4. Checkout handler (`internal/handlers/checkout_handler.go`)

New file. Follows the same patterns as `wallet_handler.go` (request structs, validation, response format).

**Endpoints**:

`POST /api/orders` — Create orders from cart
- Input: `{token: "USDC", buyerCity, buyerState, buyerCountry}`
- Groups cart items by seller (use `GetCartSummary` pattern)
- For each seller group: detect shipping zone (buyer location vs seller User.City/State/Country), look up shipping rate from Product, calculate platform fee (10%, 0% for MCGP), compute total
- Creates Order records in DB with `pending_payment` status
- Returns array of order summaries with amounts

`POST /api/orders/:id/prepare-escrow`
- Auth: must be the buyer
- Returns: `{approveTx: UnsignedTx, createOrderTx: UnsignedTx}`
- The approve tx: `ERC20.approve(escrowAddress, totalAmount)`
- The createOrder tx: `ProductEscrow.createOrder(orderId, seller, token, productAmount, shippingAmount, upline)`

`POST /api/orders/:id/submit-escrow`
- Input: `{approveTxHash, escrowTxHash}`
- Verifies both tx receipts on-chain (status = success)
- Parses OrderCreated event from escrow tx receipt
- Updates order: status → `escrowed`, stores tx hashes, sets `escrowExpiresAt = now + 30 days`

`POST /api/orders/:id/deliver`
- Auth: must be the seller
- Input: `{deliveryProofUrl}` (Cloudinary URL from mobile upload)
- Updates: status → `delivered`, sets `sellerDeliveredAt`

`POST /api/orders/:id/prepare-confirm`
- Auth: must be the buyer
- Returns: `{confirmTx: UnsignedTx}` for `ProductEscrow.confirmReceipt(orderId)`

`POST /api/orders/:id/submit-confirm`
- Input: `{txHash}`
- Verifies tx on-chain
- Updates: status → `completed`, sets `buyerConfirmedAt`

`POST /api/orders/:id/request-refund`
- Auth: must be the buyer
- If status is `escrowed` (before delivery): prepare unsigned `requestRefund` tx, return it
- If status is `delivered`: update to `refund_requested` for admin review

`POST /api/orders/:id/cancel`
- Auth: must be the seller
- Only if status is `escrowed` (before delivery)
- Prepare unsigned `cancelOrder` tx, return it

`GET /api/orders`
- Auth: any authenticated user
- Returns orders where user is buyer OR seller
- Query params: `?status=escrowed&role=buyer` (filter by status and role)
- Paginated (page, limit)

`GET /api/orders/:id`
- Auth: buyer, seller, or admin
- Returns full order detail

`GET /api/orders/shipping-estimate`
- Query params: `?productId=X&buyerCity=Y&buyerState=Z&buyerCountry=W`
- Returns: `{zone, shippingCost, currency}`

`POST /api/admin/orders/:id/resolve`
- Auth: admin only
- Input: `{refundBuyer: bool}`
- Prepares unsigned `adminResolve` tx, returns it
- After signed tx submitted: updates order status

### 5. Route registration (`internal/routes/routes.go`)

Replace the existing admin-only order routes with the new structure:

```go
// Order/checkout routes (authenticated)
orderGroup := api.Group("/orders")
orderGroup.Use(auth)
{
    orderGroup.POST("", h.CreateOrderFromCart)
    orderGroup.GET("", h.GetUserOrders)
    orderGroup.GET("/shipping-estimate", h.GetShippingEstimate)
    orderGroup.GET("/:id", h.GetOrderDetail)
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

**IMPORTANT**: Register `/shipping-estimate` BEFORE `/:id` to avoid Gin treating "shipping-estimate" as an ID.

### 6. Handler struct updates

The checkout handler needs access to `BlockchainService` and `EscrowService`. Follow the `WalletHandler` pattern — either extend it or create a `CheckoutHandler` struct:

```go
type CheckoutHandler struct {
    DB                *gorm.DB
    Config            *config.Config
    BlockchainService *services.BlockchainService
    EscrowService     *services.EscrowService
}
```

Wire it up in `cmd/server/main.go` where the blockchain service is initialized.

### 7. Tests

Write tests in `internal/handlers/checkout_handler_test.go`:

1. **Shipping zone detection**: Test all 4 zones + missing location fallback
2. **Fee calculation**: 10% for USDC, 0% for MCGP, correct wei math
3. **Order creation**: Cart with 2 items from different sellers → 2 orders, correct amounts
4. **Status transitions**: Valid transitions succeed, invalid ones return 400
5. **Auth checks**: Only buyer can confirm, only seller can deliver, only admin can resolve
6. **Prepare escrow**: Returns valid UnsignedTx JSON with correct ABI-encoded data
7. **Multi-seller cart**: Items grouped correctly, each order has its own escrow

Use SQLite in-memory DB for tests (same pattern as `wallet_handler_test.go`).

## Patterns To Follow

- **Response format**: Always use `utils.SuccessResponse(c, statusCode, message, data)` and `utils.ErrorResponse(c, statusCode, message, errors)`
- **Auth**: Use `getUserFromContext(c)` to get the authenticated user
- **Validation**: Validate all inputs, return 400 with descriptive errors
- **BigInt math**: Use `math/big` for all token amount calculations — never float64
- **Tx preparation**: Return `UnsignedTx` JSON (same struct from `blockchain/token.go`)
- **Tx verification**: Use `BlockchainService.GetTransactionReceipt()` then check `receipt.Status == types.ReceiptStatusSuccessful`

## What NOT To Do

- Do NOT build mobile screens (that's a separate task)
- Do NOT deploy contracts (already deployed)
- Do NOT modify the smart contracts
- Do NOT add event listeners or webhooks (future task)
- Do NOT use float64 for token amounts — use string/big.Int
- Do NOT create a new go module — this is inside the existing `tsa-api-go` module

## Verification

After building, run:
```bash
cd tsa-api-go
go build ./...          # Must compile
go test ./... -v        # All tests must pass
```

Then test manually with curl:
1. Create a test user, get JWT
2. Add products to cart
3. POST /api/orders to create order
4. POST /api/orders/:id/prepare-escrow to get unsigned txs
5. Verify the unsigned tx data is valid ABI-encoded calls
