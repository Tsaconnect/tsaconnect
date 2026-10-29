package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"golang.org/x/sync/errgroup"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The pool console (spec 2026-10-14 §7, no-collateral amendment): the pool's
// books, what open orders hold back, the LPs' pending withdrawals and paying
// them. Refilling is signed by the company's refill wallet in the console,
// never by a server key; payPending is the operator's and only ever pays an
// LP's own wallet.

// P2P audit action for the operator's payPending.
const poolAuditPayPending = "liquidity.pay_pending"

// SetVaultLedger gives the console the ledger's reconcile view.
func (s *ITService) SetVaultLedger(x *VaultIndexer) {
	if x != nil && s.poolDesk != nil && s.poolDesk.c.Treasury != nil {
		x.SetTreasury(s.poolDesk.c.Treasury.Address()) // our fills pay only the treasury
	}
	s.vaultLedger = x
}

// VaultPoolSummary is GET /api/admin/liquidity/pool. Token amounts are
// decimal strings in whole tokens.
type VaultPoolSummary struct {
	Vault        string `json:"vault"`
	ChainID      int64  `json:"chainId"`
	Token        string `json:"token"`
	TokenAddress string `json:"tokenAddress"`
	Decimals     int    `json:"decimals"`
	// The wallets that sign in the console (collateralProvider is the
	// company's refill wallet; the contract kept the name). Operators are the
	// ledger's OperatorSet history (the contract keeps a mapping, not a list).
	CollateralProvider   string   `json:"collateralProvider,omitempty"`
	Guardian             string   `json:"guardian,omitempty"`
	Operators            []string `json:"operators"`
	ServerOperator       string   `json:"serverOperator"`
	ServerOperatorActive bool     `json:"serverOperatorActive"`
	Paused               bool     `json:"paused"`
	Outstanding          string   `json:"outstanding"` // sold + spread the company owes the pool until it refills
	Liquid               string   `json:"liquid"`      // balance − pending withdrawals: what fills and claims may use
	Reserved             string   `json:"reserved"`    // fills of open orders not yet proven
	Free                 string   `json:"free"`        // liquid − reserved
	TotalPrincipal       string   `json:"totalPrincipal"`
	// TotalPendingWithdrawals is the contract's total; PendingWithdrawals the
	// wallets behind it the ledger has indexed (each amount read from the
	// contract), in the order they first went pending.
	TotalPendingWithdrawals string                   `json:"totalPendingWithdrawals"`
	PendingWithdrawals      []VaultPendingWithdrawal `json:"pendingWithdrawals"`
	Utilisation             string                   `json:"utilisationPercent"` // outstanding ÷ (principal + pending)
	ActiveLPs               int64                    `json:"activeLps"`
	Spread30d               string                   `json:"spread30d"`
	// The contract's deposit limits (read-only here: only the pool owner's
	// setLimits changes them). Omitted when the read failed.
	MinDeposit string                `json:"minDeposit,omitempty"`
	MaxPerLP   string                `json:"maxPerLp,omitempty"`
	TotalCap   string                `json:"totalCap,omitempty"`
	Ledger     *VaultReconcileReport `json:"ledger,omitempty"`
}

// VaultPendingWithdrawal is one LP wallet's pending withdrawal. LP is the LP
// account (payment_merchants id) when the wallet is known, else omitted.
type VaultPendingWithdrawal struct {
	LP     string `json:"lp,omitempty"`
	Wallet string `json:"wallet"`
	Amount string `json:"amount"`
}

