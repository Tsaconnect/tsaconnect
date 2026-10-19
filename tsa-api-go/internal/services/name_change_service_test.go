package services

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ── rig ──────────────────────────────────────────────────────────────────

// nameChangeTables are the tables a name change touches, as raw DDL: users is
// written by an approval and read for the admin view, and the blocker tables
// need only the columns the blocker queries read.
var nameChangeTables = []string{
	`CREATE TABLE users (
		id TEXT PRIMARY KEY, name TEXT, username TEXT, email TEXT, wallet_address TEXT,
		verification_status TEXT DEFAULT 'pending', verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT,
		didit_session_id TEXT, created_at DATETIME, last_login DATETIME, updated_at DATETIME
	)`,
	`CREATE TABLE bill_payments (
		id TEXT PRIMARY KEY, user_id TEXT, order_id TEXT UNIQUE, purpose TEXT NOT NULL DEFAULT 'bill', wallet_address TEXT,
		mcgp_amount TEXT, ngn_kobo TEXT, rate_kobo TEXT, from_bought TEXT,
		debit_ref TEXT UNIQUE, status TEXT, tx_hash TEXT, resolve_tx_hash TEXT,
		failure_reason TEXT, created_at DATETIME, updated_at DATETIME
	)`,
	`CREATE TABLE verification_logs (
		id TEXT PRIMARY KEY, user_id TEXT, action TEXT, status TEXT, notes TEXT, metadata TEXT, admin_id TEXT,
		created_at DATETIME, updated_at DATETIME
	)`,
	`CREATE TABLE job_run_markers (name TEXT PRIMARY KEY, last_run_at DATETIME)`,
	`CREATE TABLE p2p_trades (id TEXT PRIMARY KEY, buyer_id TEXT, seller_id TEXT, status TEXT)`,
	`CREATE TABLE it_intents (id TEXT PRIMARY KEY, user_id TEXT, direction TEXT, status TEXT)`,
	`CREATE TABLE pay_merchants (id TEXT PRIMARY KEY, user_id TEXT)`,
	`CREATE TABLE pay_payments (id TEXT PRIMARY KEY, merchant_id TEXT, status TEXT, mode TEXT)`,
}

// fakeSwapPrice is the swap's price and balance reads, over the same fake the
// debit hold runs on.
type fakeSwapPrice struct {
	swap        *fakeBillSwap
	usdcPerMcgp *big.Int
}

func (f *fakeSwapPrice) Quote(kind uint8, mcgp *big.Int) (*big.Int, error) {
	if kind != PriceKindPremiumSell {
		return nil, errors.New("fee must be priced at PremiumSell")
	}
	return new(big.Int).Div(new(big.Int).Mul(f.usdcPerMcgp, mcgp), oneE18), nil
}
func (f *fakeSwapPrice) PremiumBalance(user string) (*big.Int, error) {
	return f.swap.PremiumBalance(user)
}

// fakeStableRail is the treasury: one USDC option on base, what the chain says
// about each named transaction, and a refund answer per call. Like the real
// verifier it credits only Transfer logs from the wallet asked about to the
// address asked about: anything else mined is "no match".
type fakeStableRail struct {
	payment *StablePayment // any transaction not in byHash
	byHash  map[string]fakeChainTx
	// creditFrom is whose tokens payment's Transfer logs move (default: its
	// signer) — set apart to model a transferFrom by an approved contract.
	creditFrom string
	verifyErr  error
	lastFrom   string

	refundHash      string
	refundAttempted bool
	refundErr       error
	refunds         int
	refundedAmount  *big.Int
	refundedTo      string

	// txUnknown: the chain has never heard of the named transaction;
	// txLookupErr: it cannot be asked; pendingFrom: transactions the chain
	// knows but has not mined, by signer.
	txUnknown   bool
	txLookupErr error
	pendingFrom map[string]string
}

// fakeChainTx is one mined transaction: what it proves, and whose tokens it
// moved to whom (defaults: its signer, to the treasury).
type fakeChainTx struct {
	pay      *StablePayment
	from, to string
}

const nameChangeTreasury = "0x000000000000000000000000000000000000fEE5"

func (f *fakeStableRail) mined(hash string) (fakeChainTx, bool) {
	if tx, ok := f.byHash[hash]; ok {
		return tx, true
	}
	if f.payment == nil {
		return fakeChainTx{}, false
	}
	return fakeChainTx{pay: f.payment, from: f.creditFrom}, true
}

