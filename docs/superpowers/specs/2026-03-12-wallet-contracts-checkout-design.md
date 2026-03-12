# TSA Platform — Wallet, Smart Contracts & Marketplace Checkout Design

**Date:** 2026-03-12
**Status:** Approved
**Scope:** Sub-projects 3 (Wallet), 4 (Smart Contracts), 5 (Marketplace + Checkout)
**Build Order:** Wallet → Smart Contracts → Marketplace Checkout

## Network & Contract References

- **Sonic Testnet RPC:** https://rpc.testnet.soniclabs.com (Chain ID: 14601)
- **Sonic Mainnet RPC:** https://rpc.soniclabs.com (Chain ID: 146)
- **MCGP Token:** `0x517600323e5E2938207fA2e2e915B9D80e5B2b21`
- **Swap Contract:** `0xfEF5d571D47d21A9cd9c6aF423116557Bbb5BF8b`
- **System/Treasury Wallet:** `0xaF326D5D242C9A55590540f14658adDDd3586A8d`
- **USDT on Sonic:** TBD — look up or deploy test token on Blaze testnet
- **USDC on Sonic:** TBD — look up or deploy test token on Blaze testnet
- **Development target:** Testnet first, mainnet via config switch

---

## Sub-project 3: Wallet System

### Architecture

```
Mobile (tsa-app)                    API (tsa-api-go)                Sonic Testnet
┌─────────────────────┐      ┌──────────────────────┐      ┌─────────────────┐
│ Key Generation      │      │ Store public address  │      │ MCGP Token      │
│ (ethers.js)         │─────→│ in User model         │      │ USDT / USDC     │
│                     │      │                       │      │                 │
│ Encrypted Storage   │      │ Balance endpoint      │─────→│ RPC: balanceOf  │
│ (react-native-      │      │ (proxy to Sonic RPC)  │      │                 │
│  keychain + bio)    │      │                       │      │                 │
│                     │      │ Tx history endpoint   │      │                 │
│ Seed Phrase Backup  │      │ (mirror on-chain txs) │      │                 │
│ (generate, confirm) │      │                       │      │                 │
│                     │      │                       │      │                 │
│ Sign Transactions   │      │ Prepare unsigned txs  │      │                 │
│ (local key)         │─────→│ (for escrow, sends)   │─────→│ Submit signed tx│
└─────────────────────┘      └──────────────────────┘      └─────────────────┘
```

### Mobile-Side Components

1. **Key Generation** — `ethers.js` generates HD wallet (BIP-39 mnemonic → private key + address) at signup completion
2. **Secure Storage** — Private key encrypted via `react-native-keychain` with biometric access (FaceID/fingerprint). Key never leaves device.
3. **Seed Phrase Flow** — After generation: display 12-word mnemonic → user confirms by selecting words in order → flag `seedPhraseBackedUp` on server
4. **Wallet Import** — "Restore wallet" option on login using seed phrase (for device migration)
5. **Balance Display** — Shows MCGP, USDT, USDC balances (API fetches from Sonic RPC)
6. **Send Flow** — User enters recipient address + amount → API prepares unsigned tx → app signs with local key → submits to chain
7. **Receive Flow** — Display wallet address as QR code + copy button

### API-Side (Go)

1. **User model** — Add `wallet_address` field (public address only, set once at wallet creation). The existing `Wallet` GORM model is for portfolio/asset tracking — keep it separate. `wallet_address` on User is the on-chain EOA address.
2. **`GET /api/wallet/balances`** — Queries Sonic RPC for MCGP/USDT/USDC balances using token contract `balanceOf(address)`
3. **`POST /api/wallet/prepare-tx`** — Builds unsigned ERC-20 transfer transaction (nonce, gas, data), returns to mobile for signing
4. **`POST /api/wallet/submit-tx`** — Receives signed transaction, submits to Sonic RPC, stores tx record in DB
5. **`GET /api/wallet/transactions`** — Returns user's transaction history from DB
6. **Blockchain service** — Go package using `go-ethereum` (`ethclient`) to interact with Sonic RPC

