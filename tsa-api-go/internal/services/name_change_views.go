package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Token amounts in these views are whole-token decimal strings ("1.00"), each
// with its exact base-units twin (…BaseUnits) for anything that signs or
// compares: a decimal string is for reading, never for building a transfer.

// displayAmount is a base-units amount as a whole-token decimal string with at
// least two decimal places ("1.00", "0.50", "12.345678"), exact — never rounded.
func displayAmount(base *big.Int, decimals int) string {
	out := BaseUnitsToDecimal(base, decimals)
	dot := strings.Index(out, ".")
	switch {
	case dot < 0:
		return out + ".00"
	case len(out)-dot == 2:
		return out + "0"
	}
	return out
}

// displayBaseUnits is displayAmount for a stored base-units string.
func displayBaseUnits(base string, decimals int) string {
	n, ok := new(big.Int).SetString(strings.TrimSpace(base), 10)
	if !ok {
		return ""
	}
	return displayAmount(n, decimals)
}

func (s *NameChangeService) feeUSD() string {
	if s.feeMicros == nil {
		return ""
	}
	return displayAmount(s.feeMicros, usdMicrosDecimals)
}

// NameChangeTransfer is the stablecoin payment the user is to make.
type NameChangeTransfer struct {
	Chain           string `json:"chain"`
	ChainID         int64  `json:"chainId"`
	Token           string `json:"token"`
	TokenAddress    string `json:"tokenAddress"`
	From            string `json:"from"`
	To              string `json:"to"`
	Amount          string `json:"amount"`
	AmountBaseUnits string `json:"amountBaseUnits"`
	Decimals        int    `json:"decimals"`
	// UnsignedTx is ready to sign when it could be built; absent, the app
	// builds the same ERC-20 transfer itself — from From, the proven wallet.
	UnsignedTx json.RawMessage `json:"unsignedTx,omitempty"`
}

// NameChangeView is a change as its user sees it. Message is the only text
// about the change meant for them; an admin's own note never reaches it.
type NameChangeView struct {
	ID              uuid.UUID           `json:"id"`
	Status          string              `json:"status"`
	FeeUSD          string              `json:"feeUsd"`
	PayMethod       string              `json:"payMethod"`
	PayToken        string              `json:"payToken"`
	PayChain        string              `json:"payChain"`
	PayAmount       string              `json:"payAmount"`
	PayAmountBase   string              `json:"payAmountBaseUnits"`
	PayDecimals     int                 `json:"payDecimals"`
	PayFrom         string              `json:"payFrom,omitempty"`
	FeeStatus       string              `json:"feeStatus"`
	Attempts        int                 `json:"attempts"`
	MaxAttempts     int                 `json:"maxAttempts"`
	ExpiresAt       time.Time           `json:"expiresAt"`
	OldName         string              `json:"oldName"`
	NewVerifiedName string              `json:"newVerifiedName,omitempty"`
	MatchStrength   string              `json:"matchStrength,omitempty"`
	Message         string              `json:"message,omitempty"`
	Transfer        *NameChangeTransfer `json:"transfer,omitempty"`
	CompletedAt     *time.Time          `json:"completedAt,omitempty"`
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
}

func (s *NameChangeService) view(ch *models.UserNameChange) *NameChangeView {
	if ch == nil {
		return nil
	}
	v := &NameChangeView{ID: ch.ID, Status: ch.Status, FeeUSD: ch.FeeUSD, PayMethod: ch.PayMethod, PayToken: ch.PayToken,
		PayChain: ch.PayChain, PayAmount: displayBaseUnits(ch.PayAmount, ch.PayDecimals), PayAmountBase: ch.PayAmount,
		PayDecimals: ch.PayDecimals, PayFrom: ch.PayFrom, FeeStatus: ch.FeeStatus, Attempts: ch.Attempts,
		MaxAttempts: s.cfg.MaxAttempts, ExpiresAt: ch.ExpiresAt, OldName: ch.OldName, NewVerifiedName: ch.NewVerifiedName,
		MatchStrength: ch.MatchStrength, Message: ch.UserMessage, CompletedAt: ch.CompletedAt, CreatedAt: ch.CreatedAt,
		UpdatedAt: ch.UpdatedAt}
	if ch.Status == models.NameChangeStatusPaying && ch.PayMethod == models.NameChangePayStablecoin {
		v.Transfer = &NameChangeTransfer{Chain: ch.PayChain, Token: ch.PayToken, TokenAddress: ch.PayTokenAddress,
			From: ch.PayFrom, To: ch.PayTo, Amount: v.PayAmount, AmountBaseUnits: ch.PayAmount, Decimals: ch.PayDecimals}
		for _, o := range s.stableOptions(context.Background()) {
			if o.Chain == ch.PayChain && strings.EqualFold(o.TokenAddress, ch.PayTokenAddress) {
				v.Transfer.ChainID = o.ChainID
			}
		}
	}
	return v
}