func (f *fakeStableRail) Available() bool { return true }
func (f *fakeStableRail) To() string      { return nameChangeTreasury }
func (f *fakeStableRail) Options(context.Context) []StableFeeOption {
	return []StableFeeOption{{Chain: "base", ChainID: 8453, Token: "USDC", TokenAddress: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", Decimals: 6}}
}
func (f *fakeStableRail) PrepareTransfer(string, string, string, *big.Int) ([]byte, error) {
	return []byte(`{"to":"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913","data":"0xa9059cbb"}`), nil
}
func (f *fakeStableRail) VerifyPayment(_ context.Context, _, hash, _, from, to string) (*StablePayment, error) {
	f.lastFrom = from
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	tx, ok := f.mined(hash)
	if !ok {
		return nil, ErrTransferNotFound
	}
	logFrom, logTo := tx.from, tx.to
	if logFrom == "" {
		logFrom = tx.pay.Sender
	}
	if logTo == "" {
		logTo = nameChangeTreasury
	}
	if !strings.EqualFold(logFrom, from) || !strings.EqualFold(logTo, to) {
		nothing := *tx.pay
		nothing.Credited = new(big.Int)
		return &nothing, fmt.Errorf("%w: tx=%s", ErrTransferNoMatch, hash)
	}
	return tx.pay, nil
}
func (f *fakeStableRail) TransactionKnown(_ context.Context, _, hash string) (bool, string, error) {
	if f.txLookupErr != nil {
		return false, "", f.txLookupErr
	}
	if sender, ok := f.pendingFrom[hash]; ok {
		return true, sender, nil
	}
	if tx, ok := f.mined(hash); ok {
		return true, tx.pay.Sender, nil
	}
	return !f.txUnknown, "", nil
}
func (f *fakeStableRail) Refund(_ context.Context, _, _, to string, amount *big.Int, _ float64) (string, bool, error) {
	f.refunds++
	f.refundedAmount, f.refundedTo = amount, to
	return f.refundHash, f.refundAttempted, f.refundErr
}

type nameChangeRig struct {
	db     *gorm.DB
	svc    *NameChangeService
	swap   *fakeBillSwap
	rail   *fakeStableRail
	now    time.Time
	user   uuid.UUID
	key    *ecdsa.PrivateKey
	wallet string
}

func newNameChangeRig(t *testing.T, cfg NameChangeConfig) *nameChangeRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	for _, ddl := range nameChangeTables {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	if err := db.AutoMigrate(&models.UserNameChange{}); err != nil {
		t.Fatal(err)
	}
	// The same one-open-change index AutoMigrate builds in production.
	if err := db.Exec(`CREATE UNIQUE INDEX uq_user_name_changes_one_open ON user_name_changes (user_id)
		WHERE status NOT IN ('approved','rejected','lapsed','cancelled')`).Error; err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.GenerateKey()
	r := &nameChangeRig{db: db, swap: &fakeBillSwap{balance: mcgpBig(1000)}, rail: &fakeStableRail{},
		now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), key: key, wallet: crypto.PubkeyToAddress(key.PublicKey).Hex()}
	if cfg.FeeUSD == "" {
		cfg.FeeUSD = "1.00"
	}
	cfg.Enabled = true
	cfg.RequireFaceMatch = true
	r.svc = NewNameChangeService(db, cfg, nil, func() time.Time { return r.now })
	r.svc.SetMCGP(NewBillPaymentService(db, r.swap).ForPurpose(models.BillPayPurposeNameChange),
		&fakeSwapPrice{swap: r.swap, usdcPerMcgp: big.NewInt(250_000)}) // $0.25 a MCGP
	r.svc.SetStableRail(r.rail)
	r.user = r.seedUser(t, models.VerificationStatusVerified, "ADA CHIOMA OBI")
	return r
}

func (r *nameChangeRig) seedUser(t *testing.T, status, legal string) uuid.UUID {
	t.Helper()
	return r.seedUserWallet(t, status, legal, r.wallet)
}

// seedUserWallet is seedUser with a wallet of its own.
func (r *nameChangeRig) seedUserWallet(t *testing.T, status, legal, wallet string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := r.db.Exec(`INSERT INTO users (id, name, username, email, wallet_address, verification_status, verified_legal_name, didit_session_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, "Ada Obi", "ada_"+id.String()[:6], id.String()[:6]+"@x.ng", wallet, status, legal,
		"sess-first-kyc", r.now.AddDate(-1, 0, 0)).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// mcgpRequest pays from spendable MCGP, from the wallet the handler proved.
func (r *nameChangeRig) mcgpRequest(t *testing.T, _ uuid.UUID) NameChangeRequest {
	t.Helper()
	return NameChangeRequest{PayMethod: models.NameChangePayMCGP, ProvenWallet: r.wallet}
}

func (r *nameChangeRig) stableRequest(t *testing.T, _ uuid.UUID) NameChangeRequest {
	t.Helper()
	return NameChangeRequest{PayMethod: models.NameChangePayStablecoin, Chain: "base", Token: "USDC", ProvenWallet: r.wallet}
}

type nameChangeUserRow struct {
	Name              string
	VerifiedLegalName string
}

func (r *nameChangeRig) userRow(t *testing.T) nameChangeUserRow {
	t.Helper()
	var u nameChangeUserRow
	if err := r.db.Table("users").Select("name", "verified_legal_name").Where("id = ?", r.user).Scan(&u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func (r *nameChangeRig) change(t *testing.T, id uuid.UUID) *models.UserNameChange {
	t.Helper()
	ch, err := r.svc.get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

// payMCGP opens a change paid from spendable MCGP and checks it came back paid.
func (r *nameChangeRig) payMCGP(t *testing.T) *models.UserNameChange {
	t.Helper()
	out, err := r.svc.Create(context.Background(), r.user, r.mcgpRequest(t, r.user))
	if err != nil {
		t.Fatal(err)
	}
	if out.Change.Status != models.NameChangeStatusPaid || out.Change.FeeStatus != models.NameChangeFeeHeld {
		t.Fatalf("an MCGP fee must come back paid and held: %+v", out.Change)
	}
	return r.change(t, out.Change.ID)
}

// session starts the next attempt and records the Didit session bought for it.
func (r *nameChangeRig) session(t *testing.T, sessionID string) *models.UserNameChange {
	t.Helper()
	a, err := r.svc.BeginAttempt(context.Background(), r.user)
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	if err := r.svc.RecordSession(context.Background(), a.Change.ID, a.Claim, sessionID, "https://verify.didit.me/"+sessionID); err != nil {
		t.Fatal(err)
	}
	return r.change(t, a.Change.ID)
}

// sameAdult is the original document's identity: the person the account was
// verified as.
func sameAdult(name string) NameChangeIdentity {
	return NameChangeIdentity{Name: name, DateOfBirth: "1990-05-17", DocumentNumber: "A1234567", IssuingCountry: "NGA"}
}

// strongPass is a pass with every same-person signal: same date of birth and
// country as the original, and Didit's face search matching the original session.
func strongPass(ch *models.UserNameChange, name string) NameChangeDecision {
	fresh := sameAdult(name)
	fresh.DocumentNumber = "B7654321"
	fresh.FaceMatches = []NameChangeFaceMatch{{SessionID: "sess-first-kyc", Similarity: 97.5}}
	original := sameAdult("ADA CHIOMA OBI")
	return NameChangeDecision{SessionID: ch.DiditSessionID, Approved: true, New: fresh, Original: &original}
}

func (r *nameChangeRig) decide(t *testing.T, d NameChangeDecision, id uuid.UUID) *models.UserNameChange {
	t.Helper()
	out, err := r.svc.ApplyDecision(context.Background(), id, d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (r *nameChangeRig) decline(t *testing.T, ch *models.UserNameChange) *models.UserNameChange {
	t.Helper()
	return r.decide(t, NameChangeDecision{SessionID: ch.DiditSessionID}, ch.ID)
}

func (r *nameChangeRig) billPayment(t *testing.T, changeID uuid.UUID) models.BillPayment {
	t.Helper()
	var p models.BillPayment
	if err := r.db.Where("order_id = ?", changeID).First(&p).Error; err != nil {
		t.Fatal(err)
	}
	return p
}

// provenPayment is what the chain says of a full, deep, on-time USDC fee from
// the rig's wallet.
func (r *nameChangeRig) provenPayment(amount int64) *StablePayment {
	return &StablePayment{Credited: big.NewInt(amount), Sender: r.wallet, BlockTime: r.now.Add(time.Minute), Confirmations: 10}
}

var nameChangeTx = "0x" + strings.Repeat("ab", 32)

// ── starting, limits, flag ───────────────────────────────────────────────

func TestNameChange_FlagOffStartsNothingButFinishesWhatIsPaid(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	ch := r.payMCGP(t)
	r.svc.cfg.Enabled = false
	if _, err := r.svc.Create(context.Background(), r.user, r.mcgpRequest(t, r.user)); !errors.Is(err, ErrNameChangeDisabled) {
		t.Fatalf("err = %v", err)
	}
	ov, err := r.svc.Overview(context.Background(), r.user, "")
	if err != nil || ov.Enabled || ov.UnavailableReason != NameChangeUnavailableDisabled || ov.Quote != nil {
		t.Fatalf("overview %+v err %v", ov, err)
	}
	// A change already paid is still cancelled — and its hold returned.
	view, err := r.svc.Cancel(context.Background(), r.user)
	if err != nil || view.ID != ch.ID || view.FeeStatus != models.NameChangeFeeReversed {
		t.Fatalf("cancel while switched off: view %+v err %v", view, err)
	}
}

func TestNameChange_UnverifiedUserEditsTheirNameInstead(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	r.user = r.seedUser(t, models.VerificationStatusPending, "")
	if _, err := r.svc.Create(context.Background(), r.user, r.mcgpRequest(t, r.user)); !errors.Is(err, ErrNameChangeNotVerified) {
		t.Fatalf("err = %v", err)
	}
	ov, _ := r.svc.Overview(context.Background(), r.user, "")
	if ov.UnavailableReason != NameChangeUnavailableNotVerified || ov.Quote != nil {
		t.Fatalf("overview %+v", ov)
	}
	if _, err := r.svc.Overview(context.Background(), uuid.New(), ""); !errors.Is(err, ErrNameChangeUserNotFound) {
		t.Fatalf("a missing user: err = %v", err)
	}
}

func TestNameChange_OneOpenChangeAndAYearlyLimit(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{MaxPerYear: 2})
	ch := r.payMCGP(t)
	if _, err := r.svc.Create(context.Background(), r.user, r.mcgpRequest(t, r.user)); !errors.Is(err, ErrNameChangeOpen) {
		t.Fatalf("a second open change: err = %v", err)
	}
	if ov, _ := r.svc.Overview(context.Background(), r.user, ""); ov.Quote != nil {
		t.Fatal("no quote (no chain reads) while a change is open")
	}
	// The index refuses what a read-then-insert race would let through.
	dup := &models.UserNameChange{ID: uuid.New(), UserID: r.user, Status: models.NameChangeStatusPaying, FeeUSD: "1.00",
		PayMethod: models.NameChangePayMCGP, FeeStatus: models.NameChangeFeeNone, ExpiresAt: r.now}
	if err := r.svc.insert(context.Background(), dup); !errors.Is(err, ErrNameChangeOpen) {
		t.Fatalf("the one-open index: err = %v", err)
	}
	ch = r.session(t, "sess-1")
	r.decide(t, strongPass(ch, "ADA CHIOMA OBI"), ch.ID)

	eightMonths := r.now.AddDate(0, -8, 0)
	older := &models.UserNameChange{ID: uuid.New(), UserID: r.user, Status: models.NameChangeStatusApproved, FeeUSD: "1.00",
		PayMethod: models.NameChangePayFree, FeeStatus: models.NameChangeFeeNone, ExpiresAt: eightMonths, CompletedAt: &eightMonths,
		CreatedAt: eightMonths}
	if err := r.db.Create(older).Error; err != nil {
		t.Fatal(err)
	}
	_, err := r.svc.Create(context.Background(), r.user, r.mcgpRequest(t, r.user))
	var limit *NameChangeLimitError
	if !errors.As(err, &limit) || !limit.NextAllowedAt.Equal(eightMonths.AddDate(1, 0, 0)) || !strings.Contains(err.Error(), "2027-01-28") {
		t.Fatalf("err = %v (want the limit, next allowed when the older change ages out, as an ISO date)", err)
	}
	ov, _ := r.svc.Overview(context.Background(), r.user, "")
	if ov.Eligible || ov.NextAllowedAt == nil || ov.Limits.CompletedInLastYear != 2 || ov.Change == nil || ov.Change.ID != ch.ID {
		t.Fatalf("overview %+v (the latest change is still shown once closed)", ov)
	}
}

func TestNameChange_MoneyInFlightBlocksAChange(t *testing.T) {
	for name, seed := range map[string]func(r *nameChangeRig){
		"p2p_trade_open": func(r *nameChangeRig) {
			r.db.Exec(`INSERT INTO p2p_trades (id, buyer_id, seller_id, status) VALUES (?, ?, ?, ?)`, uuid.New(), uuid.New(), r.user, models.P2PTradeStatusPaid)
		},
		"it_sell_in_flight": func(r *nameChangeRig) {
			r.db.Exec(`INSERT INTO it_intents (id, user_id, direction, status) VALUES (?, ?, ?, ?)`, uuid.New(), r.user, models.ITDirectionSell, models.ITStatusDebited)
		},
		"it_buy_in_flight": func(r *nameChangeRig) {
			r.db.Exec(`INSERT INTO it_intents (id, user_id, direction, status) VALUES (?, ?, ?, ?)`, uuid.New(), r.user, models.ITDirectionBuy, models.ITStatusPendingPayment)
		},
		"pay_settlement_in_flight": func(r *nameChangeRig) {
			m := uuid.New()
			r.db.Exec(`INSERT INTO pay_merchants (id, user_id) VALUES (?, ?)`, m, r.user)
			r.db.Exec(`INSERT INTO pay_payments (id, merchant_id, status, mode) VALUES (?, ?, ?, ?)`, uuid.New(), m, models.PayStatusProcessing, models.PayModeLive)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newNameChangeRig(t, NameChangeConfig{})
			seed(r)
			_, err := r.svc.Create(context.Background(), r.user, r.mcgpRequest(t, r.user))
			var blocked *NameChangeBlockedError
			if !errors.As(err, &blocked) || len(blocked.Blockers) != 1 || blocked.Blockers[0].Code != name {
				t.Fatalf("err = %v", err)
			}
			if r.swap.debitCalls != 0 {
				t.Fatal("a blocked change took a fee")
			}
		})
	}
	// A trade where the user is only the buyer moves no money to them.
	r := newNameChangeRig(t, NameChangeConfig{})
	r.db.Exec(`INSERT INTO p2p_trades (id, buyer_id, seller_id, status) VALUES (?, ?, ?, ?)`, uuid.New(), r.user, uuid.New(), models.P2PTradeStatusPaid)
	if b, err := r.svc.Blockers(context.Background(), r.user); err != nil || len(b) != 0 {
		t.Fatalf("blockers %+v err %v", b, err)
	}
}

// ── the paying wallet ────────────────────────────────────────────────────

// The fee is only ever taken from the account's own linked wallet: a caller that
// names no proven wallet, or any other wallet, is refused before anything is
// written or debited.
func TestNameChange_FeeIsPaidOnlyFromTheAccountsLinkedWallet(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	for name, wallet := range map[string]string{
		"no proven wallet":  "",
		"another wallet":    "0x1111111111111111111111111111111111111111",
		"not an address":    "0xnope",
		"a lookalike (len)": r.wallet + "00",
	} {
		req := r.mcgpRequest(t, r.user)
		req.ProvenWallet = wallet
		if _, err := r.svc.Create(context.Background(), r.user, req); !errors.Is(err, ErrNameChangeWalletUnproven) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// The account's wallet in another letter case is the same wallet.
	req := r.stableRequest(t, r.user)
	req.ProvenWallet = strings.ToLower(r.wallet)
	out, err := r.svc.Create(context.Background(), r.user, req)
	if err != nil || out.Transfer == nil || out.Transfer.From != r.wallet {
		t.Fatalf("out %+v err %v (the transfer names the checksummed wallet)", out, err)
	}
	var n int64
	r.db.Model(&models.UserNameChange{}).Count(&n)
	if n != 1 || r.swap.debitCalls != 0 {
		t.Fatalf("a refused wallet took a fee or wrote a change: rows=%d debits=%d", n, r.swap.debitCalls)
	}
}

// ── fee: MCGP ────────────────────────────────────────────────────────────

func TestNameChange_MCGPHoldIsTakenOnTheProvenWalletAndSettledWhenApplied(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	ch := r.payMCGP(t)
	pay := r.billPayment(t, ch.ID)
	if pay.Purpose != models.BillPayPurposeNameChange || pay.McgpAmount != mcgpBig(4).String() || !strings.EqualFold(pay.WalletAddress, r.wallet) {
		t.Fatalf("$1 at $0.25 is 4 MCGP held on the proven wallet: %+v", pay)
	}
	if r.swap.lastRef == billDebitRef(ch.ID) {
		t.Fatal("a name-change debit must not share the bill ref namespace")
	}
	if got := r.userRow(t); got.VerifiedLegalName != "ADA CHIOMA OBI" || r.swap.settleCalls != 0 {
		t.Fatalf("nothing may change before a verification passes: %+v", got)
	}
	ch = r.session(t, "sess-1")
	ch = r.decide(t, strongPass(ch, "ADA  OBI"), ch.ID) // the middle name dropped
	if ch.Status != models.NameChangeStatusApproved || ch.MatchStrength != string(NameMatchPartial) ||
		ch.FeeStatus != models.NameChangeFeeSettled || ch.DOBMatch == nil || !*ch.DOBMatch || ch.FaceMatch == nil || !*ch.FaceMatch {
		t.Fatalf("change %+v", ch)
	}
	if got := r.userRow(t); got.Name != "ADA OBI" || got.VerifiedLegalName != "ADA OBI" {
		t.Fatalf("the document name must become both names: %+v", got)
	}
	var logs int64
	r.db.Model(&models.VerificationLog{}).Where("user_id = ? AND action = ?", r.user, models.VerificationActionNameChanged).Count(&logs)
	if logs != 1 || r.swap.settleCalls != 1 {
		t.Fatalf("logs=%d settles=%d", logs, r.swap.settleCalls)
	}
	r.decide(t, strongPass(ch, "ADA OBI"), ch.ID) // Didit redelivers
	if r.swap.settleCalls != 1 {
		t.Fatal("a redelivered approval settled the fee again")
	}
	if !r.svc.NameSetByChange(context.Background(), r.user) {
		t.Fatal("the account's name now comes from a change")
	}
}

func TestNameChange_CancelBeforeVerificationReversesTheHoldOncePerWindow(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{MaxRefundedCancels: 1})
	ch := r.payMCGP(t)
	view, err := r.svc.Cancel(context.Background(), r.user)
	if err != nil || view.Status != models.NameChangeStatusCancelled || view.FeeStatus != models.NameChangeFeeReversed || view.Message == "" {
		t.Fatalf("view %+v err %v", view, err)
	}
	if p := r.billPayment(t, ch.ID); p.Status != models.BillPayStatusReversed {
		t.Fatalf("payment %+v", p)
	}
	r.payMCGP(t)
	_, err = r.svc.Cancel(context.Background(), r.user)
	var limit *NameChangeLimitError
	if !errors.As(err, &limit) || !limit.Cancel {
		t.Fatalf("a second refunded cancel in the window: err = %v", err)
	}
	if r.swap.reverseCalls != 1 {
		t.Fatalf("reverses = %d", r.swap.reverseCalls)
	}
}

func TestNameChange_UncertainDebitIsFinishedByTheSweep(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	r.swap.verifyErr = errors.New("receipt timeout")
	out, err := r.svc.Create(context.Background(), r.user, r.mcgpRequest(t, r.user))
	if err != nil || out.Change.Status != models.NameChangeStatusPaying {
		t.Fatalf("out %+v err %v", out, err)
	}
	if _, err := r.svc.Cancel(context.Background(), r.user); !errors.Is(err, ErrNameChangePaymentPending) {
		t.Fatalf("cancel while the debit is unresolved: err = %v", err)
	}
	r.swap.verifyErr = nil
	if n, err := r.svc.Sweep(context.Background()); err != nil || n != 1 {
		t.Fatalf("sweep moved %d err %v", n, err)
	}
	if got := r.change(t, out.Change.ID); got.Status != models.NameChangeStatusPaid || got.FeeStatus != models.NameChangeFeeHeld {
		t.Fatalf("change %+v", got)
	}
}

func TestNameChange_PayMethodsUnavailableOrShort(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	r.swap.balance = mcgpBig(3) // the fee is 4
	if _, err := r.svc.Create(context.Background(), r.user, r.mcgpRequest(t, r.user)); !errors.Is(err, ErrNameChangeInsufficient) {
		t.Fatalf("short balance: err = %v", err)
	}
	var n int64
	r.db.Model(&models.UserNameChange{}).Count(&n)
	if n != 0 || r.swap.debitCalls != 0 {
		t.Fatalf("a short balance must write and debit nothing: rows=%d debits=%d", n, r.swap.debitCalls)
	}
	r.swap.noOperator = true
	if _, err := r.svc.Create(context.Background(), r.user, r.mcgpRequest(t, r.user)); !errors.Is(err, ErrNameChangeMethodUnavailable) {
		t.Fatalf("no operator: err = %v", err)
	}
	r.svc.SetStableRail(nil)
	if _, err := r.svc.Create(context.Background(), r.user, r.stableRequest(t, r.user)); !errors.Is(err, ErrNameChangeMethodUnavailable) {
		t.Fatalf("no treasury: err = %v", err)
	}
	ov, err := r.svc.Overview(context.Background(), r.user, r.wallet)
	if err != nil || ov.Quote == nil || len(ov.Quote.Methods) != 2 {
		t.Fatalf("overview %+v err %v", ov, err)
	}
	if m := ov.Quote.Methods[0].(NameChangeMCGPQuote); m.Available {
		t.Fatalf("mcgp offered with no operator: %+v", m)
	}
	if m := ov.Quote.Methods[1].(NameChangeStableQuote); m.Available || m.Options == nil {
		t.Fatalf("stablecoin offered with no treasury (options must still be a list): %+v", m)
	}
}

// ── fee: stablecoin ──────────────────────────────────────────────────────

func (r *nameChangeRig) openStable(t *testing.T) *NameChangeCreated {
	t.Helper()
	out, err := r.svc.Create(context.Background(), r.user, r.stableRequest(t, r.user))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNameChange_StablecoinFeeIsHeldToSenderTimeDepthAndAmount(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{MinConfirmations: 3})
	out := r.openStable(t)
	if out.Change.Status != models.NameChangeStatusPaying || out.Transfer == nil || out.Transfer.To != nameChangeTreasury ||
		out.Transfer.From != r.wallet || out.Transfer.AmountBaseUnits != "1000000" || out.Transfer.Amount != "1.00" || len(out.Transfer.UnsignedTx) == 0 {
		t.Fatalf("created %+v transfer %+v", out.Change, out.Transfer)
	}
	ctx := context.Background()

	// Not mined yet: told apart from a refusal, and the hash is kept.
	if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); !errors.Is(err, ErrNameChangePaymentOnChain) {
		t.Fatalf("not mined: err = %v", err)
	}
	if ch := r.change(t, out.Change.ID); ch.PayRef == nil || *ch.PayRef != nameChangeTx {
		t.Fatalf("the named transaction must be kept: %+v", ch)
	}
	// Mined but shallow: still pending.
	p := r.provenPayment(1_000_000)
	p.Confirmations = 1
	r.rail.payment = p
	if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); !errors.Is(err, ErrNameChangePaymentOnChain) {
		t.Fatalf("shallow: err = %v", err)
	}
	// Signed by another wallet: refused, and the hash forgotten so another can
	// be named — and remembered, so naming it again is answered at once.
	p = r.provenPayment(1_000_000)
	p.Sender = "0x1111111111111111111111111111111111111111"
	r.rail.payment = p
	if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); !errors.Is(err, ErrNameChangePaymentMismatch) {
		t.Fatalf("another sender: err = %v", err)
	}
	if ch := r.change(t, out.Change.ID); ch.PayRef != nil {
		t.Fatal("a refuted transaction must be cleared")
	}
	r.rail.payment = r.provenPayment(1_000_000)
	if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); !errors.Is(err, ErrNameChangePaymentMismatch) ||
		!strings.Contains(err.Error(), "already checked") {
		t.Fatalf("a refuted hash named again: err = %v", err)
	}
	// Moved out of the proven wallet by a contract it approved, not sent by
	// it: refused.
	moved := "0x" + strings.Repeat("a1", 32)
	p = r.provenPayment(1_000_000)
	p.Sender = "0x1111111111111111111111111111111111111111"
	r.rail.payment, r.rail.creditFrom = p, r.wallet
	if _, err := r.svc.ConfirmPayment(ctx, r.user, moved); !errors.Is(err, ErrNameChangePaymentMismatch) ||
		!strings.Contains(err.Error(), "other than the one proven") {
		t.Fatalf("a transferFrom by another signer: err = %v", err)
	}
	r.rail.creditFrom = ""
	// Made before the change was opened: refused.
	early := "0x" + strings.Repeat("a2", 32)
	p = r.provenPayment(1_000_000)
	p.BlockTime = r.now.Add(-time.Hour)
	r.rail.payment = p
	if _, err := r.svc.ConfirmPayment(ctx, r.user, early); !errors.Is(err, ErrNameChangePaymentMismatch) {
		t.Fatalf("an old transfer: err = %v", err)
	}
	// Proven.
	good := "0x" + strings.Repeat("a3", 32)
	r.rail.payment = r.provenPayment(1_000_000)
	view, err := r.svc.ConfirmPayment(ctx, r.user, good)
	if err != nil || view.Status != models.NameChangeStatusPaid || view.FeeStatus != models.NameChangeFeeReceived {
		t.Fatalf("view %+v err %v", view, err)
	}
	if !strings.EqualFold(r.rail.lastFrom, r.wallet) {
		t.Fatalf("proven from %q, want the proven wallet", r.rail.lastFrom)
	}
	// One transaction never pays two changes.
	other := r.seedUser(t, models.VerificationStatusVerified, "EMEKA NWOSU")
	if _, err := r.svc.Create(ctx, other, r.stableRequest(t, other)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.ConfirmPayment(ctx, other, good); !errors.Is(err, ErrNameChangePaymentMismatch) {
		t.Fatalf("a reused transaction: err = %v", err)
	}
	// Cancelled before verification: refunded to the proven wallet.
	r.rail.refundHash, r.rail.refundAttempted = "0xrefund", true
	view, err = r.svc.Cancel(ctx, r.user)
	if err != nil || view.Status != models.NameChangeStatusCancelled || view.FeeStatus != models.NameChangeFeeRefunded ||
		!strings.EqualFold(r.rail.refundedTo, r.wallet) {
		t.Fatalf("view %+v err %v to %s", view, err, r.rail.refundedTo)
	}
}

// Short money is not stranded: what arrived is recorded and refunded — the
// amount credited, not the quote.
func TestNameChange_UnderpaidStablecoinIsRefundedAsCredited(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	out := r.openStable(t)
	r.rail.payment = r.provenPayment(600_000)
	r.rail.refundHash, r.rail.refundAttempted = "0xback", true
	if _, err := r.svc.ConfirmPayment(context.Background(), r.user, nameChangeTx); err != nil {
		t.Fatalf("an underpayment is recorded and refunded, not refused: %v", err)
	}
	ch := r.change(t, out.Change.ID)
	if ch.Status != models.NameChangeStatusCancelled || ch.FeeStatus != models.NameChangeFeeRefunded || ch.PayCredited != "600000" ||
		r.rail.refundedAmount.String() != "600000" || ch.RefundAmount != "600000" || ch.UserMessage == "" {
		t.Fatalf("change %+v refunded %v", ch, r.rail.refundedAmount)
	}
}

// R1: a payment named while pending and proven after the change was closed —
// or confirmed only after the sweep closed it — is recorded and refunded, never
// answered with a mismatch. Its refund counts toward the user's allowance (L1).
func TestNameChange_LateStablecoinPaymentIsRecordedAndRefunded(t *testing.T) {
	ctx := context.Background()
	t.Run("confirmed after the sweep closed the change", func(t *testing.T) {
		r := newNameChangeRig(t, NameChangeConfig{MaxRefundedCancels: 1})
		out := r.openStable(t)
		r.now = r.now.Add(8 * 24 * time.Hour)
		if _, err := r.svc.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if ch := r.change(t, out.Change.ID); ch.Status != models.NameChangeStatusCancelled {
			t.Fatalf("an unpaid change out of time closes: %+v", ch)
		}
		p := r.provenPayment(1_000_000)
		p.BlockTime = r.now
		r.rail.payment, r.rail.refundHash, r.rail.refundAttempted = p, "0xlate", true
		view, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx)
		if err != nil || view.Status != models.NameChangeStatusCancelled || view.FeeStatus != models.NameChangeFeeRefunded {
			t.Fatalf("view %+v err %v", view, err)
		}
		if next, _ := r.svc.refundLimit(ctx, r.user); next == nil {
			t.Fatal("a late payment's refund counts toward the refund allowance")
		}
	})
	t.Run("named while pending, proven by the sweep", func(t *testing.T) {
		r := newNameChangeRig(t, NameChangeConfig{})
		out := r.openStable(t)
		if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); !errors.Is(err, ErrNameChangePaymentOnChain) {
			t.Fatalf("err = %v", err)
		}
		if _, err := r.svc.Cancel(ctx, r.user); !errors.Is(err, ErrNameChangePaymentPending) {
			t.Fatalf("cancelling with a payment on its way: err = %v", err)
		}
		r.rail.payment = r.provenPayment(1_000_000)
		if _, err := r.svc.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if ch := r.change(t, out.Change.ID); ch.Status != models.NameChangeStatusPaid || ch.FeeStatus != models.NameChangeFeeReceived {
			t.Fatalf("change %+v", ch)
		}
	})
}

// A refund that provably never left is retried by the sweep; one that may have
// left is never retried — it waits for a super admin to record what the chain shows.
func TestNameChange_StablecoinRefundRetriesOnlyWhatNeverLeft(t *testing.T) {
	ctx := context.Background()
	paid := func(t *testing.T) *nameChangeRig {
		r := newNameChangeRig(t, NameChangeConfig{})
		r.openStable(t)
		r.rail.payment = r.provenPayment(1_000_000)
		if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); err != nil {
			t.Fatal(err)
		}
		return r
	}

	r := paid(t)
	r.rail.refundErr = ErrTreasuryKilled
	view, _ := r.svc.Cancel(ctx, r.user)
	if view.FeeStatus != models.NameChangeFeeRefundFailed {
		t.Fatalf("an unsent refund: %+v", view)
	}
	r.rail.refundErr, r.rail.refundHash, r.rail.refundAttempted = nil, "0xretry", true
	r.now = r.now.Add(nameChangeRefundRetryBase) // its backoff
	if _, err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.change(t, view.ID); got.FeeStatus != models.NameChangeFeeRefunded || got.FeeTxHash != "0xretry" || r.rail.refunds != 2 {
		t.Fatalf("change %+v refunds=%d", got, r.rail.refunds)
	}

	u := paid(t)
	u.rail.refundErr, u.rail.refundAttempted, u.rail.refundHash = errors.New("rpc timeout"), true, "0xmaybe"
	view, _ = u.svc.Cancel(ctx, u.user)
	if view.FeeStatus != models.NameChangeFeeRefundUncertain {
		t.Fatalf("an uncertain refund: %+v", view)
	}
	if _, err := u.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if u.rail.refunds != 1 {
		t.Fatalf("an uncertain refund was sent again (%d sends)", u.rail.refunds)
	}
	if _, err := u.svc.AdminRecordRefundOutcome(ctx, uuid.New(), view.ID, NameChangeRefundOutcome{Sent: true}); !errors.Is(err, ErrNameChangeRefundSentOnly) {
		t.Fatalf("a sent refund needs its tx: err = %v", err)
	}
	if _, err := u.svc.AdminRecordRefundOutcome(ctx, uuid.New(), view.ID, NameChangeRefundOutcome{Sent: true, TxHash: "0xshort"}); !errors.Is(err, ErrNameChangeInvalid) {
		t.Fatalf("a malformed tx: err = %v", err)
	}
	refundTx := "0x" + strings.Repeat("ef", 32)
	u.rail.byHash = map[string]fakeChainTx{refundTx: {pay: &StablePayment{Credited: big.NewInt(1_000_000), Sender: nameChangeTreasury,
		BlockTime: u.now, Confirmations: 2}, from: nameChangeTreasury, to: u.wallet}}
	av, err := u.svc.AdminRecordRefundOutcome(ctx, uuid.New(), view.ID, NameChangeRefundOutcome{Sent: true, TxHash: refundTx, Note: "seen on basescan"})
	if err != nil || av.FeeStatus != models.NameChangeFeeRefunded || av.FeeTxHash != refundTx {
		t.Fatalf("view %+v err %v", av, err)
	}
}

// ── verification and the same-person gate ────────────────────────────────

// Anything short of strong evidence waits for an admin, with the account and
// the fee untouched and the reason recorded.
func TestNameChange_WeakEvidenceIsHeldForAnAdmin(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(r *nameChangeRig, d *NameChangeDecision)
		reason string
	}{
		"a different surname":        {func(r *nameChangeRig, d *NameChangeDecision) { d.New.Name = "ADA CHIOMA NWOSU" }, "current verified name"},
		"a different date of birth":  {func(r *nameChangeRig, d *NameChangeDecision) { d.New.DateOfBirth = "1991-05-17" }, "date of birth differs"},
		"no date of birth":           {func(r *nameChangeRig, d *NameChangeDecision) { d.New.DateOfBirth = "" }, "date of birth is missing"},
		"the original session gone":  {func(r *nameChangeRig, d *NameChangeDecision) { d.Original = nil }, "original KYC session could not be read"},
		"no face match":              {func(r *nameChangeRig, d *NameChangeDecision) { d.New.FaceMatches = nil }, "no face match"},
		"another country's document": {func(r *nameChangeRig, d *NameChangeDecision) { d.New.IssuingCountry = "GHA" }, "different country"},
		"no document country":        {func(r *nameChangeRig, d *NameChangeDecision) { d.New.IssuingCountry = "" }, "issuing country is missing"},
		"no original country":        {func(r *nameChangeRig, d *NameChangeDecision) { d.Original.IssuingCountry = " " }, "issuing country is missing"},
		// A partial step from the current name that is no longer close to
		// the ORIGINAL KYC name: the chaining the second comparison stops.
		"a chained partial step": {func(r *nameChangeRig, d *NameChangeDecision) {
			r.db.Model(&models.UserNameChange{}).Where("user_id = ?", r.user).Update("old_verified_name", "ADA OBI NWOSU")
			d.New.Name = "OBI NWOSU"
		}, "original KYC name"},
		"money in flight when it passed": {func(r *nameChangeRig, d *NameChangeDecision) {
			r.db.Exec(`INSERT INTO p2p_trades (id, buyer_id, seller_id, status) VALUES (?, ?, ?, ?)`, uuid.New(), uuid.New(), r.user, models.P2PTradeStatusPaid)
		}, "money in flight"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newNameChangeRig(t, NameChangeConfig{})
			r.payMCGP(t)
			ch := r.session(t, "sess-1")
			d := strongPass(ch, "ADA OBI")
			tc.mutate(r, &d)
			ch = r.decide(t, d, ch.ID)
			if ch.Status != models.NameChangeStatusHeldForReview || !strings.Contains(ch.HoldReason, tc.reason) {
				t.Fatalf("change status %s reason %q", ch.Status, ch.HoldReason)
			}
			if got := r.userRow(t); got.VerifiedLegalName != "ADA CHIOMA OBI" || r.swap.settleCalls != 0 {
				t.Fatalf("a held change must leave the account and the fee alone: %+v", got)
			}
		})
	}
}

func TestNameChange_HeldChangeIsApprovedByAnotherAdmin(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	r.payMCGP(t)
	ch := r.session(t, "sess-1")
	d := strongPass(ch, "ADA CHIOMA NWOSU")
	d.New.DateOfBirth = "1990-05-17"
	ch = r.decide(t, d, ch.ID)
	if ch.Status != models.NameChangeStatusHeldForReview {
		t.Fatalf("change %+v", ch)
	}
	if _, err := r.svc.AdminApprove(context.Background(), r.user, ch.ID, ""); !errors.Is(err, ErrNameChangeSelfReview) {
		t.Fatalf("self review: err = %v", err)
	}
	admin := uuid.New()
	view, err := r.svc.AdminApprove(context.Background(), admin, ch.ID, "marriage certificate seen")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != models.NameChangeStatusApproved || view.ReviewedBy == nil || *view.ReviewedBy != admin || view.ReviewedAt == nil ||
		view.OldVerifiedName != "ADA CHIOMA OBI" || view.User.ID != r.user || view.User.AccountAgeDays < 360 ||
		view.OriginalDiditSessionID != "sess-first-kyc" || view.DOBMatch == nil || !*view.DOBMatch || view.OriginalDOB != "" || view.NewDOB != "" ||
		view.DocumentCountryMatch == nil || !*view.DocumentCountryMatch || view.ReviewNote != "marriage certificate seen" ||
		strings.Contains(view.Message, "marriage") {
		t.Fatalf("admin view %+v", view)
	}
	if got := r.userRow(t); got.Name != "ADA CHIOMA NWOSU" || r.swap.settleCalls != 1 {
		t.Fatalf("approval must apply the held name and settle: %+v", got)
	}
	if _, err := r.svc.AdminApprove(context.Background(), admin, ch.ID, ""); !errors.Is(err, ErrNameChangeBadState) {
		t.Fatalf("a second approval: err = %v", err)
	}
}

func TestNameChange_AdminApproveWaitsForMoneyInFlight(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	r.payMCGP(t)
	ch := r.session(t, "sess-1")
	d := strongPass(ch, "ADA OBI")
	d.New.FaceMatches = nil
	ch = r.decide(t, d, ch.ID)
	r.db.Exec(`INSERT INTO it_intents (id, user_id, direction, status) VALUES (?, ?, ?, ?)`, uuid.New(), r.user, models.ITDirectionSell, models.ITStatusDebited)
	var blocked *NameChangeBlockedError
	if _, err := r.svc.AdminApprove(context.Background(), uuid.New(), ch.ID, ""); !errors.As(err, &blocked) {
		t.Fatalf("err = %v", err)
	}
}

func TestNameChange_AdminRejectKeepsTheNameAndTheFee(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	r.payMCGP(t)
	ch := r.session(t, "sess-1")
	ch = r.decide(t, strongPass(ch, "EMEKA NWOSU"), ch.ID)
	view, err := r.svc.AdminReject(context.Background(), uuid.New(), ch.ID, "different person")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != models.NameChangeStatusRejected || view.FeeStatus != models.NameChangeFeeSettled ||
		view.ReviewNote != "different person" || strings.Contains(view.Message, "different person") {
		t.Fatalf("view %+v (the admin's note is internal; the user gets their own message)", view)
	}
	if got := r.userRow(t); got.VerifiedLegalName != "ADA CHIOMA OBI" {
		t.Fatalf("a rejected name must never reach the account: %+v", got)
	}
}

// ── attempts, sessions, time ─────────────────────────────────────────────

func TestNameChange_AttemptsRunOutAndTheChangeLapsesKeepingTheFee(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{MaxAttempts: 2})
	r.payMCGP(t)
	ch := r.decline(t, r.session(t, "sess-1"))
	if ch.Status != models.NameChangeStatusDeclined || ch.Attempts != 1 || !strings.Contains(ch.UserMessage, "1 attempt") {
		t.Fatalf("change %+v", ch)
	}
	if _, err := r.svc.Cancel(context.Background(), r.user); !errors.Is(err, ErrNameChangeCancelNotAllowed) {
		t.Fatalf("cancel after an attempt: err = %v", err)
	}
	ch = r.decline(t, r.session(t, "sess-2"))
	if ch.Status != models.NameChangeStatusLapsed || ch.FeeStatus != models.NameChangeFeeSettled || r.swap.settleCalls != 1 {
		t.Fatalf("change %+v settles=%d", ch, r.swap.settleCalls)
	}
}

// Only one request can buy a Didit session for an attempt; a failed purchase
// gives the attempt back; an open session is resumed, never bought again.
func TestNameChange_SessionPurchaseIsClaimedOnce(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	ch := r.payMCGP(t)
	ctx := context.Background()
	first, err := r.svc.BeginAttempt(ctx, r.user)
	if err != nil || first.Resume || first.Claim == "" || !first.Counted || first.Change.Attempts != 1 {
		t.Fatalf("attempt %+v err %v", first, err)
	}
	if _, err := r.svc.BeginAttempt(ctx, r.user); !errors.Is(err, ErrNameChangeSessionPending) {
		t.Fatalf("a second tap while the first is buying: err = %v", err)
	}
	r.svc.AbandonAttempt(ctx, first) // Didit refused
	if got := r.change(t, ch.ID); got.Status != models.NameChangeStatusPaid || got.Attempts != 0 || got.DiditSessionID != "" {
		t.Fatalf("change %+v", got)
	}
	r.session(t, "sess-1")
	again, err := r.svc.BeginAttempt(ctx, r.user)
	if err != nil || !again.Resume || again.Change.Attempts != 1 {
		t.Fatalf("resume %+v err %v", again, err)
	}
	if found, _ := r.svc.FindBySession(ctx, first.Claim); found != nil {
		t.Fatal("a purchase claim is not a session")
	}
}

func TestNameChange_SessionsAreCappedPerMonthWhateverTheFee(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{FeeUSD: "0", MaxAttempts: 5, MaxSessionsPer30Days: 2})
	if _, err := r.svc.Create(context.Background(), r.user, NameChangeRequest{}); err != nil {
		t.Fatal(err)
	}
	r.decline(t, r.session(t, "sess-1"))
	r.decline(t, r.session(t, "sess-2"))
	if _, err := r.svc.BeginAttempt(context.Background(), r.user); !errors.Is(err, ErrNameChangeSessionLimit) {
		t.Fatalf("a third session in the window: err = %v", err)
	}
}

func TestNameChange_TimeRunsOutUnusedFeeReturnedUsedFeeKept(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{TTL: 7 * 24 * time.Hour})
	unused := r.payMCGP(t)
	other := newNameChangeRig(t, NameChangeConfig{TTL: 7 * 24 * time.Hour})
	other.payMCGP(t)
	used := other.decline(t, other.session(t, "sess-1"))
	for _, rig := range []*nameChangeRig{r, other} {
		rig.now = rig.now.Add(8 * 24 * time.Hour)
		if n, err := rig.svc.Sweep(context.Background()); err != nil || n != 1 {
			t.Fatalf("sweep moved %d err %v", n, err)
		}
	}
	if got := r.change(t, unused.ID); got.Status != models.NameChangeStatusLapsed || got.FeeStatus != models.NameChangeFeeReversed {
		t.Fatalf("an unused change's hold must be reversed: %+v", got)
	}
	if got := other.change(t, used.ID); got.Status != models.NameChangeStatusLapsed || got.FeeStatus != models.NameChangeFeeSettled {
		t.Fatalf("an attempted change's fee is kept: %+v", got)
	}
	if n, err := r.svc.Sweep(context.Background()); err != nil || n != 0 || r.swap.reverseCalls != 1 {
		t.Fatalf("second sweep moved %d err %v reverses=%d", n, err, r.swap.reverseCalls)
	}
	// Past the refund allowance, an unused change that lapses keeps its fee.
	r.now = r.now.Add(time.Hour)
	again := r.payMCGP(t)
	r.now = r.now.Add(8 * 24 * time.Hour)
	if _, err := r.svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.change(t, again.ID); got.Status != models.NameChangeStatusLapsed || got.FeeStatus != models.NameChangeFeeSettled {
		t.Fatalf("a second unused lapse in the window keeps the fee: %+v", got)
	}
}

func TestNameChange_ADecisionForAnotherSessionChangesNothing(t *testing.T) {
	r := newNameChangeRig(t, NameChangeConfig{})
	r.payMCGP(t)
	ch := r.session(t, "sess-1")
	d := strongPass(ch, "ADA OBI")
	d.SessionID = "sess-other"
	if got := r.decide(t, d, ch.ID); got.Status != models.NameChangeStatusVerifying {
		t.Fatalf("change %+v", got)
	}
	// A pass that came with no readable name cannot change a name: an attempt used.
	d = strongPass(ch, "")
	if got := r.decide(t, d, ch.ID); got.Status != models.NameChangeStatusDeclined {
		t.Fatalf("change %+v", got)
	}
}

func TestParseFeeUSDMicrosAndAmounts(t *testing.T) {
	if m, err := ParseFeeUSDMicros("1.00"); err != nil || m.Int64() != 1_000_000 {
		t.Fatalf("%v %v", m, err)
	}
	for _, bad := range []string{"", "-1", "1.0000001", "abc"} {
		if _, err := ParseFeeUSDMicros(bad); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
	if got := stableForUSDMicros(big.NewInt(1_000_000), 18).String(); got != "1000000000000000000" {
		t.Fatalf("18dp stablecoin: %s", got)
	}
	// Rounded up: a fee is never under-paid.
	if got, _ := mcgpForUSDMicros(big.NewInt(1_000_000), big.NewInt(300_000)); got.String() != "3333333333333333334" {
		t.Fatalf("mcgp: %s", got)
	}
	if got := displayAmount(big.NewInt(1_500_000), 6); got != "1.50" {
		t.Fatalf("display: %s", got)
	}
}

// ── review fixes: stuck payments, bounces, overpayment, dates, backoff ───

// namedPendingPayment opens a stablecoin change and names a transaction the
// chain has not mined.
func (r *nameChangeRig) namedPendingPayment(t *testing.T) *NameChangeCreated {
	t.Helper()
	out := r.openStable(t)
	if _, err := r.svc.ConfirmPayment(context.Background(), r.user, nameChangeTx); !errors.Is(err, ErrNameChangePaymentOnChain) {
		t.Fatalf("err = %v", err)
	}
	return out
}

// M1: a named payment the chain has never heard of, a day after the change
// ran out of time, is forgotten and the change closed unpaid — and if it lands
// after all, the late-payment window still takes it and sends it back. One the
// chain knows (unmined), or that cannot be looked up, keeps waiting.
func TestNameChange_VanishedPaymentClosesTheChangeUnpaid(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		after   time.Duration
		unknown bool
		lookup  error
		closed  bool
	}{
		"unknown a day after expiry":    {after: 7*24*time.Hour + nameChangeVanishedAfter + time.Minute, unknown: true, closed: true},
		"unknown, not a day yet":        {after: 7*24*time.Hour + time.Hour, unknown: true},
		"known to the chain (pending)":  {after: 7*24*time.Hour + nameChangeVanishedAfter + time.Minute},
		"the chain cannot be looked at": {after: 7*24*time.Hour + nameChangeVanishedAfter + time.Minute, unknown: true, lookup: errors.New("rpc down")},
	} {
		t.Run(name, func(t *testing.T) {
			r := newNameChangeRig(t, NameChangeConfig{})
			out := r.namedPendingPayment(t)
			r.rail.txUnknown, r.rail.txLookupErr = tc.unknown, tc.lookup
			r.now = r.now.Add(tc.after)
			if _, err := r.svc.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			ch := r.change(t, out.Change.ID)
			if !tc.closed {
				if ch.Status != models.NameChangeStatusPaying || ch.PayRef == nil {
					t.Fatalf("must keep waiting: %+v", ch)
				}
				return
			}
			if ch.Status != models.NameChangeStatusCancelled || ch.PayRef != nil || ch.FeeStatus != models.NameChangeFeeNone ||
				!strings.Contains(ch.ReviewNote, nameChangeTx) || ch.UserMessage == "" {
				t.Fatalf("change %+v", ch)
			}
			// It lands after all: confirmed within the late window, taken and refunded.
			p := r.provenPayment(1_000_000)
			p.BlockTime = r.now
			r.rail.payment, r.rail.refundHash, r.rail.refundAttempted = p, "0xback", true
			view, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx)
			if err != nil || view.Status != models.NameChangeStatusCancelled || view.FeeStatus != models.NameChangeFeeRefunded {
				t.Fatalf("a late landing: view %+v err %v", view, err)
			}
		})
	}
}

// The manual way out: a super admin who has read the chain clears a named
// payment that will never pay, with a reason — never their own change, never
// one whose fee was already taken.
func TestNameChange_AdminClearsAnUnprovenPayment(t *testing.T) {
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{})
	out := r.namedPendingPayment(t)
	admin := uuid.New()
	if _, err := r.svc.AdminClearUnprovenPayment(ctx, admin, out.Change.ID, "  "); !errors.Is(err, ErrNameChangeInvalid) {
		t.Fatalf("no reason: err = %v", err)
	}
	if _, err := r.svc.AdminClearUnprovenPayment(ctx, r.user, out.Change.ID, "mine"); !errors.Is(err, ErrNameChangeSelfReview) {
		t.Fatalf("own change: err = %v", err)
	}
	// The chain still knows it (pending): it may yet pay, so it stays.
	if _, err := r.svc.AdminClearUnprovenPayment(ctx, admin, out.Change.ID, "looks stuck"); !errors.Is(err, ErrNameChangePaymentLive) {
		t.Fatalf("a transaction the chain knows: err = %v", err)
	}
	r.rail.txLookupErr = errors.New("rpc down")
	if _, err := r.svc.AdminClearUnprovenPayment(ctx, admin, out.Change.ID, "looks stuck"); !errors.Is(err, ErrNameChangeChainUnreadable) {
		t.Fatalf("an unreadable chain: err = %v", err)
	}
	r.rail.txLookupErr, r.rail.txUnknown = nil, true
	view, err := r.svc.AdminClearUnprovenPayment(ctx, admin, out.Change.ID, "replaced by a zero-value tx on basescan")
	if err != nil || view.Status != models.NameChangeStatusCancelled || view.PayRef != "" || view.ClearedPayRef != nameChangeTx ||
		!strings.Contains(view.ReviewNote, "basescan") || !strings.Contains(view.ReviewNote, admin.String()) {
		t.Fatalf("view %+v err %v", view, err)
	}
	if _, err := r.svc.AdminClearUnprovenPayment(ctx, admin, out.Change.ID, "again"); !errors.Is(err, ErrNameChangeBadState) {
		t.Fatalf("nothing left to clear: err = %v", err)
	}

	paid := newNameChangeRig(t, NameChangeConfig{})
	pout := paid.openStable(t)
	paid.rail.payment = paid.provenPayment(1_000_000)
	if _, err := paid.svc.ConfirmPayment(ctx, paid.user, nameChangeTx); err != nil {
		t.Fatal(err)
	}
	if _, err := paid.svc.AdminClearUnprovenPayment(ctx, admin, pout.Change.ID, "wrong"); !errors.Is(err, ErrNameChangeBadState) {
		t.Fatalf("a proven payment is never cleared: err = %v", err)
	}

	// Proven on-chain but not yet recorded, or mined with money in it and
	// only short of its confirmations: never cleared.
	for name, p := range map[string]*StablePayment{
		"proven, not yet recorded": {Credited: big.NewInt(1_000_000), Confirmations: 10},
		"mined, not deep enough":   {Credited: big.NewInt(1_000_000), Confirmations: 1},
	} {
		t.Run(name, func(t *testing.T) {
			x := newNameChangeRig(t, NameChangeConfig{MinConfirmations: 3})
			xout := x.namedPendingPayment(t)
			p.Sender, p.BlockTime = x.wallet, x.now.Add(time.Minute)
			x.rail.payment = p
			if _, err := x.svc.AdminClearUnprovenPayment(ctx, admin, xout.Change.ID, "wrong"); !errors.Is(err, ErrNameChangePaymentLive) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// Refuted (mined from another wallet, deep): cleared, and not watched —
	// it can never pay.
	ref := newNameChangeRig(t, NameChangeConfig{})
	rout := ref.namedPendingPayment(t)
	other := ref.provenPayment(1_000_000)
	other.Sender = "0x1111111111111111111111111111111111111111"
	ref.rail.payment = other
	rv, err := ref.svc.AdminClearUnprovenPayment(ctx, admin, rout.Change.ID, "paid from someone else's wallet")
	if err != nil || rv.PayRef != "" || rv.ClearedPayRef != "" {
		t.Fatalf("view %+v err %v", rv, err)
	}
	if ch := ref.change(t, rout.Change.ID); len(ch.RefutedPayRefs) != 1 || ch.RefutedPayRefs[0] != nameChangeTx {
		t.Fatalf("refuted %v", ch.RefutedPayRefs)
	}
}

// L1: a payment that cannot pay its change goes back only when it is worth
// sending and the user still has a refund in their allowance; bounces count
// toward that allowance. Otherwise it is kept and left for support.
func TestNameChange_BouncesRespectTheMinimumAndTheAllowance(t *testing.T) {
	ctx := context.Background()
	t.Run("below the minimum", func(t *testing.T) {
		r := newNameChangeRig(t, NameChangeConfig{MinRefundUSD: "0.50"})
		out := r.openStable(t)
		r.rail.payment = r.provenPayment(400_000) // $0.40
		if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); err != nil {
			t.Fatal(err)
		}
		ch := r.change(t, out.Change.ID)
		if ch.Status != models.NameChangeStatusCancelled || ch.FeeStatus != models.NameChangeFeeSettled ||
			ch.FeeDecision != nameChangeFeeKeepAll || r.rail.refunds != 0 ||
			!strings.Contains(ch.ReviewNote, "minimum") || !strings.Contains(ch.UserMessage, "support") {
			t.Fatalf("change %+v refunds=%d", ch, r.rail.refunds)
		}
	})
	t.Run("counts toward, and is held to, the allowance", func(t *testing.T) {
		r := newNameChangeRig(t, NameChangeConfig{MaxRefundedCancels: 1})
		first := r.openStable(t)
		r.rail.payment, r.rail.refundHash, r.rail.refundAttempted = r.provenPayment(600_000), "0xb1", true
		if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); err != nil {
			t.Fatal(err)
		}
		if ch := r.change(t, first.Change.ID); ch.FeeStatus != models.NameChangeFeeRefunded || ch.FeeDecision != nameChangeFeeBounce {
			t.Fatalf("the first bounce is refunded: %+v", ch)
		}
		// The allowance is used: a cancel is refused its refund...
		r.now = r.now.Add(time.Hour)
		r.payMCGP(t)
		var limit *NameChangeLimitError
		if _, err := r.svc.Cancel(ctx, r.user); !errors.As(err, &limit) || !limit.Cancel {
			t.Fatalf("a cancel after a bounce: err = %v", err)
		}
		// ...and so is a second bounce, which is kept for support.
		other := newNameChangeRig(t, NameChangeConfig{MaxRefundedCancels: 1})
		o1 := other.openStable(t)
		other.rail.payment, other.rail.refundHash, other.rail.refundAttempted = other.provenPayment(600_000), "0xb1", true
		if _, err := other.svc.ConfirmPayment(ctx, other.user, nameChangeTx); err != nil {
			t.Fatal(err)
		}
		other.now = other.now.Add(time.Hour)
		o2 := other.openStable(t)
		second := "0x" + strings.Repeat("cd", 32)
		p := other.provenPayment(700_000)
		p.BlockTime = other.now.Add(time.Minute)
		other.rail.payment = p
		if _, err := other.svc.ConfirmPayment(ctx, other.user, second); err != nil {
			t.Fatal(err)
		}
		if ch := other.change(t, o2.Change.ID); ch.FeeStatus != models.NameChangeFeeSettled || ch.FeeDecision != nameChangeFeeKeepAll ||
			!strings.Contains(ch.ReviewNote, "allowance") || other.rail.refunds != 1 {
			t.Fatalf("a second bounce in the window: %+v refunds=%d (first %s)", ch, other.rail.refunds, o1.Change.ID)
		}
	})
}

// L2: a kept fee that was overpaid sends the excess back — only the excess,
// and only when it is above the minimum refund.
func TestNameChange_KeptFeeReturnsTheOverpayment(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		credited int64
		refund   string
		fee      string
	}{
		"overpaid by $0.70": {credited: 1_700_000, refund: "700000", fee: models.NameChangeFeeRefunded},
		"overpaid by $0.20": {credited: 1_200_000, fee: models.NameChangeFeeSettled},
		"paid exactly":      {credited: 1_000_000, fee: models.NameChangeFeeSettled},
	} {
		t.Run(name, func(t *testing.T) {
			r := newNameChangeRig(t, NameChangeConfig{MinRefundUSD: "0.50"})
			out := r.openStable(t)
			r.rail.payment, r.rail.refundHash, r.rail.refundAttempted = r.provenPayment(tc.credited), "0xexcess", true
			if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); err != nil {
				t.Fatal(err)
			}
			ch := r.session(t, "sess-1")
			ch = r.decide(t, strongPass(ch, "ADA OBI"), ch.ID)
			if ch.Status != models.NameChangeStatusApproved || ch.FeeStatus != tc.fee || ch.RefundAmount != tc.refund {
				t.Fatalf("change %+v", ch)
			}
			if tc.refund != "" && (r.rail.refunds != 1 || r.rail.refundedAmount.String() != tc.refund || !strings.EqualFold(r.rail.refundedTo, r.wallet)) {
				t.Fatalf("refunds=%d amount=%v to=%s", r.rail.refunds, r.rail.refundedAmount, r.rail.refundedTo)
			}
			if tc.refund == "" && r.rail.refunds != 0 {
				t.Fatalf("nothing to send back, sent %d", r.rail.refunds)
			}
			_ = out
		})
	}
}

// L3: the dates of birth are kept for the admin while a change is held, and
// dropped once it is decided — whether they matched stays on record.
func TestNameChange_DatesOfBirthAreDroppedOnceDecided(t *testing.T) {
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{})
	r.payMCGP(t)
	ch := r.session(t, "sess-1")
	ch = r.decide(t, strongPass(ch, "EMEKA NWOSU"), ch.ID)
	if ch.Status != models.NameChangeStatusHeldForReview || ch.OriginalDOB != "1990-05-17" || ch.NewDOB != "1990-05-17" {
		t.Fatalf("a held change keeps them for its reviewer: %+v", ch)
	}
	if _, err := r.svc.AdminReject(ctx, uuid.New(), ch.ID, "different person"); err != nil {
		t.Fatal(err)
	}
	if got := r.change(t, ch.ID); got.OriginalDOB != "" || got.NewDOB != "" || got.DOBMatch == nil || !*got.DOBMatch {
		t.Fatalf("rejected: %+v", got)
	}

	auto := newNameChangeRig(t, NameChangeConfig{})
	auto.payMCGP(t)
	ach := auto.session(t, "sess-1")
	ach = auto.decide(t, strongPass(ach, "ADA OBI"), ach.ID)
	if ach.Status != models.NameChangeStatusApproved || ach.OriginalDOB != "" || ach.NewDOB != "" || ach.DOBMatch == nil || !*ach.DOBMatch {
		t.Fatalf("applied: %+v", ach)
	}
}

// L8: a refund that never left is retried after 1m, 2m, 4m… capped at an hour —
// not on every sweep.
func TestNameChange_RefundRetriesBackOff(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute,
		6: 32 * time.Minute, 7: time.Hour, 30: time.Hour} {
		if got := refundBackoff(attempt); got != want {
			t.Errorf("attempt %d: %s, want %s", attempt, got, want)
		}
	}
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{})
	r.openStable(t)
	r.rail.payment = r.provenPayment(1_000_000)
	if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); err != nil {
		t.Fatal(err)
	}
	r.rail.refundErr = ErrTreasuryKilled // never sent
	view, _ := r.svc.Cancel(ctx, r.user)
	ch := r.change(t, view.ID)
	if ch.FeeStatus != models.NameChangeFeeRefundFailed || ch.RefundAttempts != 1 || ch.RefundRetryAt == nil ||
		!ch.RefundRetryAt.Equal(r.now.Add(time.Minute)) {
		t.Fatalf("change %+v", ch)
	}
	r.svc.Sweep(ctx)
	if r.rail.refunds != 1 {
		t.Fatalf("retried before its backoff (%d sends)", r.rail.refunds)
	}
	r.now = r.now.Add(time.Minute)
	r.svc.Sweep(ctx) // fails again: next in 2m
	if ch = r.change(t, view.ID); r.rail.refunds != 2 || ch.RefundAttempts != 2 || !ch.RefundRetryAt.Equal(r.now.Add(2*time.Minute)) {
		t.Fatalf("second try: refunds=%d %+v", r.rail.refunds, ch)
	}
	r.now = r.now.Add(time.Minute)
	r.svc.Sweep(ctx)
	if r.rail.refunds != 2 {
		t.Fatalf("retried inside the doubled backoff (%d sends)", r.rail.refunds)
	}
	r.now = r.now.Add(time.Minute)
	r.rail.refundErr, r.rail.refundHash, r.rail.refundAttempted = nil, "0xok", true
	r.svc.Sweep(ctx)
	if got := r.change(t, view.ID); got.FeeStatus != models.NameChangeFeeRefunded || r.rail.refunds != 3 {
		t.Fatalf("once the backoff passed: %+v refunds=%d", got, r.rail.refunds)
	}
}
