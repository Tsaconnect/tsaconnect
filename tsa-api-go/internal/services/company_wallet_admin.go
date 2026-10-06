package services

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// CompanyWalletTokenStatus is one token's stock as the console shows it (trimmed decimals).
type CompanyWalletTokenStatus struct {
	Token    string `json:"token"`
	Balance  string `json:"balance"`
	Reserved string `json:"reserved"`
	Free     string `json:"free"`
	Low      bool   `json:"low"`
	Error    string `json:"error,omitempty"`
}

// CompanyWalletChainStatus is one lane: its gas and its tokens.
type CompanyWalletChainStatus struct {
	Chain          string                     `json:"chain"`
	NativeWei      string                     `json:"nativeWei"`
	NativeFloorWei string                     `json:"nativeFloorWei"`
	NativeLow      bool                       `json:"nativeLow"`
	Tokens         []CompanyWalletTokenStatus `json:"tokens"`
	Error          string                     `json:"error,omitempty"`
}

// CompanyWalletStatus is the console's view of the wallet (§13 Q6).
type CompanyWalletStatus struct {
	Address       string                     `json:"address"`
	LowBalanceUSD float64                    `json:"lowBalanceUsd"`
	StallMinutes  int                        `json:"stallMinutes"`
	Chains        []CompanyWalletChainStatus `json:"chains"`
}

// CompanyWalletStatus reads balances fresh — an operator deciding to refill
// needs the chain's figure, not a cached one. Native balance and floor come
// from Treasury.FloatBalances (the gas monitor's reader), and token stock from
// freeTreasuryStock; no second balance loop. Provider error text is redacted.
func (s *ITService) CompanyWalletStatus(ctx context.Context, lowUSD float64) (*CompanyWalletStatus, error) {
	if !s.HasCompanyWallet() {
		return nil, ErrTreasuryUnavailable
	}
	w := s.wallet
	out := &CompanyWalletStatus{Address: w.Address(), LowBalanceUSD: lowUSD,
		StallMinutes: int(s.treasuryLimits().sendStall / time.Minute)}
	floats, floatErr := w.Treasury().FloatBalances(ctx)
	byChain := map[string]SponsorFloat{}
	for _, f := range floats {
		byChain[strings.ToLower(f.Chain)] = f
	}
	for _, chain := range w.Chains() {
		cs := CompanyWalletChainStatus{Chain: chain, Tokens: []CompanyWalletTokenStatus{}}
		if f, ok := byChain[chain]; ok {
			cs.NativeWei, cs.NativeFloorWei = f.BalanceWei.String(), f.FloorWei.String()
			cs.NativeLow = f.BalanceWei.Cmp(new(big.Int).Mul(f.FloorWei, big.NewInt(2))) < 0
		} else if floatErr != nil {
			// FloatBalances joins its per-chain errors; the text names the chain.
			cs.Error = RedactAlertText(floatErr.Error())
		}
		for _, tok := range w.Tokens(chain) {
			ts := CompanyWalletTokenStatus{Token: tok}
			free, balance, err := s.freeTreasuryStock(ctx, s.db, chain, tok, uuid.Nil, true)
			var reserved *big.Int
			if err == nil {
				reserved, err = s.reservedTreasuryUnits(ctx, s.db, chain, tok, uuid.Nil)
			}
			if err != nil {
				ts.Error = RedactAlertText(err.Error())
			} else {
				ts.Balance = FormatStableUnits(balance.String())
				ts.Reserved = FormatStableUnits(reserved.String())
				ts.Free = FormatStableUnits(free.String())
				ts.Low = StableFromBaseUnits(free) < lowUSD
			}
			cs.Tokens = append(cs.Tokens, ts)
		}
		out.Chains = append(out.Chains, cs)
	}
	return out, nil
}

