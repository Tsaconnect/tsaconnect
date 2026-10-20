package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// fakeLPVault is the vault as the LP side sees it.
type fakeLPVault struct {
	mu        sync.Mutex
	principal map[string]*big.Int
	claimable map[string]*big.Int
	pending   map[string]*big.Int // withdrawal pending, per wallet
	allowed   map[string]bool
	state     VaultState
	// stateCalls counts State reads; positionErr fails every Position read.
	stateCalls  int
	positionErr error
	// locked: principal still inside its deposit lock, per wallet.
	locked map[string]*lpLock
	// limits are the contract's deposit limits; limitsErr fails every read,
	// limitsCalls counts them.
	limits      VaultLimits
	limitsErr   error
	limitsCalls int
}

type lpLock struct {
	amount *big.Int
	until  time.Time
}

func newFakeLPVault() *fakeLPVault {
	return &fakeLPVault{principal: map[string]*big.Int{}, claimable: map[string]*big.Int{}, pending: map[string]*big.Int{}, allowed: map[string]bool{},
		state:  VaultState{Balance: big.NewInt(0), TotalPendingWithdrawals: big.NewInt(0), Outstanding: big.NewInt(0), TotalPrincipal: big.NewInt(0)},
		limits: VaultLimits{MinDeposit: usdt18(5), MaxPerLP: usdt18(5_000), TotalCap: usdt18(50_000)}}
}

func (v *fakeLPVault) Limits(context.Context) (*VaultLimits, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.limitsCalls++
	if v.limitsErr != nil {
		return nil, v.limitsErr
	}
	l := v.limits
	return &l, nil
}

func (v *fakeLPVault) Address() string { return testVaultAddr.Hex() }
func (v *fakeLPVault) State(context.Context) (*VaultState, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.stateCalls++
	st := v.state
	return &st, nil
}
func (v *fakeLPVault) Position(_ context.Context, lp string) (*VaultPosition, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.positionErr != nil {
		return nil, v.positionErr
	}
	k := strings.ToLower(lp)
	p, c := v.principal[k], v.claimable[k]
	if p == nil {
		p = new(big.Int)
	}
	if c == nil {
		c = new(big.Int)
	}
	pos := &VaultPosition{Principal: new(big.Int).Set(p), Claimable: new(big.Int).Set(c), Allowed: v.allowed[k], Withdrawable: new(big.Int).Set(p),
		PendingWithdrawal: new(big.Int)}
	if w := v.pending[k]; w != nil {
		pos.PendingWithdrawal.Set(w)
	}
	if l := v.locked[k]; l != nil {
		pos.Withdrawable = new(big.Int).Sub(p, l.amount)
		pos.LockedUntil = l.until
	}
	return pos, nil
}
func lpTx(method string, args ...string) ([]byte, error) {
	return json.Marshal(blockchain.UnsignedTx{To: testVaultAddr.Hex(), Value: "0", ChainID: "97", Data: method + ":" + strings.Join(args, ":")})
}
func (v *fakeLPVault) PrepareDeposit(lp string, n *big.Int) ([]byte, error) {
	return lpTx("deposit", lp, n.String())
}
func (v *fakeLPVault) PrepareWithdraw(lp string, n *big.Int) ([]byte, error) {
	return lpTx("withdraw", lp, n.String())
}
func (v *fakeLPVault) PrepareClaim(lp string) ([]byte, error)       { return lpTx("claim", lp) }
func (v *fakeLPVault) PrepareClaimTo(lp, to string) ([]byte, error) { return lpTx("claimTo", lp, to) }
func (v *fakeLPVault) PrepareClaimFor(_, lp string) ([]byte, error) { return lpTx("claimFor", lp) }
func (v *fakeLPVault) PrepareSetDepositor(_, account string, allowed bool) ([]byte, error) {
	b := "false"
	if allowed {
		b = "true"
	}
	return lpTx("setDepositor", account, b)
}

type fakeAllowances struct{ allowance *big.Int }

func (f *fakeAllowances) GetTokenAllowance(string, string, string) (*big.Int, error) {
	return new(big.Int).Set(f.allowance), nil
}
func (f *fakeAllowances) PrepareERC20Approve(_, owner, _ string, n *big.Int) ([]byte, error) {
	return lpTx("approve", owner, n.String())
}

