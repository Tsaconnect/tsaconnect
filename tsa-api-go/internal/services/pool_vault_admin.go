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
)

// The collateral provider's console (spec 2026-10-14 §7): the pool's books,
// what open orders hold back, and settle-from-collateral. Posting, refilling
// and withdrawing collateral are signed by the PM's own wallet in the
// console, never by a server key.

// P2P audit action for the operator's settle-from-collateral.
const poolAuditSettleFromCollateral = "liquidity.settle_from_collateral"

// SetVaultLedger gives the console the ledger's reconcile view.
func (s *ITService) SetVaultLedger(x *VaultIndexer) { s.vaultLedger = x }

// VaultPoolSummary is GET /api/admin/liquidity/pool. Token amounts are
// decimal strings in whole tokens.
type VaultPoolSummary struct {
	Vault          string                `json:"vault"`
	ChainID        int64                 `json:"chainId"`
	Token          string                `json:"token"`
	Paused         bool                  `json:"paused"`
	Collateral     string                `json:"collateral"`
	Outstanding    string                `json:"outstanding"`
	Reserved       string                `json:"reserved"` // open orders not yet filled: never withdraw it
	Free           string                `json:"free"`     // collateral − outstanding − reserved
	WithdrawableUp string                `json:"withdrawableUpTo"`
	TotalPrincipal string                `json:"totalPrincipal"`
	Utilisation    string                `json:"utilisationPercent"`
	ActiveLPs      int64                 `json:"activeLps"`
	Spread30d      string                `json:"spread30d"`
	Ledger         *VaultReconcileReport `json:"ledger,omitempty"`
}

func (s *ITService) VaultPoolSummary(ctx context.Context) (*VaultPoolSummary, error) {
	d := s.poolDesk
	if !d.vaultMode() {
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
	if x := s.vaultLedger; x != nil {
		if rep, err := x.Reconcile(ctx); err == nil {
			out.Ledger = rep
		}
		var rows []models.LiquidityLedgerEntry
		if err := s.db.WithContext(ctx).Where("chain_id = ? AND kind = ? AND block_time >= ?", d.c.Token.ChainID, VaultEventFilled,
			time.Now().Add(-30*24*time.Hour)).Select("spread").Find(&rows).Error; err == nil {
			sum := new(big.Int)
			for _, r := range rows {
				sum.Add(sum, bigFromDecimal(r.Spread))
			}
			out.Spread30d = BaseUnitsToDecimal(sum, dec)
		}
	}
	return out, nil
}

// SettlePoolFromCollateral closes amount of outstanding against collateral:
// the company keeps the fiat it already holds instead of buying USDT back.
// Super admin; the operator sends it; the contract bounds it by outstanding.
func (s *ITService) SettlePoolFromCollateral(ctx context.Context, adminID uuid.UUID, amount, note string) (string, error) {
	if note = strings.TrimSpace(note); note == "" {
		return "", ErrITOperatorNoteRequired
	}
	d := s.poolDesk
	if !d.vaultMode() {
		return "", ErrLiquidityNotVault
	}
	n, err := DecimalToBaseUnits(strings.TrimSpace(amount), d.c.Token.Decimals)
	if err != nil || n.Sign() <= 0 {
		return "", fmt.Errorf("%w: amount %q", ErrLPAmount, amount)
	}
	id := uuid.New()
	ref := HashID(id)
	hash, err := d.c.Sender.Send(WithOperationRef(ctx, "pool_settle:"+id.String()), d.c.Token.ChainID, func() ([]byte, error) {
		return d.c.Vault.PrepareSettleFromCollateral(d.c.Sender.Address(), n, ref)
	})
	if err != nil && !errors.Is(err, ErrPendingBroadcast) {
		return "", err
	}
	meta, _ := json.Marshal(map[string]any{"amount": n.String(), "ref": HashIDHex(id), "tx": hash, "note": note})
	if aerr := s.db.WithContext(context.WithoutCancel(ctx)).Create(&models.P2PAuditLog{ID: uuid.New(), ActorID: &adminID,
		Action: poolAuditSettleFromCollateral, Metadata: datatypes.JSON(meta)}).Error; aerr != nil {
		log.Printf("[liquidity] ALERT liquidity_settle_audit_unwritten tx=%s err=%s", hash, RedactAlertText(aerr.Error()))
	}
	d.vaultCache.Forget("free")
	log.Printf("[liquidity] METRIC liquidity_settle_from_collateral amount=%s tx=%s admin=%s", n, hash, adminID)
	return hash, nil
}