### Security Rules

- Private key **never** sent to server — all signing happens on device
- API only stores public wallet address
- Biometric required to unlock key for signing
- Seed phrase can be re-displayed from Settings with biometric authentication (not truly one-time — prevents accidental loss if app crashes during first display)
- No plaintext key storage anywhere

### Tests

**API tests (Go):**
- Wallet address registration (valid Ethereum address, cannot change once set)
- Reject invalid wallet address format (non-hex, wrong length, missing 0x prefix)
- Reject duplicate wallet registration (address already set returns 409)
- Balance endpoint returns correct token balances (mock RPC responses for MCGP=1000, USDT=50, USDC=25)
- Balance endpoint returns error when RPC is unreachable (not silent zero)
- Prepare-tx builds valid unsigned ERC-20 transfer with correct nonce, gas, and data fields
- Submit-tx validates signed transaction and stores record
- Submit-tx rejects invalid signature (malformed hex)
- Submit-tx rejects replayed nonce (tx already submitted)
- Transaction history returns correct user transactions with pagination (page=1, limit=20)

**Mobile tests:**
- Key generation produces valid 12-word BIP-39 mnemonic and 0x-prefixed address
- Seed phrase confirmation rejects wrong word order, accepts correct order
- Wallet import from seed phrase derives identical address as original generation
- Biometric prompt triggers before any signing operation
- Balance display: given mock API response {MCGP: 1000, USDT: 50.25, USDC: 0}, renders "1,000 MCGP", "$50.25 USDT", "$0.00 USDC"
- Send flow rejects invalid address, rejects amount > balance, shows confirmation for valid input
- Seed phrase re-display requires biometric auth before showing words

---

## Sub-project 4: Smart Contracts

### Contract Structure

```
contracts/
├── ProductEscrow.sol      — holds funds during product orders
├── ServiceContact.sol     — instant $0.10 fee split for service contacts
└── interfaces/
    └── IERC20.sol         — standard ERC-20 interface
```

The existing **Swap contract** and **MCGP token** are already deployed — we integrate, not rebuild.

### ProductEscrow.sol

**State per order:**
```solidity
struct Order {
    address buyer;
    address seller;
    address token;           // USDT, USDC, or MCGP
    uint256 productAmount;   // product price
    uint256 shippingAmount;  // shipping fee
    uint256 platformFee;     // total platform fee: 10% of productAmount (0% if MCGP), split 5% system / 2.5% buyer / 2.5% upline on release
    address buyerUpline;     // direct referrer for cashback split
    uint256 createdAt;
    bool sellerDelivered;    // seller submitted delivery proof
    bool buyerConfirmed;     // buyer confirmed receipt
    bool refundRequested;    // buyer requested refund
    bool resolved;           // funds released or refunded
}
```

**Functions:**

- `createOrder(orderId, seller, token, productAmount, shippingAmount, buyerUpline)` — buyer calls. Requires prior ERC-20 `approve()` for the total amount. Contract calls `transferFrom` to pull `productAmount + shippingAmount + platformFee` into escrow.
- `markDelivered(orderId)` — seller marks as delivered (off-chain proof stored in API)
- `confirmReceipt(orderId)` — buyer confirms, triggers release:
  - Seller gets `productAmount + shippingAmount`
  - If not MCGP: 5% → system wallet, 2.5% → buyer cashback, 2.5% → buyer's upline
- `requestRefund(orderId)` — buyer requests refund:
  - If seller has **not** marked delivered: instant 100% refund to buyer, order resolved
  - If seller **has** marked delivered: creates dispute, admin must resolve
- `cancelOrder(orderId)` — seller cancels before marking delivered, 100% refund to buyer
- `autoRefund(orderId)` — callable by anyone after 30 days. In practice, the Go API runs a scheduled job (cron) every hour to call this for expired orders, paying gas from the system wallet:
  - If seller never delivered: 100% → buyer
  - If seller delivered but buyer didn't confirm: 90% → buyer, 7% → system, 3% → seller