// lpSender is the operator: it records every call and applies setDepositor
// to the fake vault, as a mined tx would.
type lpSender struct {
	vault  *fakeLPVault
	mu     sync.Mutex
	calls  []string
	refuse error
}

func (s *lpSender) Address() string { return "0x00000000000000000000000000000000000000a1" }
func (s *lpSender) SignAndBroadcast(context.Context, []byte) (string, error) {
	return "", errors.New("send only")
}
func (s *lpSender) Send(_ context.Context, _ int64, build func() ([]byte, error)) (string, error) {
	if s.refuse != nil {
		return "", notBroadcast(s.refuse)
	}
	raw, err := build()
	if err != nil {
		return "", notBroadcast(err)
	}
	var u blockchain.UnsignedTx
	_ = json.Unmarshal(raw, &u)
	s.mu.Lock()
	s.calls = append(s.calls, u.Data)
	s.mu.Unlock()
	if parts := strings.Split(u.Data, ":"); parts[0] == "setDepositor" {
		s.vault.mu.Lock()
		s.vault.allowed[strings.ToLower(parts[1])] = parts[2] == "true"
		s.vault.mu.Unlock()
	}
	return "0xhash", nil
}

type lpRig struct {
	db     *gorm.DB
	vault  *fakeLPVault
	tokens *fakeAllowances
	sender *lpSender
	svc    *VaultLPService
	now    time.Time
}

func newLPRig(t *testing.T) *lpRig {
	t.Helper()
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	if err := db.AutoMigrate(&models.LiquidityLedgerEntry{}, &models.JobRunMarker{}, &models.PayMerchant{}); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"vault_activated_at DATETIME", "previous_operator_wallets TEXT"} {
		if err := db.Exec("ALTER TABLE payment_merchants ADD COLUMN " + col).Error; err != nil {
			t.Fatal(err)
		}
	}
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil
	vault := newFakeLPVault()
	r := &lpRig{db: db, vault: vault, tokens: &fakeAllowances{allowance: new(big.Int)}, sender: &lpSender{vault: vault}, now: time.Now()}
	svc, err := NewVaultLPService(VaultLPConfig{DB: db, PM: NewPMService(db), Vault: vault, Tokens: r.tokens, Sender: r.sender,
		Token: PoolToken{ChainID: poolTestChain, ChainKey: "bsc", Symbol: "USDT", Address: poolTestUSDT, Decimals: 18},
		Now:   func() time.Time { return r.now }})
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	return r
}

func newLPWallet() string { u := uuid.New(); return common.BytesToAddress(u[:]).Hex() }

func (r *lpRig) activeLP(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	user, wallet := seedVerifiedUser(t, r.db), newLPWallet()
	r.link(t, user, wallet)
	if _, err := r.svc.Activate(context.Background(), user, wallet); err != nil {
		t.Fatalf("activate: %v", err)
	}
	return user, wallet
}

// link is the account's proven wallet, as the handler passes it.
func (r *lpRig) link(t *testing.T, user uuid.UUID, wallet string) {
	t.Helper()
	if err := r.db.Exec("UPDATE users SET wallet_address = ? WHERE id = ?", wallet, user).Error; err != nil {
		t.Fatal(err)
	}
}

// Spec §5: a KYC-verified user is an LP at once — row active, wallet
// allow-listed by the operator — with no application.
func TestVaultLP_ActivateInstantlyAndAllowList(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	lp, err := NewPMService(r.db).GetByUserIDKind(context.Background(), user, models.PaymentMerchantKindLP)
	if err != nil || lp.Status != models.PaymentMerchantStatusActive || lp.OperatorWalletAddress != wallet {
		t.Fatalf("lp = %+v %v", lp, err)
	}
	if len(r.sender.calls) != 1 || !strings.HasPrefix(r.sender.calls[0], "setDepositor:"+wallet+":true") {
		t.Fatalf("calls = %v, want one allow-list write", r.sender.calls)
	}
	if _, err := r.svc.Activate(context.Background(), user, wallet); err != nil {
		t.Fatal(err)
	}
	if len(r.sender.calls) != 1 {
		t.Fatalf("calls = %v, an allowed wallet is never re-sent", r.sender.calls)
	}
}

