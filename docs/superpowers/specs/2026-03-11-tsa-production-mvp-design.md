# TSA Platform — Production MVP Design Spec

**Date:** 2026-03-11
**Status:** Approved by client

## Overview

Crypto-native marketplace platform on Sonic network with in-app wallets, smart contract escrow checkout, P2P fiat-to-crypto trading, and multi-party fee distribution.

## Tech Stack

- **Mobile:** React Native / Expo (v54), expo-router v6
- **API:** Express.js, MongoDB (Mongoose)
- **Blockchain:** Sonic network (EVM-compatible)
- **Tokens:** MCGP (platform token, ERC-20), USDT, USDC (stablecoins on Sonic)
- **KYC:** Smile ID
- **Email:** Mailjet
- **Image uploads:** Cloudinary (already integrated)
- **Wallet:** In-app EOA wallet (keys generated and stored on device, encrypted)

## Architecture

```
Mobile App (React Native/Expo)
    ↕ REST API (HTTPS)
API Server (Express.js + MongoDB)
    ↕ ethers.js
Sonic Blockchain
    ├── MCGP Token (ERC-20)
    ├── USDT / USDC (ERC-20)
    ├── Escrow Contract (holds funds during product orders)
    ├── P2P Escrow Contract (holds USDT during fiat trades)
    ├── Payout Splitter Contract (distributes fees to parties)
    └── Existing Swap Contract (USDT/USDC ↔ MCGP)

External Services:
    ├── Smile ID (KYC verification)
    ├── Mailjet (transactional email)
    └── Cloudinary (image uploads - already integrated)
```

### Key Architectural Decisions

- **Wallet keys live on-device only** — server never holds private keys. Server stores public address only.
- **All fund movements require user signature** — API prepares unsigned transactions, mobile app signs with local key, submits to chain.
- **Smart contracts enforce business rules** — escrow holds, split percentages, refund timelines are on-chain, not trust-based.
- **MongoDB remains the database** — stores user profiles, products, orders, transaction history (mirrors on-chain events for fast queries).
- **Existing codebase is extended**, not rewritten — fix what's broken, replace what's mocked, add what's missing.

## What's Currently Built

- Login & signup (individual, 3-step with document/facial upload)
- Basic asset/portfolio display with price fetching
- Product CRUD & merchant inventory management
- Shopping cart (backend only)
- Category management
- Image uploads (Cloudinary)
- Admin user listing
- Basic security (JWT auth, rate limiting, CORS)

## Sub-Projects (Build Order)

### 1. Foundation Fixes

Fix broken routing, consolidate API URLs, remove dead code. Everything else builds on a working app.

**Scope:**
- Uncomment navigation redirects in app/index.tsx
- Consolidate 3 conflicting API base URLs into single environment-based config
- Remove duplicate AuthContext (keep AppContext only)
- Clean up commented code, @ts-ignore directives
- Validate required env vars at server startup

### 2. KYC Verification (Smile ID)

Replace mocked verification with real Smile ID integration.

**Scope:**
- Integrate Smile ID SDK (React Native) for mobile
- Integrate Smile ID server-side API for verification callbacks
- Replace simulated BVN verification with real Smile ID BVN lookup
- Replace simulated document OCR with real Smile ID document verification
- Replace simulated facial recognition with real Smile ID liveness/selfie check
- Admin approval/rejection workflow (already partially exists)
- Verification status notifications via Mailjet

### 3. Wallet System (EOA + Key Management)

Generate wallets on device, encrypted key storage, backup mechanism.

**Scope:**
- Generate EOA wallet (private key + address) at signup using ethers.js
- Encrypt private key with user's password/biometrics, store in device secure storage
- Seed phrase backup flow (generate, display, confirm user wrote it down)
- Store public wallet address on server (User model)
- Balance display (MCGP, USDT, USDC) by querying Sonic RPC
- Send/receive UI connected to real wallet
- Transaction signing flow (API sends unsigned tx → app signs → submits to chain)

### 4. Smart Contracts (Sonic Network)

Escrow, fee distribution, refund logic — all on-chain.

**Scope:**

#### Product Escrow Contract
- Buyer checkout → contract debits buyer wallet, holds funds
- On buyer confirmation of receipt → release funds:
  - Seller receives: product price + shipping fees
  - System fee (10% of product price) distributed: 5% system wallet, 2.5% buyer cashback, 2.5% buyer's direct upline
- **MCGP exception:** 0% system fee when paying with MCGP
- Auto-refund after 30 days:
  - 100% refund to buyer if seller never reported delivery
  - 90% refund to buyer + 10% split (7% system, 3% seller) if seller delivered with proof but buyer didn't collect

#### Service Contact Fee Contract
- Fixed $0.10 contact fee
- Instant 4-party distribution:
  - $0.05 → system wallet
  - $0.025 → service provider
  - $0.0125 → requesting user
  - $0.0125 → requesting user's direct upline