- `adminResolve(orderId, refundBuyer)` — owner (system wallet) resolves disputes manually

**Upline resolution:** The User model already has a `referred_by` field (set at signup via referral code). The API resolves the upline's wallet address by looking up the referrer's `wallet_address`. If the buyer has no upline (no referral code), `buyerUpline` is set to `address(0)` — the contract sends that 2.5% share to the system wallet instead.

**Access control:** Owner is system/treasury wallet. Only owner can call `adminResolve`. Ownership is transferable.

**Order lifecycle:**
```
Buyer creates order
  ├── Seller cancels          → 100% refund to buyer
  ├── Buyer requests refund
  │     ├── Seller not delivered → 100% instant refund
  │     └── Seller delivered     → dispute (admin resolves)
  ├── Buyer confirms receipt  → release with fee splits
  └── 30 days pass (autoRefund)
        ├── Seller never delivered → 100% refund
        └── Seller delivered       → 90/7/3 split
```

### ServiceContact.sol

Instant split, no escrow hold.

**Functions:**

- `payContactFee(serviceProvider, requesterUpline)` — caller pays $0.10 equivalent in USDT/USDC, instant 4-way split:
  - $0.05 → system wallet
  - $0.025 → service provider
  - $0.0125 → caller (cashback)
  - $0.0125 → caller's upline
- Returns success → API reveals contact details

The $0.10 is denominated in USD but paid in stablecoin (USDT/USDC). Since USDT and USDC are pegged 1:1 to USD, no price oracle is needed — $0.10 = 0.1 USDT or 0.1 USDC (6 decimal token = 100000 units). The contract stores the fee amount in token units. If we later support non-stable tokens for contact fees, a Chainlink price feed would be added.

### Go Bindings & Integration

1. **Compile contracts** with Hardhat (solc) — produces ABI + bytecode
2. **Generate Go bindings** via `abigen` (from `go-ethereum`) — typed Go structs for every contract function
3. **Blockchain service package** (`tsa-api-go/internal/blockchain/`):
   - `escrow.go` — prepare unsigned escrow txs (createOrder, confirmReceipt, requestRefund, etc.)
   - `service_contact.go` — prepare unsigned contact fee tx
   - `token.go` — ERC-20 balanceOf, approve, allowance queries
   - `client.go` — Sonic RPC connection, gas estimation, nonce management
4. **Deployment script** — Hardhat deploy to testnet, then transfer ownership to system wallet

### Contract Tests

**Solidity tests (Hardhat + ethers.js):**
- Happy path: create order → confirm → verify splits are correct
- MCGP 0% fee path
- 30-day auto-refund — seller never delivered (100% buyer)
- 30-day auto-refund — seller delivered, buyer didn't confirm (90/7/3)
- Buyer refund before seller delivers → full refund
- Buyer refund after seller delivers → dispute state, only admin can resolve
- Seller cancels order → full refund
- Admin dispute resolution (refund and release paths)
- Cannot refund/cancel already-resolved order
- Cannot confirm already-resolved order
- Service contact fee split accuracy (4-way split)
- Access control — non-owner cannot call adminResolve
- Edge cases: zero upline address, duplicate orderId

**Go integration tests:**
- Verify Go bindings work against a local Hardhat node
- Prepare unsigned tx → simulate signing → submit → verify state change

---

## Sub-project 5: Marketplace + Smart Contract Checkout

### New Models (Go/PostgreSQL)