// NameChangeMCGPQuote is the MCGP method's price.
type NameChangeMCGPQuote struct {
	Method           string `json:"method"`
	Available        bool   `json:"available"`
	Chain            string `json:"chain"`
	Token            string `json:"token"`
	Amount           string `json:"amount,omitempty"`
	AmountBaseUnits  string `json:"amountBaseUnits,omitempty"`
	Decimals         int    `json:"decimals"`
	Balance          string `json:"balance,omitempty"` // spendable, of the wallet asked about
	BalanceBaseUnits string `json:"balanceBaseUnits,omitempty"`
}

// NameChangeStableQuote is the stablecoin method's price on each chain/token.
type NameChangeStableQuote struct {
	Method    string                   `json:"method"`
	Available bool                     `json:"available"`
	Options   []NameChangeStableOption `json:"options"`
}

// NameChangeStableOption is one stablecoin the fee can be paid in.
type NameChangeStableOption struct {
	Chain           string `json:"chain"`
	ChainID         int64  `json:"chainId"`
	Token           string `json:"token"`
	TokenAddress    string `json:"tokenAddress"`
	Amount          string `json:"amount"`
	AmountBaseUnits string `json:"amountBaseUnits"`
	Decimals        int    `json:"decimals"`
}

// NameChangeQuote is the fee and how it can be paid right now.
type NameChangeQuote struct {
	FeeUSD  string `json:"feeUsd"`
	Methods []any  `json:"methods"`
}

// NameChangeLimits are the rules the user is held to.
type NameChangeLimits struct {
	MaxPerYear          int `json:"maxPerYear"`
	CompletedInLastYear int `json:"completedInLastYear"`
	MaxAttempts         int `json:"maxAttempts"`
	TTLDays             int `json:"ttlDays"`
}

// Why a user cannot start a change at all (the overview's unavailableReason).
const (
	NameChangeUnavailableDisabled    = "disabled"
	NameChangeUnavailableNotVerified = "not_verified"
)

// NameChangeOverview is GET /api/users/me/name-change.
type NameChangeOverview struct {
	Enabled bool `json:"enabled"`
	// UnavailableReason says why a change cannot be started at all ("disabled"
	// or "not_verified"); empty when one can be, subject to Eligible.
	UnavailableReason string `json:"unavailableReason,omitempty"`
	// Change is the user's open change or, with none open, their latest one —
	// so a result ("updated", "not approved", "expired") can still be shown
	// after the change has closed. Its status says which.
	Change        *NameChangeView     `json:"change"`
	Eligible      bool                `json:"eligible"`
	Blockers      []NameChangeBlocker `json:"blockers"`
	NextAllowedAt *time.Time          `json:"nextAllowedAt,omitempty"`
	Limits        NameChangeLimits    `json:"limits"`
	// Quote is the fee for a new change: null while one is open (nothing to
	// quote) or when none can be started.
	Quote *NameChangeQuote `json:"quote"`
}

// NameChangeCreated is what creating a change returns: the change, and for a
// stablecoin fee the transfer to make.
type NameChangeCreated struct {
	Change   *NameChangeView     `json:"change"`
	Transfer *NameChangeTransfer `json:"transfer,omitempty"`
}

func (s *NameChangeService) mcgpAvailable() bool {
	return s.debits != nil && s.debits.IsEnabled() && s.mcgp != nil
}