- Instantly reveals service provider contact details on user's screen

#### Integration with Existing Swap Contract
- Connect existing admin-funded swap contract for USDT/USDC ↔ MCGP

### 5. Marketplace + Smart Contract Checkout

Connect existing product/cart system to smart contract escrow.

**Scope:**
- Create Order model (currently missing — orders aren't persisted)
- Connect checkout to escrow contract (replace hardcoded checkout screen)
- Shipping fee calculation by seller-defined zones:
  - Same residential area: $0
  - Same state (different area): seller-defined
  - Same country (different state): seller-defined
  - International: seller-defined
- Auto-calculate total: product price + shipping + system fee (10%)
- Buyer confirmation flow ("I received the product")
- Order history and tracking
- Delivery proof submission by seller
- 30-day escrow timer with auto-refund logic
- Dispute flow (admin resolution)

### 6. P2P Trading

Buy/sell USDT with fiat, smart contract escrow, bank transfer.

**Scope:**
- Trade listing: merchants list "sell X USDT for Y [currency]" with accepted payment methods
- Smart contract escrow: seller's USDT locked when trade initiated
- Bank transfer flow: buyer sends fiat to seller's bank account
- Seller confirms fiat received → contract releases USDT to buyer
- Dispute resolution: admin can release or refund via contract
- MVP payment method: bank transfer only
- Country-specific currency support (seller selects their country/currency when listing)

### 7. Swap Integration

Connect existing swap contract to mobile app.

**Scope:**
- UI for swapping USDT/USDC ↔ MCGP (replace current mock swap screen)
- Connect to existing admin-funded swap contract on Sonic
- Display exchange rate from contract
- Transaction signing and submission
- Swap history

### 8. Merchant Request + Admin Approval

Users request merchant access, admin approves.

**Scope:**
- "Become a Merchant" button under Services screen
- Merchant request form (business type: general products, digital products, P2P merchant, service provider)
- Admin notification of new merchant requests
- Admin approval/rejection flow in admin portal
- On approval: user role updated, merchant portal access granted
- Merchant portal already partially exists (inventory, dashboard)

### 9. Corporate/Organization Signup

Extend signup for business entities.

**Scope:**
- Add account type selection at signup start (Individual / Corporate)
- Corporate additional fields:
  - Company name
  - Registration number
  - Country of incorporation
  - Business type (sole proprietor, LLC, corporation, etc.)
  - Director/representative details
  - Certificate of incorporation upload
  - Tax ID
- Representative goes through same Smile ID KYC
- Business document verification (manual review by admin)

### 10. Email System (Mailjet)

Transactional emails for all critical flows.

**Scope:**
- Mailjet integration (replace empty email stubs)
- Email templates:
  - Verification approved/rejected
  - Password reset
  - Order confirmation
  - Order delivery confirmation
  - Escrow release/refund notification
  - Merchant request approved/rejected
  - P2P trade notifications
- Password reset flow (currently missing entirely — users locked out)

## Missing Critical Features (Included Across Sub-Projects)

- Password reset flow (Sub-project 10)
- Order model & order history (Sub-project 5)
- Referral/upline tracking in User model (Sub-project 4)
- Delivery confirmation & dispute flow (Sub-project 5)
- 30-day escrow timer with auto-refund (Sub-project 4)
- Env var validation at startup (Sub-project 1)
- Zero test coverage (add tests per sub-project)

## Deferred (Post-MVP)

- Multi-level referral system (currently single-level only)
- Advanced admin analytics dashboard
- SMS/Twilio notifications
- Connect existing wallet (WalletConnect) — currently in-app wallet only
- Advanced dispute chat system
- Push notifications

## Fee Model Reference

### Product Checkout
- System fee: 10% of product price
- Distribution on buyer confirmation: 5% system wallet, 2.5% buyer cashback, 2.5% buyer's direct upline
- MCGP payment: 0% system fee
- Shipping: seller-defined by zone

### Product Refund (after 30 days)
- Seller never shipped: 100% refund to buyer
- Seller shipped (with proof) but buyer didn't collect: 90% refund to buyer, 7% system wallet, 3% seller

### Service Contact Fee
- Fixed $0.10 per contact
- Distribution: $0.05 system, $0.025 service provider, $0.0125 requesting user, $0.0125 user's direct upline

### Swap
- Fees collected by existing swap contract (goes to system wallet instantly)

## Wallet Approach

**EOA wallet generated in-app (not MPC)**

Rationale: MPC requires either expensive infrastructure (custom build with HSMs) or third-party providers ($1k+/month at scale) that hold part of the key on their servers. For MVP, a standard EOA wallet achieves the same security guarantee — user owns keys entirely on their device, encrypted with biometrics/password, nothing moves without their signature. Smart contracts enforce all business rules on-chain. Can upgrade to MPC later if scale justifies it.
