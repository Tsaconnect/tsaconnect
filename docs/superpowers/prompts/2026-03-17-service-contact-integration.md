# Agent Prompt: ServiceContact Integration (Backend + Mobile)

## Context

You are building the ServiceContact fee flow for TSA Connect. When a buyer wants to reveal a service provider's contact details, they pay a $0.10 fee that is instantly split 4 ways via the ServiceContact smart contract. This is a full-stack task: Go backend API + Expo React Native mobile screens.

The ServiceContact.sol contract is already deployed on Sonic testnet. You need to:
1. Build backend endpoints that prepare/verify the on-chain fee payment
2. Update the mobile service detail screen to handle the pay → sign → reveal flow

## Read These Files First

### Backend
1. `tsa-api-go/CLAUDE.md` — project architecture, commands, patterns
2. `tsa-api-go/internal/config/config.go` — Config struct (already has `ServiceContactAddress`)
3. `tsa-api-go/internal/services/escrow_service.go` — existing ABI encoding pattern (follow this)
4. `tsa-api-go/internal/blockchain/token.go` — UnsignedTx struct, PrepareERC20Approve
5. `tsa-api-go/internal/services/blockchain_service.go` — BlockchainService, ClientForChain
6. `tsa-api-go/internal/handlers/checkout_handler.go` — prepare-tx / submit-tx pattern to follow
7. `tsa-api-go/internal/handlers/wallet_handler.go` — handler struct pattern
8. `tsa-api-go/internal/routes/routes.go` — route registration
9. `tsa-api-go/internal/models/user.go` — User model (has WalletAddress, ReferredBy)

### Smart Contract
10. `smart-contracts/contracts/ServiceContact.sol` — the contract you're integrating with

### Mobile
11. `tsa-app/CLAUDE.md` — mobile project architecture
12. `tsa-app/app/(servicegroup)/servicedetail.tsx` — existing service detail screen (needs update)
13. `tsa-app/components/services/ServiceDetail.tsx` — service detail card component
14. `tsa-app/services/walletApi.ts` — wallet API pattern (fetch-based with auth headers)
15. `tsa-app/services/wallet.ts` — signTransaction(), getProvider()
16. `tsa-app/constants/constantValues.js` — CONTACT_FEE constant (needs update)
17. `tsa-app/constants/chains.ts` — Sonic testnet config
18. `tsa-app/constants/theme.js` — design tokens

## Deployed Contracts (Sonic Testnet)

```
ServiceContact: 0x3d761F72f4369e072767E830eE8Ce4c3A2144e6f
MockUSDC:       0x9f8AfF2706F52Ddb02921E245ec95Ade96767379  (6 decimals)
MockMCGP:       0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7  (18 decimals)
```

## ServiceContact.sol Summary

- `payContactFee(serviceProvider, requesterUpline, token)` — caller pays `feeAmount` (default 100000 = $0.10 in 6-decimal USDC/USDT)
- Instant 4-way split: 50% system, 25% provider, 12.5% caller cashback, 12.5% upline
- Requires prior ERC-20 `approve(serviceContactAddress, feeAmount)`
- If upline is `address(0)`, upline share goes to system wallet

## What To Build

### Backend

#### 1. Service Contact Service (`internal/services/service_contact_service.go`)

New file. Same pattern as `escrow_service.go`.

**ABI** — extract from ServiceContact.sol:
```json
[
  {"inputs":[{"name":"serviceProvider","type":"address"},{"name":"requesterUpline","type":"address"},{"name":"token","type":"address"}],"name":"payContactFee","outputs":[],"stateMutability":"nonpayable","type":"function"},
  {"anonymous":false,"inputs":[{"indexed":true,"name":"caller","type":"address"},{"indexed":true,"name":"serviceProvider","type":"address"},{"indexed":true,"name":"token","type":"address"},{"indexed":false,"name":"amount","type":"uint256"},{"indexed":false,"name":"upline","type":"address"}],"name":"ContactFeePaid","type":"event"}
]
```

