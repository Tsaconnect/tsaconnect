package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

// Security-review fixes for the LP side (review 2026-10-14).

func (r *lpRig) lpRowOf(t *testing.T, user uuid.UUID) *models.PaymentMerchant {
	t.Helper()
	lp, err := NewPMService(r.db).GetByUserIDKind(context.Background(), user, models.PaymentMerchantKindLP)
	if err != nil {
		t.Fatal(err)
	}
	return lp
}

// Finding 1: re-activating from a new wallet while the old one still holds
// principal, earnings or a pending withdrawal is refused — the app could no longer build the
// withdraw/claim for it, and the old wallet would stay a depositor.
func TestVaultLP_ReactivateRefusedWhileOldWalletHasFunds(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(r *lpRig, k string)
	}{
		{"principal", func(r *lpRig, k string) { r.vault.principal[k] = usdt18(10) }},
		{"claimable", func(r *lpRig, k string) { r.vault.claimable[k] = big.NewInt(1) }},
		{"pending withdrawal", func(r *lpRig, k string) { r.vault.pending[k] = big.NewInt(1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newLPRig(t)
			user, old := r.activeLP(t)
			tc.set(r, strings.ToLower(old))
			next := newLPWallet()
			r.link(t, user, next)
			if _, err := r.svc.Activate(context.Background(), user, next); !errors.Is(err, ErrLPOldWalletHasFunds) {
				t.Fatalf("err = %v, want ErrLPOldWalletHasFunds", err)
			}
			if lp := r.lpRowOf(t, user); lp.OperatorWalletAddress != old {
				t.Fatalf("wallet re-pointed to %s", lp.OperatorWalletAddress)
			}
			if !r.vault.allowed[strings.ToLower(old)] || r.vault.allowed[strings.ToLower(next)] {
				t.Fatal("allow-list changed on a refused re-activation")
			}
		})
	}
}

// Finding 1+2: an empty old wallet is revoked on re-activation, remembered,
// and the sync keeps it revoked even when that first revoke never landed.
func TestVaultLP_ReactivateRevokesOldWalletAndSyncKeepsItRevoked(t *testing.T) {
	r := newLPRig(t)
	user, old := r.activeLP(t)
	next := newLPWallet()
	r.link(t, user, next)
	if _, err := r.svc.Activate(context.Background(), user, next); err != nil {
		t.Fatal(err)
	}
	if r.vault.allowed[strings.ToLower(old)] || !r.vault.allowed[strings.ToLower(next)] {
		t.Fatalf("allowed old=%t new=%t", r.vault.allowed[strings.ToLower(old)], r.vault.allowed[strings.ToLower(next)])
	}
	lp := r.lpRowOf(t, user)
	if lp.PreviousOperatorWallets == nil || !strings.Contains(*lp.PreviousOperatorWallets, old) {
		t.Fatalf("previous wallets = %v", lp.PreviousOperatorWallets)
	}
	r.vault.allowed[strings.ToLower(old)] = true // the revoke never landed
	if _, err := r.svc.SyncDepositors(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.vault.allowed[strings.ToLower(old)] || !r.vault.allowed[strings.ToLower(next)] {
		t.Fatal("sync did not revoke the previous wallet")
	}
	// Back to the old wallet: it is current again, so the sync allows it.
	r.link(t, user, old)
	if _, err := r.svc.Activate(context.Background(), user, old); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.SyncDepositors(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.vault.allowed[strings.ToLower(old)] || r.vault.allowed[strings.ToLower(next)] {
		t.Fatalf("after moving back: old=%t next=%t", r.vault.allowed[strings.ToLower(old)], r.vault.allowed[strings.ToLower(next)])
	}
}

// Finding 2: the allow-list follows eligibility and the account's status, not
// only the row's; deposits re-check eligibility. Withdraw/claim never do.
func TestVaultLP_SyncRevokesIneligibleOrSuspended(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"kyc revoked", "UPDATE users SET verification_status = 'pending' WHERE id = ?"},
		{"suspended", "UPDATE users SET account_status = 'suspended' WHERE id = ?"},
		{"deleted", "UPDATE users SET account_status = 'deleted' WHERE id = ?"},
		{"user gone", "DELETE FROM users WHERE id = ?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newLPRig(t)
			user, wallet := r.activeLP(t)
			if err := r.db.Exec(tc.sql, user).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := r.svc.SyncDepositors(context.Background()); err != nil {
				t.Fatal(err)
			}
			if r.vault.allowed[strings.ToLower(wallet)] {
				t.Fatal("still allowed to deposit")
			}
		})
	}
	r := newLPRig(t)
	user, _ := r.activeLP(t)
	r.db.Exec("UPDATE users SET verification_status = 'pending' WHERE id = ?", user)
	if _, err := r.svc.PrepareDeposit(context.Background(), user, "10"); !errors.Is(err, ErrLPNotEligible) {
		t.Fatalf("deposit err = %v, want ErrLPNotEligible", err)
	}
	if _, err := r.svc.PrepareWithdraw(context.Background(), user, "1"); err != nil {
		t.Fatalf("withdraw must never be gated: %v", err)
	}
	if _, err := r.svc.PrepareClaim(context.Background(), user, ""); err != nil {
		t.Fatalf("claim must never be gated: %v", err)
	}
}