func (s *ITService) VaultPoolSummary(ctx context.Context) (*VaultPoolSummary, error) {
	d := s.poolDesk
	if d == nil {
		return nil, ErrLiquidityNotVault
	}
	st, err := d.c.Vault.State(ctx)
	if err != nil {
		return nil, err
	}
	reserved, err := d.vaultReserved(ctx, s.db)
	if err != nil {
		return nil, err
	}
	free := vaultFree(st, reserved.Fill)
	dec := d.c.Token.Decimals
	owedToLPs := new(big.Int).Add(st.TotalPrincipal, st.TotalPendingWithdrawals)
	out := &VaultPoolSummary{Vault: d.c.Vault.Address(), ChainID: d.c.Token.ChainID, Token: d.c.Token.Symbol, Paused: st.Paused,
		TokenAddress: d.c.Token.Address, Decimals: dec, Operators: []string{}, ServerOperator: d.c.Sender.Address(),
		Outstanding: BaseUnitsToDecimal(st.Outstanding, dec), Liquid: BaseUnitsToDecimal(st.Liquid(), dec),
		Reserved: BaseUnitsToDecimal(reserved.Fill, dec), Free: BaseUnitsToDecimal(free, dec),
		TotalPrincipal:          BaseUnitsToDecimal(st.TotalPrincipal, dec),
		TotalPendingWithdrawals: BaseUnitsToDecimal(st.TotalPendingWithdrawals, dec),
		PendingWithdrawals:      []VaultPendingWithdrawal{},
		Utilisation:             ratioPercent(st.Outstanding, owedToLPs)}
	pending, err := s.pendingWithdrawals(ctx, d)
	if err != nil {
		return nil, err
	}
	for _, p := range pending {
		out.PendingWithdrawals = append(out.PendingWithdrawals, VaultPendingWithdrawal{LP: p.lp, Wallet: p.wallet,
			Amount: BaseUnitsToDecimal(p.amount, dec)})
	}
	if err := s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("kind = ? AND status = ? AND pool_chain_id = ?", models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, d.c.Token.ChainID).
		Count(&out.ActiveLPs).Error; err != nil {
		return nil, err
	}
	if lim, err := d.c.Vault.Limits(ctx); err == nil {
		out.MinDeposit, out.MaxPerLP, out.TotalCap = BaseUnitsToDecimal(lim.MinDeposit, dec), BaseUnitsToDecimal(lim.MaxPerLP, dec),
			BaseUnitsToDecimal(lim.TotalCap, dec)
	} else {
		log.Printf("[liquidity] vault deposit limits unreadable: %s", RedactAlertText(err.Error()))
	}
	if roles, err := d.c.Vault.Roles(ctx); err == nil {
		out.CollateralProvider, out.Guardian = roles.CollateralProvider, roles.Guardian
	} else {
		log.Printf("[liquidity] vault roles unreadable: %s", RedactAlertText(err.Error()))
	}
	if ops, err := vaultOperators(ctx, s.db, d.c.Token.ChainID, d.c.Vault.Address()); err == nil {
		out.Operators = ops
		for _, op := range ops {
			out.ServerOperatorActive = out.ServerOperatorActive || SameEVMAddress(op, out.ServerOperator)
		}
	} else {
		return nil, err
	}
	if x := s.vaultLedger; x != nil {
		if rep, err := x.Reconcile(ctx); err == nil {
			out.Ledger = rep
		}
		if sum, err := ledgerSpreadSince(ctx, s.db, d.c.Token.ChainID, d.c.Vault.Address(), time.Now().Add(-30*24*time.Hour)); err == nil {
			out.Spread30d = BaseUnitsToDecimal(sum, dec)
		}
	}
	return out, nil
}

// ledgerSpreadSince is the spread this vault's fills credited LPs since since,
// from the ledger (summed in Go: chain units overflow SQLite's numerics). The
// indexer writes vault in lower case.
func ledgerSpreadSince(ctx context.Context, db *gorm.DB, chainID int64, vault string, since time.Time) (*big.Int, error) {
	var rows []models.LiquidityLedgerEntry
	if err := db.WithContext(ctx).Where("chain_id = ? AND vault = ? AND kind = ? AND block_time >= ?",
		chainID, strings.ToLower(vault), VaultEventFilled, since).Select("spread").Find(&rows).Error; err != nil {
		return nil, err
	}
	sum := new(big.Int)
	for _, r := range rows {
		sum.Add(sum, bigFromDecimal(r.Spread))
	}
	return sum, nil
}

