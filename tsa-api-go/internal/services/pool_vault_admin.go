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

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The collateral provider's console (spec 2026-10-14 §7): the pool's books,
// what open orders hold back, and settle-from-collateral. Posting, refilling
// and withdrawing collateral are signed by the PM's own wallet in the
// console, never by a server key.

// P2P audit action for the operator's settle-from-collateral.
const poolAuditSettleFromCollateral = "liquidity.settle_from_collateral"

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
	// The wallets that sign in the console. Operators are the ledger's
	// OperatorSet history (the contract keeps a mapping, not a list).
	CollateralProvider   string                `json:"collateralProvider,omitempty"`
	Guardian             string                `json:"guardian,omitempty"`
	Operators            []string              `json:"operators"`
	ServerOperator       string                `json:"serverOperator"`
	ServerOperatorActive bool                  `json:"serverOperatorActive"`
	Paused               bool                  `json:"paused"`
	Collateral           string                `json:"collateral"`
	Outstanding          string                `json:"outstanding"`
	Reserved             string                `json:"reserved"` // open orders not yet filled: never withdraw it
	Free                 string                `json:"free"`     // collateral − outstanding − reserved
	WithdrawableUp       string                `json:"withdrawableUpTo"`
	TotalPrincipal       string                `json:"totalPrincipal"`
	Utilisation          string                `json:"utilisationPercent"`
	ActiveLPs            int64                 `json:"activeLps"`
	Spread30d            string                `json:"spread30d"`
	Ledger               *VaultReconcileReport `json:"ledger,omitempty"`
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
	free, err := d.vaultFree(ctx, s.db, st)
	if err != nil {
		return nil, err
	}
	dec := d.c.Token.Decimals
	out := &VaultPoolSummary{Vault: d.c.Vault.Address(), ChainID: d.c.Token.ChainID, Token: d.c.Token.Symbol, Paused: st.Paused,
		TokenAddress: d.c.Token.Address, Decimals: dec, Operators: []string{}, ServerOperator: d.c.Sender.Address(),
		Collateral: BaseUnitsToDecimal(st.Collateral, dec), Outstanding: BaseUnitsToDecimal(st.Outstanding, dec),
		Reserved: BaseUnitsToDecimal(reserved, dec), Free: BaseUnitsToDecimal(free, dec),
		// The contract lets the PM take collateral − outstanding; what open
		// orders reserve on top must stay, or they park at the fill.
		WithdrawableUp: BaseUnitsToDecimal(free, dec),
		TotalPrincipal: BaseUnitsToDecimal(st.TotalPrincipal, dec), Utilisation: ratioPercent(st.Outstanding, st.Collateral)}
	if err := s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("kind = ? AND status = ? AND pool_chain_id = ?", models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, d.c.Token.ChainID).
		Count(&out.ActiveLPs).Error; err != nil {
		return nil, err
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

// SettlePoolFromCollateral closes amount of outstanding against collateral:
// the company keeps the fiat it already holds instead of buying USDT back.
// Super admin; the operator sends it; the contract bounds it by outstanding.
//
// Idempotent per operationID (the console's UUID for one settle): the ref is
// HashID(operationID) — the contract refuses a used settle ref — and the
// lane's operation key pool_settle:<operationID>, so a retry is answered with
// the first send's hash (from its allocation, or from the ledger once mined)
// and never sends a second settle.
func (s *ITService) SettlePoolFromCollateral(ctx context.Context, adminID, operationID uuid.UUID, amount, note string) (string, error) {
	if note = strings.TrimSpace(note); note == "" {
		return "", ErrITOperatorNoteRequired
	}
	if operationID == uuid.Nil {
		return "", ErrPoolSettleOperationID
	}
	d := s.poolDesk
	if d == nil {
		return "", ErrLiquidityNotVault
	}
	n, err := DecimalToBaseUnits(strings.TrimSpace(amount), d.c.Token.Decimals)
	if err != nil || n.Sign() <= 0 {
		return "", fmt.Errorf("%w: amount %q", ErrLPAmount, amount)
	}
	op, ref := poolSettleOp(operationID), HashID(operationID)
	if hash, err := s.settledBefore(ctx, d, op, operationID); err != nil || hash != "" {
		return hash, err
	}
	hash, err := d.c.Sender.Send(WithOperationRef(ctx, op), d.c.Token.ChainID, func() ([]byte, error) {
		return d.c.Vault.PrepareSettleFromCollateral(d.c.Sender.Address(), n, ref)
	})
	if err != nil && !errors.Is(err, ErrPendingBroadcast) {
		return "", err
	}
	meta, _ := json.Marshal(map[string]any{"amount": n.String(), "ref": HashIDHex(operationID), "operationId": operationID.String(),
		"tx": hash, "note": note})
	if aerr := s.db.WithContext(context.WithoutCancel(ctx)).Create(&models.P2PAuditLog{ID: uuid.New(), ActorID: &adminID,
		Action: poolAuditSettleFromCollateral, Metadata: datatypes.JSON(meta)}).Error; aerr != nil {
		log.Printf("[liquidity] ALERT liquidity_settle_audit_unwritten tx=%s err=%s", hash, RedactAlertText(aerr.Error()))
	}
	d.vaultCache.Forget("free")
	log.Printf("[liquidity] METRIC liquidity_settle_from_collateral amount=%s tx=%s admin=%s op=%s", n, hash, adminID, operationID)
	return hash, nil
}

// ErrPoolSettleOperationID: a settle names its operation, so a retry is never
// a second settle.
var ErrPoolSettleOperationID = errors.New("liquidity pool: settle needs an operationId (UUID)")

func poolSettleOp(operationID uuid.UUID) string { return "pool_settle:" + operationID.String() }

// settledBefore is the hash of a settle already sent for this operation: its
// live allocation on the operator's lane, or — once mined (the lane frees a
// mined operation's key) — its SettledFromCollateral row in the ledger.
func (s *ITService) settledBefore(ctx context.Context, d *PoolDesk, op string, operationID uuid.UUID) (string, error) {
	if lane := laneOfSender(d.c.Sender, d.c.Token.ChainID); lane != nil {
		hash, found, err := lane.OperationStatus(ctx, d.c.Sender.Address(), op)
		if err != nil {
			return "", err
		}
		if found && hash != "" {
			return hash, nil
		}
	}
	var rows []models.LiquidityLedgerEntry
	if err := s.db.WithContext(ctx).Where("chain_id = ? AND kind = ? AND LOWER(ref) = ?", d.c.Token.ChainID, VaultEventSettled,
		strings.ToLower(HashIDHex(operationID))).Limit(1).Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 1 {
		return rows[0].TxHash, nil
	}
	return "", nil
}
