package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
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
	allowed   map[string]bool
	state     VaultState
}

func newFakeLPVault() *fakeLPVault {
	return &fakeLPVault{principal: map[string]*big.Int{}, claimable: map[string]*big.Int{}, allowed: map[string]bool{},
		state: VaultState{Collateral: big.NewInt(0), Outstanding: big.NewInt(0), TotalPrincipal: big.NewInt(0)}}
}

func (v *fakeLPVault) Address() string { return testVaultAddr.Hex() }
func (v *fakeLPVault) State(context.Context) (*VaultState, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.state
	return &st, nil
}
func (v *fakeLPVault) Position(_ context.Context, lp string) (*VaultPosition, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	k := strings.ToLower(lp)
	p, c := v.principal[k], v.claimable[k]
	if p == nil {
		p = new(big.Int)
	}
	if c == nil {
		c = new(big.Int)
	}
	return &VaultPosition{Principal: new(big.Int).Set(p), Claimable: new(big.Int).Set(c), Allowed: v.allowed[k]}, nil
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
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil // LP limits $5 / $5,000
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
	if _, err := r.svc.Activate(context.Background(), user, wallet); err != nil {
		t.Fatalf("activate: %v", err)
	}
	return user, wallet
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
// principal at that moment.
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
	// 4 × 10/40 = 1 earned; the other LP's deposit is never shown.
	want := []string{"claim=" + BaseUnitsToDecimal(big.NewInt(1), 18), "earned=" + BaseUnitsToDecimal(big.NewInt(1), 18), "deposit=" + BaseUnitsToDecimal(big.NewInt(10), 18)}
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
	r.vault.state = VaultState{Collateral: usdt18(200), Outstanding: usdt18(50), TotalPrincipal: usdt18(40)}
	me, err := r.svc.Me(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if me.Mode != "vault" || me.Status != LiquidityStatusActive || !me.Allowed || me.SharePercent != "25.00" ||
		me.Principal != "10" || me.Claimable != "1" || me.Earned != "1" || me.Pool == nil || me.Pool.Outstanding != "50" {
		t.Fatalf("me = %+v pool = %+v", me, me.Pool)
	}
}