// vaultOperators replays the ledger's OperatorSet events: the accounts whose
// latest event allowed them, in the order they were first added.
func vaultOperators(ctx context.Context, db *gorm.DB, chainID int64, vault string) ([]string, error) {
	var rows []models.LiquidityLedgerEntry
	if err := db.WithContext(ctx).Where("chain_id = ? AND LOWER(vault) = LOWER(?) AND kind = ?", chainID, vault, VaultEventOperatorSet).
		Order("block, log_index").Select("account", "allowed").Find(&rows).Error; err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	var order []string
	for _, r := range rows {
		key := strings.ToLower(r.Account)
		if _, seen := allowed[key]; !seen {
			order = append(order, r.Account)
		}
		allowed[key] = r.Allowed != nil && *r.Allowed
	}
	out := []string{}
	for _, a := range order {
		if allowed[strings.ToLower(a)] {
			out = append(out, a)
		}
	}
	return out, nil
}

// pendingWallet is one wallet with a pending withdrawal.
type pendingWallet struct {
	lp     string // LP row id, "" when no LP row names the wallet
	wallet string // checksummed
	amount *big.Int
}

// ledgerPendingWallets replays the ledger: WithdrawalPending minus
// WithdrawalPaid per wallet, the wallets still owed in the order they first
// went pending (checksummed).
func ledgerPendingWallets(ctx context.Context, db *gorm.DB, chainID int64, vault string) ([]string, map[string]*big.Int, error) {
	var rows []models.LiquidityLedgerEntry
	if err := db.WithContext(ctx).Where("chain_id = ? AND vault = ? AND kind IN ?", chainID, strings.ToLower(vault),
		[]string{VaultEventWithdrawalPending, VaultEventWithdrawalPaid}).
		Order("block, log_index").Select("kind", "account", "amount").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	owed := map[string]*big.Int{}
	var order []string
	for _, r := range rows {
		if !common.IsHexAddress(r.Account) {
			continue
		}
		w := common.HexToAddress(r.Account).Hex()
		if _, seen := owed[w]; !seen {
			owed[w] = new(big.Int)
			order = append(order, w)
		}
		if r.Kind == VaultEventWithdrawalPending {
			owed[w].Add(owed[w], bigFromDecimal(r.Amount))
		} else {
			owed[w].Sub(owed[w], bigFromDecimal(r.Amount))
		}
	}
	var out []string
	for _, w := range order {
		if owed[w].Sign() > 0 {
			out = append(out, w)
		}
	}
	return out, owed, nil
}

// pendingReadConcurrency bounds the console's parallel pendingWithdrawal reads.
const pendingReadConcurrency = 8

// pendingWithdrawals are the wallets the ledger shows owed, each amount read
// from the contract (the contract wins: a payment the ledger has not indexed
// yet drops the wallet); an unreadable wallet keeps the ledger's amount. The
// reads run concurrently (bounded); the order stays the ledger's.
func (s *ITService) pendingWithdrawals(ctx context.Context, d *PoolDesk) ([]pendingWallet, error) {
	wallets, owed, err := ledgerPendingWallets(ctx, s.db, d.c.Token.ChainID, d.c.Vault.Address())
	if err != nil {
		return nil, err
	}
	amounts := make([]*big.Int, len(wallets))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(pendingReadConcurrency)
	for i, w := range wallets {
		g.Go(func() error {
			amounts[i] = owed[w]
			if onChain, err := d.c.Vault.PendingWithdrawal(gctx, w); err == nil {
				amounts[i] = onChain
			} else {
				log.Printf("[liquidity] pending withdrawal of %s unreadable, showing the ledger's: %s", w, RedactAlertText(err.Error()))
			}
			return nil
		})
	}
	_ = g.Wait() // a read failure keeps the ledger's amount, never fails the list
	var live []string
	for i, w := range wallets {
		if amounts[i].Sign() > 0 {
			live = append(live, w)
		}
	}
	lps := s.lpIDsForWallets(ctx, d, live)
	out := []pendingWallet{}
	for i, w := range wallets {
		if amounts[i].Sign() > 0 {
			out = append(out, pendingWallet{lp: lps[strings.ToLower(w)], wallet: w, amount: amounts[i]})
		}
	}
	return out, nil
}