**Order model:**
```go
type Order struct {
    ID                uint            `gorm:"primaryKey"`
    BuyerID           uint            `gorm:"not null;index"`
    SellerID          uint            `gorm:"not null;index"`
    ProductID         uint            `gorm:"not null"`
    Quantity          int             `gorm:"not null;default:1"`
    Token             string          `gorm:"not null"`           // "USDT", "USDC", "MCGP"
    ProductAmount     decimal.Decimal `gorm:"type:decimal(20,8)"` // product price × quantity
    ShippingAmount    decimal.Decimal `gorm:"type:decimal(20,8)"` // based on zone
    PlatformFee       decimal.Decimal `gorm:"type:decimal(20,8)"` // 10% (0% if MCGP)
    TotalAmount       decimal.Decimal `gorm:"type:decimal(20,8)"` // product + shipping + fee
    ShippingZone      string                                       // same_city, same_state, same_country, international
    EscrowTxHash      string
    Status            string          `gorm:"not null;default:'pending_payment';index"`
    DeliveryProofURL  string
    BuyerConfirmedAt  *time.Time
    SellerDeliveredAt *time.Time
    EscrowExpiresAt   time.Time
    CreatedAt         time.Time
    UpdatedAt         time.Time
}
```

**Order statuses:** `pending_payment` → `escrowed` → `delivered` → `completed` | `refund_requested` → `disputed` | `refunded` | `cancelled`

**Shipping zone detection logic:** Compare buyer location (city/state/country) to seller location on product:
- Same city + same state + same country → `same_city` (free by default)
- Different city, same state → `same_state`
- Different state, same country → `same_country`
- Different country → `international`

**Product model additions:**
```go
// Add to existing Product model
ShippingSameCity      decimal.Decimal `gorm:"type:decimal(20,8);default:0"` // same city = free by default
ShippingSameState     decimal.Decimal `gorm:"type:decimal(20,8)"`
ShippingSameCountry   decimal.Decimal `gorm:"type:decimal(20,8)"`
ShippingInternational decimal.Decimal `gorm:"type:decimal(20,8)"`
SellerCity            string          // seller's city for zone calculation
SellerState           string
SellerCountry         string
```

### API Endpoints