// CompanyWalletOrder is one order an operator may need to act on.
type CompanyWalletOrder struct {
	ID            uuid.UUID `json:"id"`
	UserID        uuid.UUID `json:"userId"`
	Status        string    `json:"status"`
	Asset         string    `json:"asset"`
	Chain         string    `json:"chain"`
	DestAddress   string    `json:"destAddress"`
	AmountNgn     string    `json:"amountNgn"`
	ReceivedNgn   string    `json:"receivedNgn"`
	TokenAmount   string    `json:"tokenAmount"`
	FailureReason string    `json:"failureReason,omitempty"`
	Overpaid      bool      `json:"overpaid"`
	Stalled       bool      `json:"stalled"`
	SendTxHash    string    `json:"sendTxHash,omitempty"`
	// Evidence is the dropped-transaction check on a stalled send, exactly as
	// ResendTreasuryBuy would see it (review C1).
	Evidence  *ResendEvidence `json:"evidence,omitempty"`
	UpdatedAt time.Time       `json:"updatedAt"`
	Actions   []string        `json:"actions"`
}

// ListCompanyWalletOrders is the console queue: parked company-wallet buys
// (deliver anyway / refund), sends stalled past the window (re-send, only when
// the evidence allows), and deliveries with no send past the window (refund,
// only when secondPaymentGate agrees).
func (s *ITService) ListCompanyWalletOrders(ctx context.Context) ([]CompanyWalletOrder, error) {
	var rows []models.ITIntent
	if err := s.db.WithContext(ctx).
		Where("provider = ? AND direction = ? AND status IN ?", models.ITProviderTreasury, models.ITDirectionBuy,
			[]string{models.ITStatusBuyAwaitingRate, models.ITStatusDelivering}).
		Order("updated_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	stall := s.treasuryLimits().sendStall
	out := []CompanyWalletOrder{}
	for i := range rows {
		r := &rows[i]
		o := CompanyWalletOrder{
			ID: r.ID, UserID: r.UserID, Status: r.Status, Asset: r.Asset, Chain: strings.ToLower(r.DestChain),
			DestAddress: r.DestAddress, AmountNgn: FormatMinor(r.AmountMinor), ReceivedNgn: FormatMinor(r.ReceivedAmount),
			TokenAmount: FormatStableUnits(r.AssetAmount), FailureReason: r.FailureReason, UpdatedAt: r.UpdatedAt,
			Actions: []string{},
		}
		switch r.Status {
		case models.ITStatusBuyAwaitingRate:
			expected, okE := new(big.Int).SetString(strings.TrimSpace(r.AmountMinor), 10)
			paid, okP := new(big.Int).SetString(strings.TrimSpace(r.ReceivedAmount), 10)
			o.Overpaid = okE && okP && paid.Cmp(expected) > 0
			o.Actions = []string{"deliver", "refund"}
		case models.ITStatusDelivering:
			leg, err := s.liveSaleLeg(ctx, r.ID)
			switch {
			case err != nil:
				continue
			case leg == nil:
				// No send in flight: waiting for stock, or refused (kill switch,
				// gas). Past the window an operator may refund it (review C2).
				if time.Since(r.UpdatedAt) <= stall {
					continue
				}
				o.Stalled, o.Actions = true, []string{"refund"}
				if o.FailureReason == "" {
					o.FailureReason = "no send has gone out"
				}
				// Offered only when no earlier signed sale can still land: the
				// same gate MarkStableBuyRefunded decides on.
				reason, rerr := s.secondPaymentGate(ctx, s.db, r.ID, uuid.Nil)
				if rerr != nil {
					reason = "refund check could not run: " + RedactAlertText(rerr.Error())
				}
				if reason != "" {
					o.Actions, o.FailureReason = []string{}, reason
				}
			case leg.Status == models.RampLegStatusPending && time.Since(leg.CreatedAt) > stall:
				o.Stalled, o.SendTxHash = true, leg.TxHash
				ev, eerr := s.resendEvidence(ctx, r, leg)
				if eerr != nil {
					ev = &ResendEvidence{TxHash: leg.TxHash, Nonce: leg.Nonce,
						Reason: "the check could not run: " + RedactAlertText(eerr.Error())}
				}
				o.Evidence = ev
				if ev.Allowed {
					o.Actions = []string{"resend"}
				}
			default:
				continue // in flight inside the window, or confirmed: nothing to act on
			}
		}
		out = append(out, o)
	}
	return out, nil
}