// lpIDsForWallets maps each wallet (lower-case) to the LP row whose current —
// or else a previous — wallet it is; absent when none (or unreadable). One
// query for the current wallets, one more only for those still unresolved.
func (s *ITService) lpIDsForWallets(ctx context.Context, d *PoolDesk, wallets []string) map[string]string {
	out := map[string]string{}
	if len(wallets) == 0 {
		return out
	}
	lower := make([]string, len(wallets))
	for i, w := range wallets {
		lower[i] = strings.ToLower(w)
	}
	lps := func() *gorm.DB {
		return s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).
			Where("kind = ? AND pool_chain_id = ?", models.PaymentMerchantKindLP, d.c.Token.ChainID)
	}
	var current []models.PaymentMerchant
	if err := lps().Where("LOWER(operator_wallet_address) IN ?", lower).
		Select("id", "operator_wallet_address").Order("created_at").Find(&current).Error; err != nil {
		return out
	}
	for _, r := range current {
		lw := strings.ToLower(r.OperatorWalletAddress)
		if _, seen := out[lw]; !seen {
			out[lw] = r.ID.String()
		}
	}
	var unresolved []string
	for _, lw := range lower {
		if _, ok := out[lw]; !ok {
			unresolved = append(unresolved, lw)
		}
	}
	if len(unresolved) == 0 {
		return out
	}
	q := lps()
	conds := s.db.Where("1 = 0")
	for _, lw := range unresolved {
		conds = conds.Or("LOWER(COALESCE(previous_operator_wallets, '')) LIKE ?", "%"+lw+"%")
	}
	var previous []models.PaymentMerchant
	if err := q.Where(conds).Select("id", "previous_operator_wallets").Order("created_at").Find(&previous).Error; err != nil {
		return out
	}
	for _, lw := range unresolved {
		for _, r := range previous {
			if r.PreviousOperatorWallets != nil && strings.Contains(strings.ToLower(*r.PreviousOperatorWallets), lw) {
				out[lw] = r.ID.String()
				break
			}
		}
	}
	return out
}

// PaidPending is one payPending the console's operation sent (or had sent).
// Amount is what it was sent expecting to pay: min(pending, what the vault
// held after the operation's earlier sends), whole tokens.
type PaidPending struct {
	Wallet string `json:"wallet"`
	TxHash string `json:"txHash"`
	Amount string `json:"amount"`
}

// SkippedPending is a pending wallet the operation did not send to: the
// vault's balance was already spoken for by the wallets before it.
type SkippedPending struct {
	Wallet  string `json:"wallet"`
	Pending string `json:"pending"`
	Reason  string `json:"reason"`
}

// PayPendingResult is POST /api/admin/liquidity/pool/pay-pending's answer.
type PayPendingResult struct {
	Paid    []PaidPending    `json:"paid"`
	Skipped []SkippedPending `json:"skipped"`
}

// payPendingSkipReason: the vault holds less than the wallets before this one
// were sent.
const payPendingSkipReason = "not enough liquid yet"

var (
	// ErrPoolOperationID: a console money action names its operation, so a
	// retry is never a second send.
	ErrPoolOperationID = errors.New("liquidity pool: the action needs an operationId (UUID)")
	// ErrPoolPendingWallet: pay-pending's wallet is not an address.
	ErrPoolPendingWallet = errors.New("liquidity pool: wallet is not a valid address")
)

func poolPayPendingOp(operationID uuid.UUID, wallet string) string {
	return "pool_pay_pending:" + operationID.String() + ":" + strings.ToLower(wallet)
}