**Checkout flow:**
- `POST /api/orders` — create order from cart, calculate shipping + fees, return unsigned escrow tx
- `POST /api/orders/:id/submit-escrow` — receive signed tx hash, update order to `escrowed`
- `POST /api/orders/:id/deliver` — seller marks delivered, uploads proof image
- `POST /api/orders/:id/confirm` — buyer confirms receipt, returns unsigned release tx
- `POST /api/orders/:id/submit-release` — receive signed release tx, update to `completed`
- `POST /api/orders/:id/refund` — buyer requests refund, returns unsigned refund tx if eligible
- `POST /api/orders/:id/cancel` — seller cancels, returns unsigned cancel tx
- `GET /api/orders` — list user's orders (as buyer or seller), filtered by status
- `GET /api/orders/:id` — order detail with timeline
- `GET /api/orders/shipping-estimate?product_id=X&buyer_city=Y&buyer_state=Z&buyer_country=W` — shipping cost by zone
- `POST /api/admin/orders/:id/resolve` — admin resolves dispute (calls contract's `adminResolve`, requires admin JWT)

### Checkout Flow (end-to-end)

```
1. Buyer taps "Checkout" in cart
2. App calls POST /api/orders (cart items, payment token, buyer location)
3. API:
   - Creates Order records (one per seller in cart)
   - Calculates shipping per zone
   - Calculates platform fee (10%, or 0% for MCGP)
   - For each order: prepares unsigned ERC-20 approve() tx + unsigned createOrder() tx
   - Returns: order summaries + unsigned tx pairs
4. App shows order summary (price + shipping + fee = total per seller, grand total)
5. Buyer confirms → app signs approve tx first (biometric unlock), submits, waits for confirmation
6. App signs createOrder tx, submits → POST /api/orders/:id/submit-escrow
7. API verifies tx on-chain, updates order to "escrowed"
8. Repeat steps 5-7 for each seller's order sequentially
   - If any order fails mid-sequence: already-escrowed orders remain valid,
     failed order stays "pending_payment" — buyer can retry or cancel
9. Seller notified → ships product → uploads proof → POST /api/orders/:id/deliver
10. Buyer receives product → POST /api/orders/:id/confirm
11. API prepares unsigned release tx → app signs → submits
12. Contract splits funds per fee model
13. Order marked "completed"
```

### Mobile Screens

- **Checkout Summary** — replaces hardcoded screen: items, shipping, fees, total, token selector, confirm button
- **Order History** — list of orders with status badges
- **Order Detail** — timeline view (escrowed → delivered → completed), action buttons per status
- **Delivery Proof** — seller uploads photo via Cloudinary
- **Refund Request** — buyer can request from order detail

### Tests

**API tests (Go):**
- Order creation from cart with correct fee/shipping calculation
- Shipping zone detection: same city (Lagos/Lagos → $0), same state (Ikeja/Lagos → seller rate), same country (Lagos/Abuja → seller rate), international
- MCGP 0% fee: order with MCGP token has platformFee=0, totalAmount = productAmount + shipping only
- Order status transitions: only valid transitions allowed (e.g., cannot go from `pending_payment` to `completed`)
- Reject invalid status transitions (return 400)
- Escrow tx submission and on-chain verification
- Refund eligibility: allowed before delivery, creates dispute after delivery
- Seller cancel: only before marking delivered
- Order listing with filters (status, role=buyer/seller) and pagination
- Multi-seller cart creates separate orders per seller with independent statuses
- Admin dispute resolution endpoint (requires admin role)
- ERC-20 approve + createOrder tx pair prepared correctly

**Mobile tests:**
- Checkout summary: given cart with 2 items ($10 + $5), USDT, same-city shipping → renders "$15.00 + $0.00 shipping + $1.50 fee = $16.50"
- Multi-seller checkout: shows per-seller subtotals and grand total
- Approve tx signed before createOrder tx (correct sequence)
- Partial failure: first seller escrowed, second fails → first order shows "escrowed", second shows "pending_payment" with retry button
- Order history displays correct status badges (color-coded by status)
- Refund button visible only when status is `escrowed` or `delivered`
- Cancel button visible only for seller when status is `escrowed`

---

## Dependencies Between Sub-projects

```
Wallet (3) ← independent, build first
    ↓
Smart Contracts (4) ← uses wallet for signing, needs wallet address
    ↓
Marketplace Checkout (5) ← orchestrates wallet + contracts + existing cart/product system
```

## File Structure (new additions)

```
tsa-api-go/
├── internal/
│   └── blockchain/
│       ├── client.go          — Sonic RPC connection
│       ├── token.go           — ERC-20 queries
│       ├── escrow.go          — ProductEscrow bindings
│       ├── service_contact.go — ServiceContact bindings
│       └── contracts/         — generated Go bindings (abigen output)
├── handlers/
│   ├── wallet.go              — wallet endpoints
│   └── order.go               — order/checkout endpoints
├── models/
│   └── order.go               — Order model
└── tests/
    ├── wallet_test.go
    ├── order_test.go
    └── blockchain_test.go

contracts/                      — Solidity (at repo root)
├── hardhat.config.js
├── contracts/
│   ├── ProductEscrow.sol
│   ├── ServiceContact.sol
│   └── interfaces/IERC20.sol
├── test/
│   ├── ProductEscrow.test.js
│   └── ServiceContact.test.js
└── scripts/
    └── deploy.js

tsa-app/
├── services/
│   ├── wallet.ts              — key gen, secure storage, signing
│   └── blockchain.ts          — tx preparation helpers
├── screens/
│   ├── wallet/
│   │   ├── WalletHome.tsx     — balances, send/receive
│   │   ├── SeedPhrase.tsx     — backup flow
│   │   └── SendToken.tsx      — send flow
│   └── orders/
│       ├── Checkout.tsx       — replaces hardcoded checkout
│       ├── OrderHistory.tsx
│       └── OrderDetail.tsx
└── __tests__/
    ├── wallet.test.ts
    └── checkout.test.ts
```