// A database error deciding eligibility never revokes: the row is skipped.
func TestVaultLP_SyncKeepsAllowedOnEligibilityReadError(t *testing.T) {
	r := newLPRig(t)
	_, wallet := r.activeLP(t)
	r.svc.c.CompanyUser = func(context.Context) (uuid.UUID, error) { return uuid.Nil, errors.New("db down") }
	if _, err := r.svc.SyncDepositors(context.Background()); err == nil {
		t.Fatal("want the read error reported")
	}
	if !r.vault.allowed[strings.ToLower(wallet)] {
		t.Fatal("a transient read error revoked an eligible LP")
	}
}

// seedSliceLP is an old slice-pool LP row: active, wallet typed in, never
// proven (vault_activated_at NULL).
func (r *lpRig) seedSliceLP(t *testing.T, user uuid.UUID, wallet string) {
	t.Helper()
	chain := r.svc.c.Token.ChainID
	if err := r.db.Create(&models.PaymentMerchant{ID: uuid.New(), UserID: user, Name: "old", Status: models.PaymentMerchantStatusActive,
		Kind: models.PaymentMerchantKindLP, PoolChainID: &chain, OperatorWalletAddress: wallet}).Error; err != nil {
		t.Fatal(err)
	}
}

// Finding 3: an old slice-pool row is not a vault LP until it activates.
func TestVaultLP_UnprovenSliceRowIsNotAVaultLP(t *testing.T) {
	r := newLPRig(t)
	claimer := seedVerifiedUser(t, r.db)
	victim := newLPWallet()
	r.seedSliceLP(t, claimer, victim)
	r.vault.allowed[strings.ToLower(victim)] = true // allow-listed by the old sync
	r.vault.principal[strings.ToLower(victim)] = usdt18(100)
	ctx := context.Background()

	me, err := r.svc.Me(ctx, claimer)
	if err != nil || me.Status != LiquidityStatusNone || me.Wallet != "" || me.Principal != "" {
		t.Fatalf("me = %+v %v, want status none and no position", me, err)
	}
	if _, err := r.svc.Activity(ctx, claimer, "", 10); !errors.Is(err, ErrNotLiquidityProvider) {
		t.Fatalf("activity err = %v", err)
	}
	if _, err := r.svc.PrepareWithdraw(ctx, claimer, "1"); !errors.Is(err, ErrNotLiquidityProvider) {
		t.Fatalf("withdraw err = %v", err)
	}
	if _, err := r.svc.PrepareClaim(ctx, claimer, ""); !errors.Is(err, ErrNotLiquidityProvider) {
		t.Fatalf("claim err = %v", err)
	}
	if _, err := r.svc.ClaimForMe(ctx, claimer); !errors.Is(err, ErrNotLiquidityProvider) {
		t.Fatalf("claim-for err = %v", err)
	}
	if _, err := r.svc.SyncDepositors(ctx); err != nil {
		t.Fatal(err)
	}
	if r.vault.allowed[strings.ToLower(victim)] {
		t.Fatal("sync allow-lists an unproven wallet")
	}

	// The wallet's real owner proves it and activates: the unproven row never
	// blocks them (wallet_in_use) and lets go of the wallet.
	owner := seedVerifiedUser(t, r.db)
	r.link(t, owner, victim)
	if _, err := r.svc.Activate(ctx, owner, victim); err != nil {
		t.Fatalf("owner activate: %v", err)
	}
	if lp := r.lpRowOf(t, claimer); lp.OperatorWalletAddress != "" {
		t.Fatalf("unproven row still holds %s", lp.OperatorWalletAddress)
	}
	if lp := r.lpRowOf(t, owner); lp.VaultActivatedAt == nil {
		t.Fatal("activation did not set vault_activated_at")
	}
}