// PayPoolPending sends payPending for pending withdrawals: wallet's alone, or
// (wallet "") every wallet the ledger shows owed, in the order they went
// pending. Super admin. The contract pays only the LP's own wallet, at most
// min(pending, its token balance).
//
// Sends are not awaited, so the vault's balance is read once (at head) and
// budgeted: each wallet is sent expecting min(pending, remaining), remaining
// being the balance less what this operation's earlier sends expect; once it
// is spent the rest are skipped ("not enough liquid yet") and nothing is
// sent — no gas on a payPending that would pay 0, no audit row claiming money
// that never moved. Single-wallet mode budgets the same way.
//
// Idempotent per operationID (the console's UUID for one action): each
// wallet's send is the lane operation pool_pay_pending:<operationID>:<wallet>,
// whose pre-broadcast hook writes the audit row (pending, expected) in the
// same transaction as the signing — so a retry is answered with the first
// send's hash and expected amount (its lane allocation, or that audit row
// once mined and the key freed), never a second send. An earlier send's
// expected amount still counts against the budget (conservative: once mined
// the balance already shows it, so a retry may skip a wallet that could now
// be paid — a new operation pays it). A wallet with nothing pending on-chain
// is neither paid nor skipped.
func (s *ITService) PayPoolPending(ctx context.Context, adminID, operationID uuid.UUID, wallet, note string) (*PayPendingResult, error) {
	if note = strings.TrimSpace(note); note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	if operationID == uuid.Nil {
		return nil, ErrPoolOperationID
	}
	d := s.poolDesk
	if d == nil {
		return nil, ErrLiquidityNotVault
	}
	var wallets []string
	if wallet = strings.TrimSpace(wallet); wallet != "" {
		if !common.IsHexAddress(wallet) || common.HexToAddress(wallet) == (common.Address{}) {
			return nil, ErrPoolPendingWallet
		}
		wallets = []string{common.HexToAddress(wallet).Hex()}
	} else {
		owed, _, err := ledgerPendingWallets(ctx, s.db, d.c.Token.ChainID, d.c.Vault.Address())
		if err != nil {
			return nil, err
		}
		wallets = owed
	}
	out := &PayPendingResult{Paid: []PaidPending{}, Skipped: []SkippedPending{}}
	if len(wallets) == 0 {
		return out, nil
	}
	st, err := d.c.Vault.State(ctx) // at head, uncached
	if err != nil {
		return nil, err
	}
	remaining := new(big.Int).Set(st.Balance)
	dec := d.c.Token.Decimals
	for _, w := range wallets {
		r, err := s.payPendingOne(ctx, d, adminID, operationID, w, note, remaining)
		if err != nil {
			return out, err
		}
		switch {
		case r.hash != "":
			remaining.Sub(remaining, r.expected)
			out.Paid = append(out.Paid, PaidPending{Wallet: w, TxHash: r.hash, Amount: BaseUnitsToDecimal(r.expected, dec)})
		case r.pending != nil && r.pending.Sign() > 0:
			out.Skipped = append(out.Skipped, SkippedPending{Wallet: w, Pending: BaseUnitsToDecimal(r.pending, dec), Reason: payPendingSkipReason})
		}
	}
	return out, nil
}

// payPendingOutcome is one wallet's payPending under an operation: sent (or
// sent before) with hash and expected; skipped (no hash, pending > 0); or
// nothing pending (no hash, pending 0).
type payPendingOutcome struct {
	hash              string
	pending, expected *big.Int
}

// payPendingOne is one wallet's payPending under operationID: the earlier
// send when there is one, nothing when nothing is pending or remaining (the
// vault balance this operation has not yet spoken for) is spent, else a new
// send expecting min(pending, remaining).
func (s *ITService) payPendingOne(ctx context.Context, d *PoolDesk, adminID, operationID uuid.UUID, w, note string, remaining *big.Int) (payPendingOutcome, error) {
	op := poolPayPendingOp(operationID, w)
	if prior, err := s.payPendingSentBefore(ctx, d, op); err != nil || prior.hash != "" {
		return prior, err
	}
	pending, err := d.c.Vault.PendingWithdrawal(ctx, w)
	if err != nil {
		return payPendingOutcome{}, err
	}
	if pending.Sign() == 0 || remaining.Sign() <= 0 {
		return payPendingOutcome{pending: pending}, nil
	}
	expected := new(big.Int).Set(pending)
	if expected.Cmp(remaining) > 0 {
		expected.Set(remaining)
	}
	meta := map[string]any{"op": op, "operationId": operationID.String(), "wallet": w, "pending": pending.String(),
		"expected": expected.String(), "note": note}
	sendCtx := WithPreBroadcast(WithOperationRef(ctx, op), func(hctx context.Context, _ uint64, hash string) error {
		return recordPayPending(LaneDB(hctx, s.db), adminID, op, hash, meta)
	})
	hash, err := d.c.Sender.Send(sendCtx, d.c.Token.ChainID, func() ([]byte, error) {
		return d.c.Vault.PreparePayPending(d.c.Sender.Address(), w)
	})
	if err != nil && !errors.Is(err, ErrPendingBroadcast) {
		return payPendingOutcome{}, err
	}
	d.vaultCache.Forget("free")
	log.Printf("[liquidity] METRIC liquidity_pay_pending wallet=%s pending=%s expected=%s tx=%s admin=%s op=%s", w, pending, expected, hash, adminID, operationID)
	return payPendingOutcome{hash: hash, pending: pending, expected: expected}, nil
}