// Without KYC, an active TSA Pay merchant still activates (spec §5); anyone
// else is refused.
func TestVaultLP_Eligibility(t *testing.T) {
	r := newLPRig(t)
	unverified := seedUserAt(t, r.db, uuid.New(), models.VerificationStatusPending)
	if _, err := r.svc.Activate(context.Background(), unverified, newLPWallet()); !errors.Is(err, ErrLPNotEligible) {
		t.Fatalf("err = %v, want ErrLPNotEligible", err)
	}
	if err := r.db.Create(&models.PayMerchant{ID: uuid.New(), MerchantRequestID: uuid.New(), UserID: unverified,
		BusinessName: "Shop", Status: models.PayMerchantActive, MinAmountKobo: 1, MaxAmountKobo: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Activate(context.Background(), unverified, newLPWallet()); err != nil {
		t.Fatalf("a TSA Pay merchant: %v", err)
	}
}

func TestVaultLP_CompanyAccountRefused(t *testing.T) {
	r := newLPRig(t)
	company := seedVerifiedUser(t, r.db)
	r.svc.c.CompanyUser = func(context.Context) (uuid.UUID, error) { return company, nil }
	if _, err := r.svc.Activate(context.Background(), company, newLPWallet()); !errors.Is(err, ErrCompanyUserCannotBeLP) {
		t.Fatalf("err = %v", err)
	}
}

// Deposit: allow-listed first, approval when the allowance is short, then the
// deposit; never past the per-LP maximum.
func TestVaultLP_PrepareDepositSteps(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	r.vault.allowed[strings.ToLower(wallet)] = false
	r.sender.refuse = errors.New("rpc down")
	if _, err := r.svc.PrepareDeposit(context.Background(), user, "10"); !errors.Is(err, ErrLPNotAllowedYet) {
		t.Fatalf("err = %v, want ErrLPNotAllowedYet", err)
	}
	r.sender.refuse = nil
	r.vault.allowed[strings.ToLower(wallet)] = true
	plan, err := r.svc.PrepareDeposit(context.Background(), user, "10")
	if err != nil || plan.Step != "approve" {
		t.Fatalf("plan = %+v %v, want approve", plan, err)
	}
	r.tokens.allowance = usdt18(10)
	if plan, err = r.svc.PrepareDeposit(context.Background(), user, "10"); err != nil || plan.Step != "deposit" {
		t.Fatalf("plan = %+v %v, want deposit", plan, err)
	}
	if _, err := r.svc.PrepareDeposit(context.Background(), user, "4"); !errors.Is(err, ErrLPAmount) {
		t.Fatalf("below min: %v", err)
	}
	r.vault.principal[strings.ToLower(wallet)] = usdt18(4_995)
	if _, err := r.svc.PrepareDeposit(context.Background(), user, "10"); !errors.Is(err, ErrLPAmount) {
		t.Fatalf("over max: %v", err)
	}
}

// The LP limits are the contract's (minDeposit / maxPerLp / totalCap), read
// fresh for every deposit: a lowered limit applies at once, the pool cap
// refuses before she signs, and an unreadable limit refuses rather than guess.
func TestVaultLP_PrepareDepositUsesTheVaultLimits(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	r.vault.allowed[strings.ToLower(wallet)] = true
	r.tokens.allowance = usdt18(1_000)
	ctx := context.Background()
	r.vault.limits = VaultLimits{MinDeposit: usdt18(1), MaxPerLP: usdt18(100), TotalCap: usdt18(1_000)}
	if plan, err := r.svc.PrepareDeposit(ctx, user, "1"); err != nil || plan.Step != "deposit" {
		t.Fatalf("deposit at the contract minimum = %+v %v", plan, err)
	}
	if _, err := r.svc.Me(ctx, user); err != nil { // caches the limits
		t.Fatal(err)
	}
	calls := r.vault.limitsCalls
	r.vault.limits.MaxPerLP = usdt18(50) // the owner lowers it
	if _, err := r.svc.PrepareDeposit(ctx, user, "60"); !errors.Is(err, ErrLPAmount) {
		t.Fatalf("over the lowered maxPerLp: %v, want ErrLPAmount", err)
	}
	if r.vault.limitsCalls != calls+1 {
		t.Fatalf("limits reads = %d, want a fresh read per deposit (%d)", r.vault.limitsCalls, calls+1)
	}
	r.vault.state.TotalPrincipal = usdt18(980)
	_, err := r.svc.PrepareDeposit(ctx, user, "30")
	if !errors.Is(err, ErrLPPoolFull) || !strings.Contains(err.Error(), "at most 20 USDT") {
		t.Fatalf("over totalCap: %v, want ErrLPPoolFull naming the 20 USDT left", err)
	}
	if plan, err := r.svc.PrepareDeposit(ctx, user, "20"); err != nil || plan.Step != "deposit" {
		t.Fatalf("exactly to the cap = %+v %v", plan, err)
	}
	r.vault.limitsErr = errors.New("rpc down")
	if _, err := r.svc.PrepareDeposit(ctx, user, "5"); !errors.Is(err, ErrLPLimitsUnavailable) {
		t.Fatalf("limits unreadable: %v, want ErrLPLimitsUnavailable", err)
	}
}

// /liquidity/me shows the contract's limits in the same {min, max} shape,
// cached lpLimitsTTL; a failed read leaves them empty and flagged, never a
// guess, and the page still renders.
func TestVaultLP_MeLimitsFromTheVaultCached(t *testing.T) {
	r := newLPRig(t)
	r.vault.limits = VaultLimits{MinDeposit: usdt18(1), MaxPerLP: usdt18(5_000), TotalCap: usdt18(50_000)}
	user, _ := r.activeLP(t)
	ctx := context.Background()
	me, err := r.svc.Me(ctx, user)
	if err != nil || !me.LimitsReadable || me.Limits.Min != "1" || me.Limits.Max != "5000" {
		t.Fatalf("me limits = %+v readable=%v %v", me.Limits, me.LimitsReadable, err)
	}
	b, _ := json.Marshal(me)
	if !strings.Contains(string(b), `"limits":{"min":"1","max":"5000"}`) {
		t.Fatalf("me JSON = %s, want limits {min, max}", b)
	}
	calls := r.vault.limitsCalls
	r.vault.limits.MinDeposit = usdt18(2)
	if me, _ = r.svc.Me(ctx, user); me.Limits.Min != "1" || r.vault.limitsCalls != calls {
		t.Fatalf("within the TTL: min %s after %d reads, want the cached 1 and no read", me.Limits.Min, r.vault.limitsCalls-calls)
	}
	r.now = r.now.Add(lpLimitsTTL)
	if me, _ = r.svc.Me(ctx, user); me.Limits.Min != "2" {
		t.Fatalf("past the TTL: min %s, want 2", me.Limits.Min)
	}
	r.vault.limitsErr = errors.New("rpc down")
	r.now = r.now.Add(lpLimitsTTL)
	me, err = r.svc.Me(ctx, user)
	if err != nil || me.LimitsReadable || me.Limits.Min != "" || me.Limits.Max != "" || me.Status != LiquidityStatusActive {
		t.Fatalf("unreadable limits: %+v readable=%v status=%s %v", me.Limits, me.LimitsReadable, me.Status, err)
	}
}

// The custody promise in the API: a paused LP can still withdraw and claim.
func TestVaultLP_PausedLPCanStillWithdrawAndClaim(t *testing.T) {
	r := newLPRig(t)
	user, _ := r.activeLP(t)
	r.db.Model(&models.PaymentMerchant{}).Where("user_id = ?", user).Update("status", models.PaymentMerchantStatusPaused)
	if _, err := r.svc.PrepareDeposit(context.Background(), user, "10"); !errors.Is(err, ErrLPPausedByAdmin) {
		t.Fatalf("deposit while paused: %v", err)
	}
	if plan, err := r.svc.PrepareWithdraw(context.Background(), user, "1"); err != nil || plan.Step != "withdraw" {
		t.Fatalf("withdraw while paused: %+v %v", plan, err)
	}
	if plan, err := r.svc.PrepareClaim(context.Background(), user, ""); err != nil || plan.Step != "claim" {
		t.Fatalf("claim while paused: %+v %v", plan, err)
	}
}

// Gas-free claim: $0.50 minimum, once per window, to the LP itself.
func TestVaultLP_ClaimForMe(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	r.vault.claimable[strings.ToLower(wallet)] = new(big.Int).Div(usdt18(1), big.NewInt(4)) // $0.25
	if _, err := r.svc.ClaimForMe(context.Background(), user); !errors.Is(err, ErrLPClaimTooSmall) {
		t.Fatalf("err = %v, want too small", err)
	}
	r.vault.claimable[strings.ToLower(wallet)] = usdt18(2)
	if _, err := r.svc.ClaimForMe(context.Background(), user); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if last := r.sender.calls[len(r.sender.calls)-1]; last != "claimFor:"+wallet {
		t.Fatalf("sent %q", last)
	}
	if _, err := r.svc.ClaimForMe(context.Background(), user); !errors.Is(err, ErrLPClaimRateLimit) {
		t.Fatalf("second claim err = %v, want rate limited", err)
	}
}

// A claim that was never sent gives the window back.
func TestVaultLP_ClaimForNotSentKeepsTheWindow(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	r.vault.claimable[strings.ToLower(wallet)] = usdt18(2)
	r.sender.refuse = errors.New("rpc down")
	if _, err := r.svc.ClaimForMe(context.Background(), user); err == nil {
		t.Fatal("want the send error")
	}
	r.sender.refuse = nil
	if _, err := r.svc.ClaimForMe(context.Background(), user); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// History replays the ledger as the contract accounts: each fill's spread by
// principal at that moment; a withdrawal's pending part leaves principal at
// once (it stops earning) and shows as withdrawal_pending, then
// withdrawal_paid when paid.
func TestVaultLP_ActivityReplaysEarnings(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	other := newLPWallet()
	at := func(m int) time.Time { return time.Date(2026, 10, 14, 10, m, 0, 0, time.UTC) }
	amt := func(n int64) *string { s := big.NewInt(n).String(); return &s }
	for i, e := range []models.LiquidityLedgerEntry{
		{Kind: VaultEventDeposited, Account: wallet, Amount: amt(10)},
		{Kind: VaultEventDeposited, Account: other, Amount: amt(30)},
		{Kind: VaultEventFilled, To: "0xtreasury", Amount: amt(100), Spread: amt(4)},
		{Kind: VaultEventClaimed, Account: wallet, To: wallet, Amount: amt(1)},
		{Kind: VaultEventWithdrawn, Account: wallet, Amount: amt(2)},                 // withdraw 5: 2 now …
		{Kind: VaultEventWithdrawalPending, Account: wallet, Amount: amt(3)},         // … 3 pending
		{Kind: VaultEventFilled, To: "0xtreasury", Amount: amt(100), Spread: amt(7)}, // 7 × 5/35 = 1
		{Kind: VaultEventWithdrawalPaid, Account: wallet, Amount: amt(3)},
		{Kind: VaultEventWithdrawalPending, Account: other, Amount: amt(9)}, // never shown
	} {
		e.ID, e.ChainID, e.Vault, e.TxHash, e.LogIndex, e.Block, e.BlockTime = uuid.New(), poolTestChain,
			strings.ToLower(testVaultAddr.Hex()), "0x"+strings.Repeat("0", 63)+string(rune('a'+i)), 0, uint64(100+i), at(i)
		if err := r.db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := r.svc.Activity(context.Background(), user, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, it := range page.Items {
		kinds = append(kinds, it.Kind+"="+it.Amount)
	}
	// 4 × 10/40 = 1 earned; after the withdrawal 7 × 5/35 = 1; the other LP's
	// rows are never shown.
	u := func(n int64) string { return BaseUnitsToDecimal(big.NewInt(n), 18) }
	want := []string{"withdrawal_paid=" + u(3), "earned=" + u(1), "withdrawal_pending=" + u(3), "withdrawal=" + u(2),
		"claim=" + u(1), "earned=" + u(1), "deposit=" + u(10)}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("activity = %v, want %v", kinds, want)
	}
}

// The allow-list follows the rows: a paused LP's deposits are revoked.
func TestVaultLP_SyncRevokesPausedLPs(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	r.db.Model(&models.PaymentMerchant{}).Where("user_id = ?", user).Update("status", models.PaymentMerchantStatusPaused)
	if _, err := r.svc.SyncDepositors(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.vault.allowed[strings.ToLower(wallet)] {
		t.Fatal("a paused LP is still allowed to deposit")
	}
}

func TestVaultLP_MeShowsPositionShareAndEarned(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	k := strings.ToLower(wallet)
	r.vault.principal[k], r.vault.claimable[k] = usdt18(10), usdt18(1)
	r.vault.state = VaultState{Balance: usdt18(200), TotalPendingWithdrawals: new(big.Int), Outstanding: usdt18(50), TotalPrincipal: usdt18(40)}
	r.now = r.now.Add(lpPoolViewTTL) // past the pool view cache
	me, err := r.svc.Me(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if me.Mode != "vault" || me.Status != LiquidityStatusActive || !me.Allowed || me.SharePercent != "25.00" ||
		me.Principal != "10" || me.Claimable != "1" || me.Earned != "1" || me.Pool == nil || me.Pool.TotalLiquidity != "40" ||
		me.PendingWithdrawal != "" {
		t.Fatalf("me = %+v pool = %+v", me, me.Pool)
	}
	b, _ := json.Marshal(me)
	if strings.Contains(string(b), "pendingWithdrawal") || strings.Contains(string(b), "collateral") || strings.Contains(string(b), "coverage") ||
		strings.Contains(string(b), "outstanding") {
		t.Fatalf("me JSON = %s, want no pendingWithdrawal while none, no collateral fields and no outstanding", b)
	}
	r.vault.pending[k] = usdt18(3)
	if me, err = r.svc.Me(context.Background(), user); err != nil || me.PendingWithdrawal != "3" {
		t.Fatalf("pending withdrawal = %q %v, want 3", me.PendingWithdrawal, err)
	}
}

// One vault position, one account: a wallet already backing another LP is
// refused, so no account reads or gas-free claims another's position.
func TestVaultLP_WalletBacksOneAccountOnly(t *testing.T) {
	r := newLPRig(t)
	_, wallet := r.activeLP(t)
	other := seedVerifiedUser(t, r.db)
	r.link(t, other, wallet)
	if _, err := r.svc.Activate(context.Background(), other, wallet); !errors.Is(err, ErrLPWalletInUse) {
		t.Fatalf("err = %v, want ErrLPWalletInUse", err)
	}
}

// The account re-linked another wallet: deposits and gas-free claims wait for
// a re-activation from it; withdraw and claim (signed by the LP wallet) don't.
func TestVaultLP_WalletChangedGatesDepositAndClaimFor(t *testing.T) {
	r := newLPRig(t)
	user, wallet := r.activeLP(t)
	r.vault.claimable[strings.ToLower(wallet)] = usdt18(2)
	r.link(t, user, newLPWallet())
	if _, err := r.svc.PrepareDeposit(context.Background(), user, "10"); !errors.Is(err, ErrLPWalletMismatch) {
		t.Fatalf("deposit err = %v", err)
	}
	if _, err := r.svc.ClaimForMe(context.Background(), user); !errors.Is(err, ErrLPWalletMismatch) {
		t.Fatalf("claim-for err = %v", err)
	}
	if _, err := r.svc.PrepareWithdraw(context.Background(), user, "1"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	me, _ := r.svc.Me(context.Background(), user)
	if !me.WalletChanged {
		t.Fatal("me does not say the wallet changed")
	}
}

// ─── Review fixes (2026-10-14, round 2) ─────────────────────────────────────

// indexDepositorSet records a DepositorSet the indexer would have written.
func (r *lpRig) indexDepositorSet(t *testing.T, wallet string, allowed bool, block uint64) {
	t.Helper()
	if err := r.db.Create(&models.LiquidityLedgerEntry{ID: uuid.New(), ChainID: poolTestChain, Vault: strings.ToLower(testVaultAddr.Hex()),
		TxHash: common.BytesToHash(uuid.New().NodeID()).Hex(), Block: block, Kind: VaultEventDepositorSet,
		Account: common.HexToAddress(wallet).Hex(), Allowed: &allowed, BlockTime: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
}

// The sync reports the setDepositor calls it sent, not the rows it walked.
func TestVaultLP_SyncCountsOnlyWrites(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	user, _ := r.activeLP(t)
	r.activeLP(t)
	if n, err := r.svc.SyncDepositors(ctx); err != nil || n != 0 {
		t.Fatalf("in-sync tick = %d %v, want 0 writes", n, err)
	}
	r.db.Model(&models.PaymentMerchant{}).Where("user_id = ?", user).Update("status", models.PaymentMerchantStatusPaused)
	if n, err := r.svc.SyncDepositors(ctx); err != nil || n != 1 {
		t.Fatalf("pause tick = %d %v, want 1 write", n, err)
	}
	if n, err := r.svc.SyncDepositors(ctx); err != nil || n != 0 {
		t.Fatalf("next tick = %d %v, want 0 writes", n, err)
	}
}

// A wallet whose last indexed DepositorSet already matches costs no RPC; with
// no event, or one that disagrees, the vault is read as before.
func TestVaultLP_SyncTrustsTheLedgerWhenItAgrees(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	user, wallet := r.activeLP(t)
	r.indexDepositorSet(t, wallet, false, 10)
	r.indexDepositorSet(t, strings.ToLower(wallet), true, 11) // the latest wins, any case
	calls := len(r.sender.calls)
	r.vault.positionErr = errors.New("rpc down")
	if n, err := r.svc.SyncDepositors(ctx); err != nil || n != 0 || len(r.sender.calls) != calls {
		t.Fatalf("ledger agrees: %d %v (calls %d→%d), want no RPC and no write", n, err, calls, len(r.sender.calls))
	}
	other, _ := r.activeLP(t) // positionErr: its own allow-list write is deferred
	if _, err := r.svc.SyncDepositors(ctx); err == nil {
		t.Fatal("a wallet with no indexed event was not read from the vault")
	}
	r.db.Exec("DELETE FROM payment_merchants WHERE user_id = ?", other)
	r.db.Model(&models.PaymentMerchant{}).Where("user_id = ?", user).Update("status", models.PaymentMerchantStatusPaused)
	if _, err := r.svc.SyncDepositors(ctx); err == nil {
		t.Fatal("a ledger that disagrees (paused, indexed allowed) was trusted")
	}
	r.vault.positionErr = nil
	if n, err := r.svc.SyncDepositors(ctx); err != nil || n != 1 || r.vault.allowed[strings.ToLower(wallet)] {
		t.Fatalf("revoke = %d %v, want the paused wallet revoked", n, err)
	}
}

// Review 2026-10-14: a revoke never trusts the ledger. An index that last
// saw "revoked" while the chain still allows the wallet (a stalled indexer,
// or an allow sent outside the server) must not leave it able to deposit.
func TestVaultLP_SyncRevokeIgnoresAStaleLedger(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	user, wallet := r.activeLP(t)
	r.indexDepositorSet(t, wallet, false, 10) // stale: says revoked
	r.vault.allowed[strings.ToLower(wallet)] = true
	r.db.Model(&models.PaymentMerchant{}).Where("user_id = ?", user).Update("status", models.PaymentMerchantStatusPaused)
	if n, err := r.svc.SyncDepositors(ctx); err != nil || n != 1 || r.vault.allowed[strings.ToLower(wallet)] {
		t.Fatalf("revoke over a stale ledger = %d %v allowed=%v, want the wallet revoked", n, err, r.vault.allowed[strings.ToLower(wallet)])
	}
}

// Owner 2026-10-14: a deposit is locked for 24 h. Me says what may leave now
// and when the rest unlocks; the contract's refusal is a 400 deposit_locked.
func TestVaultLP_MeShowsTheDepositLock(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	user, wallet := r.activeLP(t)
	k := strings.ToLower(wallet)
	r.vault.principal[k] = big.NewInt(100e6)
	until := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	r.vault.locked = map[string]*lpLock{k: {amount: big.NewInt(40e6), until: until}}
	me, err := r.svc.Me(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if me.LockedUntil == nil || !me.LockedUntil.Equal(until) {
		t.Fatalf("lockedUntil = %v, want %v", me.LockedUntil, until)
	}
	if want := r.svc.units(big.NewInt(60e6)); me.Withdrawable != want {
		t.Fatalf("withdrawable = %s, want %s", me.Withdrawable, want)
	}
	delete(r.vault.locked, k)
	if me, _ = r.svc.Me(ctx, user); me.Withdrawable != me.Principal || me.LockedUntil != nil {
		t.Fatalf("unlocked: withdrawable=%s lockedUntil=%v", me.Withdrawable, me.LockedUntil)
	}
	r.svc.c.Vault = revertingLPVault{fakeLPVault: r.vault, err: vaultRevert("Deposit still locked")}
	if _, err := r.svc.PrepareWithdraw(ctx, user, "1"); !errors.Is(err, ErrLPDepositLocked) {
		t.Fatalf("locked withdraw: %v, want ErrLPDepositLocked", err)
	}
}

// Testnet rehearsal 2026-10-14: a deposit while the vault is paused reverted
// with OZ's EnforcedPause() custom error, which surfaced as raw hex. It is
// decoded and answered as ErrLPPoolPaused.
func TestVaultLP_DepositWhilePausedIsReadable(t *testing.T) {
	sel := hexutil.MustDecode("0xd93c0665") // EnforcedPause()
	err := fmt.Errorf("estimate gas (would revert): %w", withRevertReason(revertDataErr{data: hexutil.Encode(sel)}))
	if got := lpRevertError(err); !errors.Is(got, ErrLPPoolPaused) {
		t.Fatalf("paused deposit = %v, want ErrLPPoolPaused", got)
	}
}

// revertingLPVault refuses withdraw and claim at estimate the way the
// contract does, or fails the RPC.
type revertingLPVault struct {
	*fakeLPVault
	err error
}

func (v revertingLPVault) PrepareWithdraw(string, *big.Int) ([]byte, error) { return nil, v.err }
func (v revertingLPVault) PrepareClaim(string) ([]byte, error)              { return nil, v.err }
func (v revertingLPVault) PrepareClaimTo(string, string) ([]byte, error)    { return nil, v.err }

// The vault's own refusals of an LP tx are the LP's 400s, not a 500: a
// withdraw over her principal ("Bad amount"), a claim with nothing earned
// ("Nothing to claim"). An RPC failure stays an error as before.
func TestVaultLP_WithdrawAndClaimRefusalsAreTheLPs(t *testing.T) {
	r := newLPRig(t)
	ctx := context.Background()
	user, _ := r.activeLP(t)
	set := func(err error) { r.svc.c.Vault = revertingLPVault{fakeLPVault: r.vault, err: err} }

	set(vaultRevert("Bad amount"))
	if _, err := r.svc.PrepareWithdraw(ctx, user, "1"); !errors.Is(err, ErrLPAmount) {
		t.Fatalf("withdraw over principal: %v, want ErrLPAmount", err)
	}
	set(vaultRevert("Insufficient liquidity"))
	if _, err := r.svc.PrepareClaim(ctx, user, ""); !errors.Is(err, ErrLPClaimUnavailable) {
		t.Fatalf("claim short of liquidity: %v, want ErrLPClaimUnavailable", err)
	}
	if _, err := r.svc.PrepareClaim(ctx, user, newLPWallet()); !errors.Is(err, ErrLPClaimUnavailable) {
		t.Fatalf("claimTo short of liquidity: %v, want ErrLPClaimUnavailable", err)
	}
	set(vaultRevert("Nothing to claim"))
	if _, err := r.svc.PrepareClaim(ctx, user, ""); !errors.Is(err, ErrLPNothingToClaim) {
		t.Fatalf("claim: %v, want ErrLPNothingToClaim", err)
	}
	if _, err := r.svc.PrepareClaim(ctx, user, newLPWallet()); !errors.Is(err, ErrLPNothingToClaim) {
		t.Fatalf("claimTo: %v, want ErrLPNothingToClaim", err)
	}
	set(errors.New("dial tcp: i/o timeout"))
	if _, err := r.svc.PrepareWithdraw(ctx, user, "1"); err == nil || errors.Is(err, ErrLPAmount) {
		t.Fatalf("rpc failure: %v, want the plain error", err)
	}
}
