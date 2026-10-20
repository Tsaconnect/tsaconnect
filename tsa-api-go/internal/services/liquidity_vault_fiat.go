package services

import (
	"context"
	"errors"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Fiat LP (ledger-only): an LP may keep new deposits as naira. The pool and
// its withdraw/claim stay USDT; the ledger records what each deposit was worth
// in naira that day, and the app shows that claim beside the live rate. The
// company settles any difference by hand — nothing here gates a withdrawal.

const LiquidityFiatNGN = "NGN"

var ErrLPFiatUnavailable = errors.New("liquidity: naira positions are not available right now")
var ErrLPFiatCurrency = errors.New("liquidity: unsupported currency")

// LiquidityFiat is the caller's naira position.
type LiquidityFiat struct {
	Currency string `json:"currency"`
	// Principal is the naira the fiat deposits were worth when made, less
	// withdrawals at their average rate. Units is the USDT it is held in.
	Principal string `json:"principal"`
	Units     string `json:"units"`
	AvgRate   string `json:"avgRate,omitempty"`
	// CurrentRate and CurrentUSDT price the claim today (omitted when the rate
	// is unreadable); GapUSDT = CurrentUSDT − Units: positive means the claim
	// is now worth more USDT than the pool holds for it.
	CurrentRate string `json:"currentRate,omitempty"`
	CurrentUSDT string `json:"currentUsdt,omitempty"`
	GapUSDT     string `json:"gapUsdt,omitempty"`
}

// SetFiatMode switches the caller's LP between USDT ("") and naira. Only
// deposits indexed afterwards are locked; earlier principal stays USDT.
func (s *VaultLPService) SetFiatMode(ctx context.Context, userID uuid.UUID, currency string) (*LiquidityVaultMe, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency != "" && currency != LiquidityFiatNGN {
		return nil, ErrLPFiatCurrency
	}
	lp, err := s.vaultLPRow(ctx, userID)
	if err != nil {
		return nil, err
	}
	if currency != "" {
		if s.c.FiatRate == nil {
			return nil, ErrLPFiatUnavailable
		}
		if _, err := s.c.FiatRate(ctx, currency); err != nil {
			return nil, ErrLPFiatUnavailable
		}
	}
	if err := s.c.DB.WithContext(ctx).Model(&models.PaymentMerchant{}).Where("id = ?", lp.ID).
		Update("fiat_currency", currency).Error; err != nil {
		return nil, err
	}
	return s.Me(ctx, userID)
}

// fiatLedgerKinds are the rows that move a wallet's principal.
var fiatLedgerKinds = []string{VaultEventDeposited, VaultEventWithdrawn, VaultEventWithdrawalPending}

// fiatPosition is a wallet's stamped (naira) share of its principal.
type fiatPosition struct {
	units *big.Rat // USDT chain units still held as naira
	naira *big.Rat // what that is worth at the rates it was locked at
}

// foldFiatLedger replays one wallet's principal rows in order. A stamped
// deposit adds its units and naira; any withdrawal takes the same fraction of
// the stamped holding as of the whole principal at that moment (pro rata), so
// mixed USDT/naira history is attributed the same whichever came out first.
// The locked rate of what remains never changes.
func foldFiatLedger(rows []models.LiquidityLedgerEntry) fiatPosition {
	units, naira, total := new(big.Rat), new(big.Rat), new(big.Rat)
	for _, r := range rows {
		a := new(big.Rat).SetInt(bigFromDecimal(r.Amount))
		if r.Kind == VaultEventDeposited {
			total.Add(total, a)
			if r.FiatCurrency != "" && r.FiatAmount != nil {
				f, _ := new(big.Rat).SetString(*r.FiatAmount)
				units.Add(units, a)
				naira.Add(naira, f)
			}
			continue
		}
		if total.Sign() <= 0 {
			continue
		}
		if a.Cmp(total) > 0 {
			a.Set(total)
		}
		keep := new(big.Rat).Sub(big.NewRat(1, 1), new(big.Rat).Quo(a, total))
		units.Mul(units, keep)
		naira.Mul(naira, keep)
		total.Sub(total, a)
	}
	return fiatPosition{units: units, naira: naira}
}

// fiatLedgerRows loads the principal rows of the given wallets (lower case),
// in ledger order, grouped by wallet.
func fiatLedgerRows(ctx context.Context, db *gorm.DB, chainID int64, vault string, wallets []string) (map[string][]models.LiquidityLedgerEntry, error) {
	var rows []models.LiquidityLedgerEntry
	if len(wallets) == 0 {
		return nil, nil
	}
	if err := db.WithContext(ctx).Where("chain_id = ? AND vault = ? AND LOWER(account) IN ? AND kind IN ?",
		chainID, strings.ToLower(vault), wallets, fiatLedgerKinds).Order("block ASC, log_index ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string][]models.LiquidityLedgerEntry{}
	for _, r := range rows {
		k := strings.ToLower(r.Account)
		out[k] = append(out[k], r)
	}
	return out, nil
}

// signedMoney is r to 2 dp, never "-0.00".
func signedMoney(r *big.Rat) string {
	out := r.FloatString(2)
	if strings.Trim(out, "-0.") == "" {
		return "0.00"
	}
	return out
}

// fiatView is the LP's naira position from the ledger's stamped deposits. It
// is read off the stamped rows, not the current mode, so switching back to
// USDT never hides naira still owed. Capped at the on-chain principal.
func (s *VaultLPService) fiatView(ctx context.Context, wallet string, principal *big.Int) (*LiquidityFiat, error) {
	byWallet, err := fiatLedgerRows(ctx, s.c.DB, s.c.Token.ChainID, s.c.Vault.Address(), []string{strings.ToLower(wallet)})
	if err != nil {
		return nil, err
	}
	pos := foldFiatLedger(byWallet[strings.ToLower(wallet)])
	if principal != nil && pos.units.Cmp(new(big.Rat).SetInt(principal)) > 0 {
		scale := new(big.Rat).Quo(new(big.Rat).SetInt(principal), pos.units)
		pos.units.SetInt(principal)
		pos.naira.Mul(pos.naira, scale)
	}
	whole := new(big.Rat).Quo(pos.units, new(big.Rat).SetInt(pow10(s.c.Token.Decimals)))
	out := &LiquidityFiat{Currency: LiquidityFiatNGN, Principal: "0", Units: s.units(new(big.Int).Quo(pos.units.Num(), pos.units.Denom()))}
	if pos.units.Sign() == 0 {
		return out, nil
	}
	out.Principal = signedMoney(pos.naira)
	out.AvgRate = signedMoney(new(big.Rat).Quo(pos.naira, whole))
	if s.c.FiatRate == nil {
		return out, nil
	}
	rate, err := s.c.FiatRate(ctx, LiquidityFiatNGN)
	if err != nil || rate == nil || rate.Sign() <= 0 {
		return out, nil
	}
	cur := new(big.Rat).Quo(pos.naira, rate) // USDT worth of the claim today
	out.CurrentRate = signedMoney(rate)
	out.CurrentUSDT = signedMoney(cur)
	out.GapUSDT = signedMoney(new(big.Rat).Sub(cur, whole))
	return out, nil
}

// VaultFiatExposure is what the company carries for fiat LPs: the naira it is
// held to against the USDT the pool holds for them. Gap > 0 means the claims
// are worth more USDT than the pool holds (the company tops up on exit); < 0,
// the company keeps the difference.
type VaultFiatExposure struct {
	Currency    string `json:"currency"`
	LPs         int    `json:"lps"`
	Naira       string `json:"naira"` // owed to fiat LPs at their locked rates
	Units       string `json:"units"` // USDT the pool holds for them
	CurrentRate string `json:"currentRate,omitempty"`
	CurrentUSDT string `json:"currentUsdt,omitempty"`
	GapUSDT     string `json:"gapUsdt,omitempty"`
}

// fiatExposure sums every wallet that ever had a stamped deposit, whatever
// mode it is in now (two queries however many LPs). Nil when none holds naira.
func (s *ITService) fiatExposure(ctx context.Context, chainID int64, vault string, decimals int) (*VaultFiatExposure, error) {
	var wallets []string
	if err := s.db.WithContext(ctx).Model(&models.LiquidityLedgerEntry{}).
		Where("chain_id = ? AND vault = ? AND kind = ? AND fiat_currency <> ''", chainID, strings.ToLower(vault), VaultEventDeposited).
		Distinct().Pluck("LOWER(account)", &wallets).Error; err != nil {
		return nil, err
	}
	byWallet, err := fiatLedgerRows(ctx, s.db, chainID, vault, wallets)
	if err != nil {
		return nil, err
	}
	out := &VaultFiatExposure{Currency: LiquidityFiatNGN}
	naira, units := new(big.Rat), new(big.Rat)
	for _, w := range wallets {
		pos := foldFiatLedger(byWallet[w])
		if pos.units.Sign() == 0 {
			continue
		}
		out.LPs++
		naira.Add(naira, pos.naira)
		units.Add(units, pos.units)
	}
	if out.LPs == 0 {
		return nil, nil
	}
	whole := new(big.Rat).Quo(units, new(big.Rat).SetInt(pow10(decimals)))
	out.Naira, out.Units = signedMoney(naira), BaseUnitsToDecimal(new(big.Int).Quo(units.Num(), units.Denom()), decimals)
	if rate, err := s.StableMidFiatPerUnit(ctx, LiquidityFiatNGN, models.ITAssetUSDC); err == nil && rate.Sign() > 0 {
		cur := new(big.Rat).Quo(naira, rate)
		out.CurrentRate, out.CurrentUSDT = signedMoney(rate), signedMoney(cur)
		out.GapUSDT = signedMoney(new(big.Rat).Sub(cur, whole))
	}
	return out, nil
}