// mcgpAmount is the fee in MCGP at the price MCGP bills are paid at: Contract
// B's PremiumSell quote, the platform's conservative side of the spread.
func (s *NameChangeService) mcgpAmount() (*big.Int, error) {
	usdcPerMcgp, err := s.mcgp.Quote(PriceKindPremiumSell, oneE18)
	if err != nil {
		return nil, err
	}
	return mcgpForUSDMicros(s.feeMicros, usdcPerMcgp)
}

func (s *NameChangeService) stableOptions(ctx context.Context) []StableFeeOption {
	if s.rail == nil || !s.rail.Available() {
		return nil
	}
	return s.rail.Options(ctx)
}

// quote prices a new change. wallet (optional, the device's) only adds its
// spendable MCGP balance.
func (s *NameChangeService) quote(ctx context.Context, wallet string) *NameChangeQuote {
	q := &NameChangeQuote{FeeUSD: s.feeUSD(), Methods: []any{}}
	if s.free() {
		return q
	}
	mq := NameChangeMCGPQuote{Method: models.NameChangePayMCGP, Chain: nameChangeMCGPChain, Token: "MCGP", Decimals: 18}
	if s.mcgpAvailable() {
		if amount, err := s.mcgpAmount(); err == nil {
			mq.Available, mq.Amount, mq.AmountBaseUnits = true, displayAmount(amount, 18), amount.String()
			if wallet != "" {
				if bal, err := s.mcgp.PremiumBalance(wallet); err == nil {
					mq.Balance, mq.BalanceBaseUnits = displayAmount(bal, 18), bal.String()
				}
			}
		}
	}
	sq := NameChangeStableQuote{Method: models.NameChangePayStablecoin, Options: []NameChangeStableOption{}}
	for _, o := range s.stableOptions(ctx) {
		amount := stableForUSDMicros(s.feeMicros, o.Decimals)
		sq.Options = append(sq.Options, NameChangeStableOption{Chain: o.Chain, ChainID: o.ChainID, Token: o.Token,
			TokenAddress: o.TokenAddress, Amount: displayAmount(amount, o.Decimals), AmountBaseUnits: amount.String(), Decimals: o.Decimals})
	}
	sq.Available = len(sq.Options) > 0
	q.Methods = append(q.Methods, mq, sq)
	return q
}

// Overview is what the app needs to show the name-change screen. wallet is the
// device's wallet (optional), for the MCGP balance.
func (s *NameChangeService) Overview(ctx context.Context, userID uuid.UUID, wallet string) (*NameChangeOverview, error) {
	out := &NameChangeOverview{Enabled: s.Enabled(), Blockers: []NameChangeBlocker{},
		Limits: NameChangeLimits{MaxPerYear: s.cfg.MaxPerYear, MaxAttempts: s.cfg.MaxAttempts, TTLDays: int(s.cfg.TTL / (24 * time.Hour))}}
	u, err := s.loadUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	open, err := s.openChange(ctx, userID)
	if err != nil {
		return nil, err
	}
	out.Change = s.view(open)
	if open == nil {
		var last models.UserNameChange
		if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at desc").First(&last).Error; err == nil {
			out.Change = s.view(&last)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if !out.Enabled {
		out.UnavailableReason = NameChangeUnavailableDisabled
		out.Blockers = append(out.Blockers, NameChangeBlocker{Code: "name_change_disabled", Message: ErrNameChangeDisabled.Error()})
		return out, nil
	}
	if u.VerificationStatus.String != models.VerificationStatusVerified {
		out.UnavailableReason = NameChangeUnavailableNotVerified
		out.Blockers = append(out.Blockers, NameChangeBlocker{Code: "not_verified", Message: ErrNameChangeNotVerified.Error()})
		return out, nil
	}
	used, next, err := s.limit(ctx, userID)
	if err != nil {
		return nil, err
	}
	out.Limits.CompletedInLastYear, out.NextAllowedAt = used, next
	blockers, err := s.Blockers(ctx, userID)
	if err != nil {
		return nil, err
	}
	out.Blockers = append(out.Blockers, blockers...)
	out.Eligible = open == nil && next == nil && len(blockers) == 0
	// A quote costs chain reads; only a user who could start a change needs one.
	if open == nil {
		out.Quote = s.quote(ctx, strings.TrimSpace(wallet))
	}
	return out, nil
}