**ServiceContactService struct**: Holds `*blockchain.EVMClient`, contract address, config.

**Methods**:
- `PreparePayContactFee(caller, serviceProvider, upline, token string) ([]byte, error)` — encodes `payContactFee()` call, builds UnsignedTx
- `VerifyContactFeePaid(txHash string) (bool, error)` — fetches receipt, confirms ContactFeePaid event exists
- `GetFeeAmount() *big.Int` — returns 100000 (the $0.10 fee in 6-decimal token units)

Use `buildUnsignedTx` pattern from `escrow_service.go` — return error in production if client is nil, return minimal tx in non-production for tests.

#### 2. Service Contact Handler (`internal/handlers/service_contact_handler.go`)

New file.

**Handler struct**:
```go
type ServiceContactHandler struct {
    Config                *config.Config
    BlockchainService     *services.BlockchainService
    ServiceContactService *services.ServiceContactService
}
```

**Endpoints**:

`POST /api/services/:id/prepare-contact-fee`
- Auth: authenticated user (the buyer/caller)
- Looks up the service advert by ID to get the provider's user ID → wallet address
- Looks up the caller's upline (user.ReferredBy → referrer.WalletAddress, or `address(0)`)
- Determines token address (default USDC, accept `token` query param for USDT)
- Returns:
  ```json
  {
    "approveTx": { UnsignedTx for ERC20.approve(serviceContactAddress, feeAmount) },
    "payFeeTx": { UnsignedTx for ServiceContact.payContactFee(...) },
    "feeAmount": "100000",
    "token": "USDC"
  }
  ```
- Validates: caller has a wallet, provider has a wallet, caller != provider

`POST /api/services/:id/submit-contact-fee`
- Auth: authenticated user (the buyer/caller)
- Body: `{approveTxHash: string, payFeeTxHash: string}`
- Verifies both tx receipts on-chain (status = success)
- Parses ContactFeePaid event from pay fee receipt
- Records the contact fee payment in DB (create a `ServiceContactPayment` model)
- Returns the provider's contact details (phone, email, address — whatever is on their profile)

`GET /api/services/:id/contact`
- Auth: authenticated user
- Checks if the user has already paid the contact fee for this service
- If paid: returns provider contact details
- If not paid: returns 402 Payment Required with fee info

#### 3. ServiceContactPayment Model (`internal/models/service_contact_payment.go`)

```go
type ServiceContactPayment struct {
    ID                uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
    CallerID          uuid.UUID `gorm:"type:uuid;not null" json:"callerId"`
    ServiceProviderID uuid.UUID `gorm:"type:uuid;not null" json:"serviceProviderId"`
    ServiceID         uuid.UUID `gorm:"type:uuid;not null" json:"serviceId"`
    Token             string    `json:"token"`
    FeeAmount         string    `json:"feeAmount"`
    ApproveTxHash     string    `json:"approveTxHash"`
    PayFeeTxHash      string    `json:"payFeeTxHash"`
    CreatedAt         time.Time `json:"createdAt"`
}
```

Auto-migrate this in `cmd/server/main.go`.

#### 4. Route Registration (`internal/routes/routes.go`)

```go
// Service contact fee routes (authenticated)
serviceGroup := api.Group("/services")
serviceGroup.Use(auth)
{
    serviceGroup.POST("/:id/prepare-contact-fee", sch.PrepareContactFee)
    serviceGroup.POST("/:id/submit-contact-fee", sch.SubmitContactFee)
    serviceGroup.GET("/:id/contact", sch.GetServiceContact)
}
```

#### 5. Wire Up in `cmd/server/main.go`

```go
serviceContactService := services.NewServiceContactService(sonicClient, cfg)
sch := handlers.NewServiceContactHandler(cfg, blockchainService, serviceContactService)
```

### Mobile

#### 6. Service Contact API (`services/serviceContactApi.ts`)

New file. Follow `walletApi.ts` pattern.

