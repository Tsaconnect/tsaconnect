# Dashboard Redesign Spec

## Problem

The current dashboard (`screens/dashboard2.tsx`, ~2000 lines) has poor information hierarchy:
- "Debit Account" selector and "Selected Asset Summary" consume most of the viewport showing the same MCGP data twice
- Quick Actions are buried below the fold
- No transaction history visible
- No clear visual hierarchy between wallet and marketplace actions

## Design

### Layout (top to bottom)

**1. Hero Balance Card** (dark gradient, `#1A1A1A` → `#2D2D2D`)
- Total portfolio value in large type (26px, weight 800)
- Daily change percentage (green/red) — note: backend currently returns 0; show only when non-zero, otherwise omit the line
- Eye icon toggle to hide/show values (existing feature, preserved)
- Quick Actions row embedded at bottom of card with gold (`#D4AF37`) circular icon backgrounds:
  - Deposit (navigates to `/fund`)
  - Send (navigates to `/send`)
  - Swap (navigates to `/swap`)
  - Buy (navigates to marketplace)

**2. Assets Section** (white card, compact rows)
- Header row: "Assets" label + "See all →" link
- One row per token (MCGP, USDC, USDT), each showing:
  - Colored circle avatar with initial letter
  - Token name + full name
  - Balance amount + USD value (right-aligned)
- Rows are tappable — navigate to asset detail or expand inline
- MCGP: gold gradient, USDC: blue gradient, USDT: green gradient

**3. Recent Activity Section** (white card)
- Header row: "Recent Activity" label + "View all →" link
- Shows last 3-5 transactions via `getTransactionHistory(1, 5)`
- Each row: type icon (send/receive/swap), derived description, amount, timestamp
  - **Deriving description from Transaction fields**: use `type` + `tokenSymbol` + truncated address, e.g. "Sent 50 USDC to 0xab...cd", "Received 10 MCGP from 0x12...ef", "Swapped USDC → MCGP"
- Empty state: "No recent transactions" centered text
- "View all" navigates to full transaction history

**4. Trade Now Section** (preserved from current, below fold)
- Buy Product, Order Services, Trade and Earn cards
- Only shown when scrolled down — secondary to wallet info

**5. Backup Reminder Banner** (conditional, preserved)
- Shown when seed phrase hasn't been backed up
- Preserves existing dismiss/snooze logic: checks `seedPhraseBackedUp`, `backupBannerDismissed`, and `backupBannerRemindAt` from AsyncStorage
- Same styling as current

### What's Removed

- **Debit Account selector** — moves to send/transfer flow screens (`/send`, `/sendfiat`). When user initiates a send, they pick the source asset there.
- **Selected Asset Summary card** — redundant with the compact asset list. All the same info (name, balance, USD value, status) is in the asset rows.

### Data Sources

All data sources already exist — no new API endpoints needed:

| Section | Source | Method | Notes |
|---------|--------|--------|-------|
| Total balance | Portfolio API | `api.getPortfolioAssets()` → `totals.usdValue` | `usdValue` is a number |
| Daily change | Portfolio API | `totals.dailyChange` | Currently always 0 from backend; only render when non-zero |
| Asset balances | Portfolio API | `api.getPortfolioAssets()` → `assets` array | Each `Asset` has numeric `balance` and `usdValue` — use this as canonical source rather than `walletApi` to avoid string-to-number conversion |
| Transactions | Wallet API | `import { getTransactionHistory } from '@/services/walletApi'` | Returns `ApiResponse<{ transactions: Transaction[]; total: number }>` — access via `response.data.transactions` |
| Backup status | AsyncStorage | Keys: `seedPhraseBackedUp`, `backupBannerDismissed`, `backupBannerRemindAt` | Preserve existing snooze/dismiss logic from current dashboard |

### Component Structure

Refactor the monolithic `dashboard2.tsx` (2000 lines) into focused components. Create new `components/dashboard/` directory:

```
screens/dashboard2.tsx              — orchestrator, data fetching, pull-to-refresh
components/dashboard/
  BalanceCard.tsx                    — hero card + quick actions
  AssetList.tsx                      — compact token rows
  RecentActivity.tsx                 — transaction list + empty state
  TradeActions.tsx                   — buy/order/trade cards (extracted from current)
  BackupBanner.tsx                   — seed phrase reminder (extracted from current)
```

Each component receives props — no internal data fetching. The parent dashboard handles all API calls and passes data down.

### Navigation

Quick action destinations (unchanged from current):
- Deposit → `/fund` (opens fund options modal or screen)
- Send → `/send` (opens send options modal or screen)
- Swap → `/swap`
- Buy → `/(dashboard)/(tabs)/marketplace` (tab switch)

Asset row tap → expand inline to show 24h chart / more details (stretch goal, v2)

### Styling

- Balance card: dark gradient (`#1A1A1A` → `#2D2D2D`), white text, gold accent buttons
- Asset avatars: per-token gradient (MCGP gold, USDC blue, USDT green)
- Cards: white bg, 14px border-radius, subtle shadow (consistent with marketplace tiles)
- Section headers: 13px weight 700, with gold "See all →" links
- Overall background: `#F5F5F7` (matches existing app background)
- Minimum touch target: 44px for all interactive elements

### Error & Loading States

- **Loading**: Skeleton placeholders for balance card and asset rows (not a full-screen spinner). Simple opacity pulse animation.
- **Error**: Inline error banner with "Tap to retry" (same pattern as marketplace)
- **Pull-to-refresh**: Refresh all data sources simultaneously

## Out of Scope

- Asset detail screen (tapping an asset row)
- Transaction detail screen
- Portfolio chart / sparklines
- Push notifications for transactions
- Debit account selector in send/transfer flows (separate task)