// recordPayPending is a payPending's pre-broadcast hook, in the key lane's
// allocation transaction: the audit row (one per operation; a second is a
// refusal and rolls the allocation back).
func recordPayPending(tx *gorm.DB, adminID uuid.UUID, op, hash string, meta map[string]any) error {
	var rows []models.P2PAuditLog
	if err := payPendingAudits(tx, op).Select("id", "metadata").Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		if payPendingAuditOp(r) == op {
			return fmt.Errorf("liquidity pool: %s is already recorded", op)
		}
	}
	m := map[string]any{"tx": hash}
	for k, v := range meta {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	return tx.Create(&models.P2PAuditLog{ID: uuid.New(), ActorID: &adminID, Action: poolAuditPayPending, Metadata: datatypes.JSON(raw)}).Error
}

// payPendingAudits narrows to the audit rows that may be op's: the indexed
// action first, then op's text in the metadata matched literally (the key's
// "_" is a LIKE wildcard, so it is escaped). The text match is only a
// filter — a row is op's when its metadata's "op" equals op
// (payPendingAuditOp), checked in Go by every reader.
func payPendingAudits(db *gorm.DB, op string) *gorm.DB {
	return db.Model(&models.P2PAuditLog{}).Where("action = ?", poolAuditPayPending).
		Where(`CAST(metadata AS TEXT) LIKE ? ESCAPE '\'`, "%"+LikeLiteral(op)+"%")
}

// payPendingAuditOp is the operation key an audit row records ("" when its
// metadata does not parse).
func payPendingAuditOp(r models.P2PAuditLog) string {
	var m struct {
		Op string `json:"op"`
	}
	if json.Unmarshal(r.Metadata, &m) != nil {
		return ""
	}
	return m.Op
}

// payPendingSentBefore is a payPending already sent for op: its hash (its
// live allocation on the operator's lane, or — once mined, the lane frees a
// mined operation's key — its audit row) and, from the audit row the hook
// wrote with the signing, what it was sent expecting (a row written before
// "expected" was recorded falls back to its pending). hash "" when none.
func (s *ITService) payPendingSentBefore(ctx context.Context, d *PoolDesk, op string) (payPendingOutcome, error) {
	var out payPendingOutcome
	if lane := laneOfSender(d.c.Sender, d.c.Token.ChainID); lane != nil {
		hash, found, err := lane.OperationStatus(ctx, d.c.Sender.Address(), op)
		if err != nil {
			return out, err
		}
		if found && hash != "" {
			out.hash = hash
		}
	}
	var rows []models.P2PAuditLog
	if err := payPendingAudits(s.db.WithContext(ctx), op).Order("created_at").Limit(5).Find(&rows).Error; err != nil {
		return payPendingOutcome{}, err
	}
	for _, r := range rows {
		var m struct {
			Op       string `json:"op"`
			Tx       string `json:"tx"`
			Pending  string `json:"pending"`
			Expected string `json:"expected"`
		}
		if json.Unmarshal(r.Metadata, &m) != nil || m.Op != op || m.Tx == "" {
			continue
		}
		if out.hash == "" {
			out.hash = m.Tx
		}
		out.pending = bigFromDecimal(&m.Pending)
		out.expected = out.pending
		if m.Expected != "" {
			out.expected = bigFromDecimal(&m.Expected)
		}
		return out, nil
	}
	if out.hash != "" {
		// The hook writes the audit row with the allocation, so this is a row
		// whose metadata no longer parses: the hash still answers the retry.
		out.pending, out.expected = new(big.Int), new(big.Int)
	}
	return out, nil
}