```typescript
export async function prepareContactFee(serviceId: string, token?: string): Promise<ApiResponse<{
  approveTx: UnsignedTx;
  payFeeTx: UnsignedTx;
  feeAmount: string;
  token: string;
}>>

export async function submitContactFee(serviceId: string, approveTxHash: string, payFeeTxHash: string): Promise<ApiResponse<{
  contact: { phone?: string; email?: string; address?: string; name?: string };
}>>

export async function getServiceContact(serviceId: string): Promise<ApiResponse<{
  paid: boolean;
  contact?: { phone?: string; email?: string; address?: string; name?: string };
}>>
```

#### 7. Update Service Detail Screen (`app/(servicegroup)/servicedetail.tsx`)

Replace the current `handlePay` which just navigates to `orderproduct`.

**New flow**:

1. On screen load: call `getServiceContact(serviceId)` to check if already paid
   - If paid: show contact details immediately (phone, email with copy buttons)
   - If not paid: show "Reveal Contact — $0.10" button

2. On "Reveal Contact" tap:
   - Show bottom sheet or modal with payment confirmation:
     - "Pay $0.10 in USDC to reveal contact details"
     - "Fee split: Provider gets $0.025, you get $0.0125 cashback"
     - Token selector (USDC / USDT — pill buttons)
     - "Pay & Reveal" button

3. Payment flow (same signing pattern as checkout):
   - Call `prepareContactFee(serviceId, token)`
   - Sign approve tx → broadcast → get approveTxHash
   - Sign payFee tx → broadcast → get payFeeTxHash
   - Call `submitContactFee(serviceId, approveTxHash, payFeeTxHash)`
   - On success: show contact details with slide-in animation

4. Loading states: "Approving token..." → "Processing payment..." → "Revealing contact..."

**UI updates**:
- Remove the old `CONTACT_FEE` constant usage
- Add a "Contact Details" card that shows phone/email/address after payment
- Add a locked state with a lock icon + "$0.10" badge before payment
- Keep the existing service detail card (title, image, description)

#### 8. Update Constants

In `constants/constantValues.js`, update or remove `CONTACT_FEE=500` — it's no longer used (fee comes from the contract/backend).

## Patterns To Follow

### Backend
- **Response format**: `utils.SuccessResponse(c, statusCode, message, data)` / `utils.ErrorResponse(c, statusCode, message, errors)`
- **Auth**: `getUserFromContext(c)` for authenticated user
- **Tx preparation**: Return `UnsignedTx` JSON (same struct from `blockchain/token.go`)
- **Tx verification**: Use receipt status switch: "confirmed" → proceed, "error" → 503, "pending" → 409
- **BigInt**: Use `math/big` for all token amounts

### Mobile
- **Signing**: `signTransaction()` from `services/wallet.ts`
- **Broadcasting**: `getProvider('sonic').broadcastTransaction(signedTx)`
- **Auth headers**: `getAuthHeaders()` from `walletApi.ts` pattern
- **Design**: Brown/gold theme, `LinearGradient` buttons, Ionicons, white cards with `#E8D5C0` borders

## What NOT To Do

- Do NOT modify the smart contracts
- Do NOT modify the wallet service
- Do NOT change the ProductEscrow/checkout flow
- Do NOT add price oracles (stablecoins are 1:1 with USD)
- Do NOT add new npm dependencies unless absolutely necessary
- Do NOT use float64 for token amounts in the backend

## Verification

### Backend
```bash
cd tsa-api-go
go build ./...          # Must compile
go test ./... -v        # All tests must pass
```

Test with curl:
1. `GET /api/services/:id/contact` → 402 (not paid)
2. `POST /api/services/:id/prepare-contact-fee` → get unsigned txs
3. Verify tx data is valid ABI-encoded `payContactFee` call

### Mobile
```bash
cd tsa-app
npx expo start          # Must start without errors
```

Test flow:
1. Navigate to a service detail
2. See "Reveal Contact — $0.10" button
3. Tap → see payment confirmation modal
4. Pay → sign → contact details revealed
5. Revisit same service → contact details shown immediately (already paid)