// An old row re-proves through Activate and becomes a vault LP.
func TestVaultLP_ActivateUpgradesOldRow(t *testing.T) {
	r := newLPRig(t)
	user := seedVerifiedUser(t, r.db)
	typed, proven := newLPWallet(), newLPWallet()
	r.seedSliceLP(t, user, typed)
	r.vault.principal[strings.ToLower(typed)] = usdt18(5) // not this account's to protect
	r.vault.allowed[strings.ToLower(typed)] = true
	r.link(t, user, proven)
	me, err := r.svc.Activate(context.Background(), user, proven)
	if err != nil || me.Status != LiquidityStatusActive || me.Wallet != proven {
		t.Fatalf("me = %+v %v", me, err)
	}
	if lp := r.lpRowOf(t, user); lp.VaultActivatedAt == nil {
		t.Fatal("vault_activated_at not set")
	}
	if r.vault.allowed[strings.ToLower(typed)] {
		t.Fatal("the typed-in wallet is still a depositor")
	}
}

// Finding 4: Me's pool card is read at most once per TTL for everyone.
func TestVaultLP_PoolViewCached(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	for range 3 {
		if _, err := r.svc.Me(ctx, uuid.New()); err != nil {
			t.Fatal(err)
		}
	}
	if r.vault.stateCalls != 1 {
		t.Fatalf("State read %d times, want 1", r.vault.stateCalls)
	}
	r.now = r.now.Add(lpPoolViewTTL)
	if _, err := r.svc.Me(ctx, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if r.vault.stateCalls != 2 {
		t.Fatalf("State read %d times after the TTL, want 2", r.vault.stateCalls)
	}
}

// fullReplayActivity is the pre-fix Activity: every ledger row replayed. The
// bounded replay must answer exactly as it did.
func fullReplayActivity(t *testing.T, svc *VaultLPService, wallet string) []string {
	t.Helper()
	var rows []models.LiquidityLedgerEntry
	if err := svc.vaultScope(svc.c.DB).Where("kind IN ?", []string{VaultEventDeposited, VaultEventWithdrawn, VaultEventClaimed, VaultEventFilled}).
		Order("block ASC").Order("log_index ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	me := strings.ToLower(wallet)
	principal := map[string]*big.Int{}
	total := new(big.Int)
	var items []LiquidityVaultActivity
	for _, row := range rows {
		who := strings.ToLower(row.Account)
		amt := bigFromDecimal(row.Amount)
		if principal[who] == nil {
			principal[who] = new(big.Int)
		}
		switch row.Kind {
		case VaultEventDeposited:
			principal[who].Add(principal[who], amt)
			total.Add(total, amt)
			if who == me {
				items = append(items, LiquidityVaultActivity{Kind: "deposit", Amount: svc.units(amt), At: row.BlockTime})
			}
		case VaultEventWithdrawn:
			principal[who].Sub(principal[who], amt)
			total.Sub(total, amt)
			if who == me {
				items = append(items, LiquidityVaultActivity{Kind: "withdrawal", Amount: svc.units(amt), At: row.BlockTime})
			}
		case VaultEventClaimed:
			if who == me {
				items = append(items, LiquidityVaultActivity{Kind: "claim", Amount: svc.units(amt), At: row.BlockTime})
			}
		case VaultEventFilled:
			mine := principal[me]
			spread := bigFromDecimal(row.Spread)
			if mine == nil || mine.Sign() == 0 || total.Sign() == 0 || spread.Sign() == 0 {
				continue
			}
			share := new(big.Int).Mul(spread, mine)
			share.Quo(share, total)
			if share.Sign() > 0 {
				items = append(items, LiquidityVaultActivity{Kind: "earned", Amount: svc.units(share), At: row.BlockTime})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Kind + "=" + it.Amount + "@" + it.At.Format(time.RFC3339)
	}
	return out
}

// Finding 4: Activity replays only from the LP's first deposit (the earlier
// pool total summed in the database) and answers exactly as the full replay.
func TestVaultLP_ActivityBoundedReplayMatchesFullReplay(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	seedActivityLedger(t, r.db, wallet, 1)
	want := fullReplayActivity(t, r.svc, wallet)
	page, err := r.svc.Activity(context.Background(), user, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(page.Items))
	for i, it := range page.Items {
		got[i] = it.Kind + "=" + it.Amount + "@" + it.At.Format(time.RFC3339)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("bounded replay:\n%s\nfull replay:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	earned := 0
	for _, g := range got {
		if strings.HasPrefix(g, "earned=") {
			earned++
		}
	}
	if earned != 4 {
		t.Fatalf("earned lines = %d, want 4 (fixture sanity)", earned)
	}
}

// Finding 5: the company check fails closed — only "no company account
// configured" lets activation through.
func TestVaultLP_CompanyCheckFailsClosed(t *testing.T) {
	r := newLPRig(t)
	user := seedVerifiedUser(t, r.db)
	wallet := newLPWallet()
	r.link(t, user, wallet)
	r.svc.c.CompanyUser = func(context.Context) (uuid.UUID, error) { return uuid.Nil, errors.New("connection reset") }
	_, err := r.svc.Activate(context.Background(), user, wallet)
	if err == nil || errors.Is(err, ErrLPNotEligible) || errors.Is(err, ErrCompanyUserCannotBeLP) {
		t.Fatalf("err = %v, want an internal error", err)
	}
	r.svc.c.CompanyUser = func(context.Context) (uuid.UUID, error) {
		return uuid.Nil, fmt.Errorf("%w: IT_PM_ID is not set", ErrCompanyPMUnusable)
	}
	if _, err := r.svc.Activate(context.Background(), user, wallet); err != nil {
		t.Fatalf("no company account configured: %v", err)
	}
}

// Finding 6: a rate-limited gas-free claim is refused before any chain read.
func TestVaultLP_ClaimForRateLimitBeforePositionRead(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	r.vault.claimable[strings.ToLower(wallet)] = usdt18(2)
	if _, err := r.svc.ClaimForMe(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	r.vault.positionErr = errors.New("rpc must not be read")
	if _, err := r.svc.ClaimForMe(context.Background(), user); !errors.Is(err, ErrLPClaimRateLimit) {
		t.Fatalf("err = %v, want rate limited before the RPC", err)
	}
}

// Finding 7: a malformed or zero claim destination is the caller's error.
func TestVaultLP_PrepareClaimValidatesTo(t *testing.T) {
	r := newLPRig(t)
	user, _ := r.activeLP(t)
	for _, to := range []string{"nothex", "0x1234", "0x0000000000000000000000000000000000000000"} {
		if _, err := r.svc.PrepareClaim(context.Background(), user, to); !errors.Is(err, ErrLPClaimTo) {
			t.Fatalf("to=%q err = %v, want ErrLPClaimTo", to, err)
		}
	}
	if plan, err := r.svc.PrepareClaim(context.Background(), user, newLPWallet()); err != nil || plan.Step != "claim" {
		t.Fatalf("valid to: %+v %v", plan, err)
	}
}

// seedActivityLedger writes a ledger where other LPs deposit, withdraw and
// fills land before and after wallet's first deposit; unit scales amounts.
func seedActivityLedger(t *testing.T, db *gorm.DB, wallet string, unit int64) {
	t.Helper()
	a, b := newLPWallet(), newLPWallet()
	base := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	amt := func(n int64) *string { s := new(big.Int).Mul(big.NewInt(n), big.NewInt(unit)).String(); return &s }
	type ev struct {
		kind, who    string
		amount       int64
		spread       int64
		block, logIx uint64
	}
	events := []ev{
		{VaultEventDeposited, a, 300, 0, 10, 0},
		{VaultEventFilled, "", 50, 7, 11, 0}, // before me: earns me nothing
		{VaultEventDeposited, b, 120, 0, 12, 0},
		{VaultEventWithdrawn, a, 40, 0, 12, 1},
		{VaultEventDeposited, wallet, 77, 0, 13, 2}, // my first deposit
		{VaultEventFilled, "", 900, 13, 13, 3},
		{VaultEventDeposited, a, 61, 0, 14, 0},
		{VaultEventFilled, "", 900, 29, 15, 0},
		{VaultEventWithdrawn, wallet, 20, 0, 16, 0},
		{VaultEventFilled, "", 900, 101, 17, 0},
		{VaultEventClaimed, wallet, 3, 0, 18, 0},
		{VaultEventDeposited, wallet, 15, 0, 19, 0},
		{VaultEventWithdrawn, b, 120, 0, 20, 0},
		{VaultEventFilled, "", 900, 47, 21, 0},
	}
	for i, e := range events {
		row := models.LiquidityLedgerEntry{ID: uuid.New(), ChainID: poolTestChain, Vault: strings.ToLower(testVaultAddr.Hex()),
			TxHash: fmt.Sprintf("0x%064x", i+1), LogIndex: uint(e.logIx), Block: e.block, Kind: e.kind, Account: e.who,
			Amount: amt(e.amount), BlockTime: base.Add(time.Duration(i) * time.Minute)}
		if e.kind == VaultEventFilled {
			row.Spread = amt(e.spread)
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// The bounded replay's SQL (numeric SUM of 18-decimal amounts, the block
// bound) on PostgreSQL, against the full replay.
func TestVaultLP_ActivityBoundedReplayPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "lpact_", &models.User{}, &models.PaymentMerchant{}, &models.LiquidityLedgerEntry{})
	user, wallet := uuid.New(), newLPWallet()
	if err := db.Exec("INSERT INTO users (id, name, username, email, password) VALUES (?, 'lp', ?, ?, 'x')",
		user, user.String(), user.String()+"@t").Error; err != nil {
		t.Fatal(err)
	}
	chain, now := int64(poolTestChain), time.Now()
	lp := &models.PaymentMerchant{ID: uuid.New(), UserID: user, Name: "lp", Status: models.PaymentMerchantStatusActive,
		Kind: models.PaymentMerchantKindLP, PoolChainID: &chain, OperatorWalletAddress: wallet}
	if err := db.Create(lp).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(lp).Update("vault_activated_at", now).Error; err != nil {
		t.Fatal(err)
	}
	seedActivityLedger(t, db, wallet, 1_000_000_000_000_000_000) // 1e18: past int64 once summed
	svc, err := NewVaultLPService(VaultLPConfig{DB: db, PM: NewPMService(db), Vault: newFakeLPVault(), Tokens: &fakeAllowances{allowance: new(big.Int)},
		Sender: &lpSender{}, Token: PoolToken{ChainID: poolTestChain, ChainKey: "bsc", Symbol: "USDT", Address: poolTestUSDT, Decimals: 18}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.Activity(context.Background(), user, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(page.Items))
	for i, it := range page.Items {
		got[i] = it.Kind + "=" + it.Amount + "@" + it.At.Format(time.RFC3339)
	}
	if want := fullReplayActivity(t, svc, wallet); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("bounded replay:\n%s\nfull replay:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
