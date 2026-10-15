package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Pending withdrawals (no-collateral amendment, 2026-10-14): the console's
// pending list and the operator's payPending.

const (
	pendingLPA = "0x00000000000000000000000000000000000000A1"
	pendingLPB = "0x00000000000000000000000000000000000000B2"
	pendingLPC = "0x00000000000000000000000000000000000000c3"
)

var pendingLedgerSeq int

// seedPendingLedger writes one WithdrawalPending (amount > 0) or
// WithdrawalPaid (amount < 0) row for wallet, in ledger order.
func seedPendingLedger(t *testing.T, r *vaultRig, wallet string, whole int64) {
	t.Helper()
	pendingLedgerSeq++
	kind, amt := VaultEventWithdrawalPending, usdt18(whole)
	if whole < 0 {
		kind, amt = VaultEventWithdrawalPaid, usdt18(-whole)
	}
	a := amt.String()
	if err := r.db.Create(&models.LiquidityLedgerEntry{ID: uuid.New(), ChainID: poolTestChain, Vault: strings.ToLower(testVaultAddr.Hex()),
		TxHash: fmt.Sprintf("0x%064x", 1_000+pendingLedgerSeq), Block: uint64(100 + pendingLedgerSeq), Kind: kind,
		Account: common.HexToAddress(wallet).Hex(), Amount: &a, BlockTime: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
}

func payPendingAuditCount(t *testing.T, r *vaultRig) int64 {
	t.Helper()
	var n int64
	if err := r.db.Model(&models.P2PAuditLog{}).Where("action = ?", poolAuditPayPending).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// Review 2026-10-15: the summary reads each pending wallet's on-chain amount
// concurrently (bounded) and names LP rows with at most two queries — one for
// current wallets, one for previous wallets only when some are unresolved —
// keeping the ledger's order and the same LP resolution.
func TestVaultAdmin_SummaryPendingReadsInParallelAndLooksUpLPsOnce(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	if err := r.db.Exec("ALTER TABLE payment_merchants ADD COLUMN previous_operator_wallets TEXT").Error; err != nil {
		t.Fatal(err)
	}
	insertLP := func(wallet, previous string) uuid.UUID {
		id := uuid.New()
		if err := r.db.Exec(`INSERT INTO payment_merchants (id, user_id, name, status, kind, pool_chain_id, operator_wallet_address, previous_operator_wallets)
			VALUES (?, ?, 'LP', 'active', ?, ?, ?, ?)`, id.String(), uuid.NewString(), models.PaymentMerchantKindLP, poolTestChain,
			wallet, previous).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	var wallets []string
	for i := 1; i <= 12; i++ {
		w := common.HexToAddress(fmt.Sprintf("0x%040x", 0xd00+i)).Hex()
		wallets = append(wallets, w)
		seedPendingLedger(t, r, w, int64(i%5+1)) // ≤ 9 tokens: SQLite keeps larger numerics as REAL
		r.vault.setPending(w, usdt18(int64(i%5+1)))
	}
	current := insertLP(wallets[2], "")
	moved := insertLP("0x00000000000000000000000000000000000000e1", strings.ToLower(wallets[5])+",0x00000000000000000000000000000000000000e2")
	queries := 0
	if err := r.db.Callback().Query().After("gorm:query").Register("count_lp_lookups", func(db *gorm.DB) {
		if db.Statement.Table == "payment_merchants" && strings.Contains(db.Statement.SQL.String(), "operator_wallet") {
			queries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	r.vault.mu.Lock()
	r.vault.pendingDelay = 20 * time.Millisecond
	r.vault.mu.Unlock()
	sum, err := r.it.VaultPoolSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.PendingWithdrawals) != len(wallets) {
		t.Fatalf("pending = %+v", sum.PendingWithdrawals)
	}
	for i, p := range sum.PendingWithdrawals {
		want := ""
		switch i {
		case 2:
			want = current.String()
		case 5:
			want = moved.String()
		}
		if !SameEVMAddress(p.Wallet, wallets[i]) || p.Amount != fmt.Sprint((i+1)%5+1) || p.LP != want {
			t.Fatalf("pending[%d] = %+v, want %s %d lp %q", i, p, wallets[i], (i+1)%5+1, want)
		}
	}
	if queries > 2 {
		t.Fatalf("LP lookups = %d, want at most 2", queries)
	}
	if r.vault.pendingMaxPar < 2 || r.vault.pendingMaxPar > pendingReadConcurrency {
		t.Fatalf("parallel reads = %d, want 2..%d", r.vault.pendingMaxPar, pendingReadConcurrency)
	}
}

// The console lists the wallets the ledger shows owed, in the order they
// went pending, each amount read from the contract (which wins), with the LP
// row when one names the wallet.
func TestVaultAdmin_SummaryListsPendingWithdrawals(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	if err := r.db.Exec("ALTER TABLE payment_merchants ADD COLUMN previous_operator_wallets TEXT").Error; err != nil {
		t.Fatal(err)
	}
	lpID := uuid.New()
	if err := r.db.Exec(`INSERT INTO payment_merchants (id, user_id, name, status, kind, pool_chain_id, operator_wallet_address)
		VALUES (?, ?, 'LP A', 'active', ?, ?, ?)`, lpID.String(), uuid.NewString(), models.PaymentMerchantKindLP, poolTestChain,
		common.HexToAddress(pendingLPA).Hex()).Error; err != nil {
		t.Fatal(err)
	}
	seedPendingLedger(t, r, pendingLPB, 3)
	seedPendingLedger(t, r, pendingLPA, 5)
	seedPendingLedger(t, r, pendingLPC, 2)
	seedPendingLedger(t, r, pendingLPC, -2) // paid in full
	r.vault.setPending(pendingLPA, usdt18(5))
	r.vault.setPending(pendingLPB, usdt18(1)) // partly paid, not yet indexed: the contract wins
	sum, err := r.it.VaultPoolSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.TotalPendingWithdrawals != "6" || len(sum.PendingWithdrawals) != 2 {
		t.Fatalf("pending = %s %+v, want 6 over B then A", sum.TotalPendingWithdrawals, sum.PendingWithdrawals)
	}
	b, a := sum.PendingWithdrawals[0], sum.PendingWithdrawals[1]
	if !SameEVMAddress(b.Wallet, pendingLPB) || b.Amount != "1" || b.LP != "" {
		t.Fatalf("first = %+v, want B 1 with no LP row", b)
	}
	if !SameEVMAddress(a.Wallet, pendingLPA) || a.Amount != "5" || a.LP != lpID.String() {
		t.Fatalf("second = %+v, want A 5 on LP %s", a, lpID)
	}
	// An unreadable contract keeps the ledger's amount.
	r.vault.mu.Lock()
	r.vault.pendingErr = errors.New("rpc down")
	r.vault.mu.Unlock()
	sum, err = r.it.VaultPoolSummary(context.Background())
	if err != nil || len(sum.PendingWithdrawals) != 2 || sum.PendingWithdrawals[0].Amount != "3" {
		t.Fatalf("pending with the contract unreadable = %+v %v, want the ledger's 3 for B", sum.PendingWithdrawals, err)
	}
}

func TestVaultAdmin_PayPendingRefusesBadInput(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	ctx, admin := context.Background(), uuid.New()
	if _, err := r.it.PayPoolPending(ctx, admin, uuid.New(), "", " "); !errors.Is(err, ErrITOperatorNoteRequired) {
		t.Fatalf("no note: %v", err)
	}
	if _, err := r.it.PayPoolPending(ctx, admin, uuid.Nil, "", "n"); !errors.Is(err, ErrPoolOperationID) {
		t.Fatalf("no operation id: %v", err)
	}
	if _, err := r.it.PayPoolPending(ctx, admin, uuid.New(), "0xnope", "n"); !errors.Is(err, ErrPoolPendingWallet) {
		t.Fatalf("bad wallet: %v", err)
	}
	if r.sender.sends != 0 {
		t.Fatalf("sends = %d, want none", r.sender.sends)
	}
}

// One wallet: payPending to that wallet, audited; a wallet with nothing
// pending on-chain is skipped (nothing sent); a retry of the same operation
// is answered with the first hash (from the audit row once mined).
func TestVaultAdmin_PayPendingOneWallet(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	ctx, admin, op := context.Background(), uuid.New(), uuid.New()
	r.vault.setPending(pendingLPA, usdt18(5))
	res, err := r.it.PayPoolPending(ctx, admin, op, strings.ToLower(pendingLPA), "pool refilled")
	if err != nil || len(res.Paid) != 1 || len(res.Skipped) != 0 {
		t.Fatalf("res = %+v %v, want A paid", res, err)
	}
	paid := res.Paid
	if !SameEVMAddress(paid[0].Wallet, pendingLPA) || paid[0].TxHash == "" || paid[0].Amount != "5" {
		t.Fatalf("paid = %+v, want A sent 5", paid)
	}
	if r.sender.sends != 1 || r.sender.lastData != "paypending:"+strings.ToLower(pendingLPA) {
		t.Fatalf("sends = %d last %q", r.sender.sends, r.sender.lastData)
	}
	if p, _ := r.vault.PendingWithdrawal(ctx, pendingLPA); p.Sign() != 0 {
		t.Fatalf("pending after pay = %s", p)
	}
	if n := payPendingAuditCount(t, r); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	again, err := r.it.PayPoolPending(ctx, admin, op, pendingLPA, "pool refilled")
	if err != nil || len(again.Paid) != 1 || again.Paid[0] != paid[0] || r.sender.sends != 1 {
		t.Fatalf("retry = %+v %v sends=%d, want the first send and no second", again, err, r.sender.sends)
	}
	// A new operation for a wallet with nothing pending sends nothing.
	none, err := r.it.PayPoolPending(ctx, admin, uuid.New(), pendingLPA, "again")
	if err != nil || len(none.Paid) != 0 || len(none.Skipped) != 0 || r.sender.sends != 1 {
		t.Fatalf("zero pending = %+v %v sends=%d, want nothing", none, err, r.sender.sends)
	}
}

// All: every wallet the ledger shows owed, in order; one the contract shows
// paid already is skipped.
func TestVaultAdmin_PayPendingAll(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	ctx := context.Background()
	seedPendingLedger(t, r, pendingLPB, 3)
	seedPendingLedger(t, r, pendingLPA, 5)
	seedPendingLedger(t, r, pendingLPC, 2)
	r.vault.setPending(pendingLPA, usdt18(5))
	r.vault.setPending(pendingLPB, usdt18(3))
	// C: paid on-chain, not indexed yet → pending 0 on the contract.
	res, err := r.it.PayPoolPending(ctx, uuid.New(), uuid.New(), "", "pay everyone")
	if err != nil {
		t.Fatal(err)
	}
	paid := res.Paid
	if len(paid) != 2 || len(res.Skipped) != 0 || !SameEVMAddress(paid[0].Wallet, pendingLPB) || !SameEVMAddress(paid[1].Wallet, pendingLPA) {
		t.Fatalf("paid = %+v %v, want B then A", paid, err)
	}
	if r.sender.sends != 2 || payPendingAuditCount(t, r) != 2 {
		t.Fatalf("sends = %d audits = %d, want 2 each", r.sender.sends, payPendingAuditCount(t, r))
	}
	if st, _ := r.vault.State(ctx); st.Balance.Cmp(usdt18(92)) != 0 || st.TotalPendingWithdrawals.Sign() != 0 {
		t.Fatalf("vault = balance %s pending %s, want 92 and none", st.Balance, st.TotalPendingWithdrawals)
	}
}

// On a real key lane: a retry of the same operation is answered from the
// lane's allocation (same hash, one broadcast); once mined (the key freed)
// from the audit row the pre-broadcast hook wrote with the signing.
func TestVaultAdmin_PayPendingIsIdempotentOnTheLane(t *testing.T) {
	r, node, lane := newLaneVaultRig(t, 100, 25)
	ctx, admin, op := context.Background(), uuid.New(), uuid.New()
	r.vault.setPending(pendingLPA, usdt18(5))
	first, err := r.it.PayPoolPending(ctx, admin, op, pendingLPA, "pool refilled")
	if err != nil || len(first.Paid) != 1 || first.Paid[0].TxHash == "" {
		t.Fatalf("pay: %+v %v", first, err)
	}
	h1 := first.Paid[0].TxHash
	if n := payPendingAuditCount(t, r); n != 1 {
		t.Fatalf("audit rows = %d, want the hook's one", n)
	}
	again, err := r.it.PayPoolPending(ctx, admin, op, pendingLPA, "pool refilled")
	if err != nil || len(again.Paid) != 1 || !strings.EqualFold(again.Paid[0].TxHash, h1) || again.Paid[0].Amount != "5" {
		t.Fatalf("retry = %+v %v, want %s", again, err, h1)
	}
	node.mu.Lock()
	node.mined = 1
	node.mu.Unlock()
	node.mine(common.HexToHash(h1), 101, 110)
	if err := lane.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	// The contract still shows pending here (the fake lane does not run the
	// call): only the audit row stops a second send.
	after, err := r.it.PayPoolPending(ctx, admin, op, pendingLPA, "pool refilled")
	if err != nil || len(after.Paid) != 1 || !strings.EqualFold(after.Paid[0].TxHash, h1) || after.Paid[0].Amount != "5" {
		t.Fatalf("retry after mined = %+v %v, want %s", after, err, h1)
	}
	if n := len(node.atNonce(0)) + len(node.atNonce(1)); n != 1 {
		t.Fatalf("payPending broadcasts = %d, want 1", n)
	}
	if n := payPendingAuditCount(t, r); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
}

// payPendingAuditAmounts is wallet → (pending, expected) from the audit rows.
func payPendingAuditAmounts(t *testing.T, r *vaultRig) map[string][2]string {
	t.Helper()
	var rows []models.P2PAuditLog
	if err := r.db.Where("action = ?", poolAuditPayPending).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string][2]string{}
	for _, row := range rows {
		var m struct{ Wallet, Pending, Expected string }
		if err := json.Unmarshal(row.Metadata, &m); err != nil {
			t.Fatal(err)
		}
		out[strings.ToLower(m.Wallet)] = [2]string{m.Pending, m.Expected}
	}
	return out
}

// Review 2026-10-15: "pay all" budgets the vault's balance (read once) over
// the wallets in order, never sending one the balance cannot reach. Balance
// 100, pending A=80, B=80, C=80: A is sent expecting 80, B 20, C is skipped
// (nothing sent, no audit row); the audit rows record what each send expects.
// A retry of the operation answers exactly the same.
func TestVaultAdmin_PayPendingAllBudgetsTheBalance(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	ctx, admin, op := context.Background(), uuid.New(), uuid.New()
	for _, w := range []string{pendingLPA, pendingLPB, pendingLPC} {
		seedPendingLedger(t, r, w, 8) // SQLite keeps numerics past int64 as REAL: the contract's 80 wins
		r.vault.setPending(w, usdt18(80))
	}
	res, err := r.it.PayPoolPending(ctx, admin, op, "", "pay everyone")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Paid) != 2 || !SameEVMAddress(res.Paid[0].Wallet, pendingLPA) || res.Paid[0].Amount != "80" ||
		!SameEVMAddress(res.Paid[1].Wallet, pendingLPB) || res.Paid[1].Amount != "20" {
		t.Fatalf("paid = %+v, want A 80 then B 20", res.Paid)
	}
	if len(res.Skipped) != 1 || !SameEVMAddress(res.Skipped[0].Wallet, pendingLPC) || res.Skipped[0].Pending != "80" ||
		res.Skipped[0].Reason != "not enough liquid yet" {
		t.Fatalf("skipped = %+v, want C (80) not enough liquid yet", res.Skipped)
	}
	if r.sender.sends != 2 {
		t.Fatalf("sends = %d, want 2 (none for C)", r.sender.sends)
	}
	audits := payPendingAuditAmounts(t, r)
	if len(audits) != 2 || audits[strings.ToLower(pendingLPA)] != [2]string{usdt18(80).String(), usdt18(80).String()} ||
		audits[strings.ToLower(pendingLPB)] != [2]string{usdt18(80).String(), usdt18(20).String()} {
		t.Fatalf("audits = %v, want A 80/80 and B 80/20", audits)
	}
	if p, _ := r.vault.PendingWithdrawal(ctx, pendingLPB); p.Cmp(usdt18(60)) != 0 {
		t.Fatalf("B pending after = %s, want 60", p)
	}
	again, err := r.it.PayPoolPending(ctx, admin, op, "", "retry")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(again.Paid) != fmt.Sprint(res.Paid) || fmt.Sprint(again.Skipped) != fmt.Sprint(res.Skipped) ||
		r.sender.sends != 2 || payPendingAuditCount(t, r) != 2 {
		t.Fatalf("retry = %+v sends=%d, want %+v unchanged and no new send", again, r.sender.sends, res)
	}
}

// The same budget before anything is mined: the sends are not awaited, so the
// balance the second wallet is budgeted against is the one read at the start
// less the first send's expected amount.
func TestVaultAdmin_PayPendingAllBudgetsUnminedSends(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	r.sender.noMine = true
	for _, w := range []string{pendingLPA, pendingLPB, pendingLPC} {
		seedPendingLedger(t, r, w, 8) // SQLite keeps numerics past int64 as REAL: the contract's 80 wins
		r.vault.setPending(w, usdt18(80))
	}
	res, err := r.it.PayPoolPending(context.Background(), uuid.New(), uuid.New(), "", "pay everyone")
	if err != nil || len(res.Paid) != 2 || res.Paid[1].Amount != "20" || len(res.Skipped) != 1 || r.sender.sends != 2 {
		t.Fatalf("res = %+v %v sends=%d, want A 80, B 20, C skipped", res, err, r.sender.sends)
	}
}

// One wallet: sent expecting min(pending, balance); with an empty vault it is
// skipped and nothing is sent.
func TestVaultAdmin_PayPendingOneWalletPartial(t *testing.T) {
	r := newVaultRig(t, 30, 25)
	ctx := context.Background()
	r.vault.setPending(pendingLPA, usdt18(50))
	res, err := r.it.PayPoolPending(ctx, uuid.New(), uuid.New(), pendingLPA, "part")
	if err != nil || len(res.Paid) != 1 || res.Paid[0].Amount != "30" || len(res.Skipped) != 0 {
		t.Fatalf("res = %+v %v, want A sent 30", res, err)
	}
	if a := payPendingAuditAmounts(t, r)[strings.ToLower(pendingLPA)]; a != [2]string{usdt18(50).String(), usdt18(30).String()} {
		t.Fatalf("audit = %v, want pending 50 expected 30", a)
	}
	// The vault is now empty: a new operation skips A, sends nothing.
	res, err = r.it.PayPoolPending(ctx, uuid.New(), uuid.New(), pendingLPA, "again")
	if err != nil || len(res.Paid) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Pending != "20" || r.sender.sends != 1 {
		t.Fatalf("res = %+v %v sends=%d, want A skipped (20 pending)", res, err, r.sender.sends)
	}
}

// Review 2026-10-15: the op key's "_" is matched literally and a row is op's
// only when its metadata's op equals the key: a sibling row whose op differs
// only where the key has "_" neither matches the audit lookup nor blocks the
// pre-broadcast hook from recording the real op.
func TestVaultAdmin_PayPendingAuditMatchesTheOpExactly(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	op := poolPayPendingOp(uuid.New(), pendingLPA) // "pool_pay_pending:…"
	sibling := strings.ReplaceAll(op, "_", "x")
	raw, _ := json.Marshal(map[string]any{"op": sibling, "tx": "0xsibling", "pending": "1", "expected": "1"})
	if err := r.db.Create(&models.P2PAuditLog{ID: uuid.New(), Action: poolAuditPayPending, Metadata: raw}).Error; err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := payPendingAudits(r.db, op).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("lookup of %s = %d %v, want no match on the sibling", op, n, err)
	}
	if err := recordPayPending(r.db, uuid.New(), op, "0xreal", map[string]any{"op": op}); err != nil {
		t.Fatalf("record: %v, want the real op recorded", err)
	}
	if err := recordPayPending(r.db, uuid.New(), op, "0xagain", map[string]any{"op": op}); err == nil {
		t.Fatal("a second record of the same op must be refused")
	}
	prior, err := r.it.payPendingSentBefore(context.Background(), r.desk, op)
	if err != nil || prior.hash != "0xreal" {
		t.Fatalf("sent before = %+v %v, want 0xreal", prior, err)
	}
}
