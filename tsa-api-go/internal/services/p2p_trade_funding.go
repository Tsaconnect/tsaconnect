package services

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// tradeFunding is which side funds a trade's escrow, and so who opens it.
//
//   - pooled (sell-ad, openTrade): the maker's voucher pool funds it; the buyer
//     opens it, so on chain buyer = opener and seller = the ad's wallet.
//   - JIT (buy-ad, openTradeWithDeposit): the taker deposits at open; she is
//     the trade's seller, so on chain seller = opener and buyer = the ad's
//     wallet (the merchant).
type tradeFunding uint8

const (
	fundingPooled tradeFunding = tradeFunding(models.P2PTradeFundingPooled)
	fundingJIT    tradeFunding = tradeFunding(models.P2PTradeFundingDepositedByTaker)
)

func fundingOf(t *models.P2PTrade) tradeFunding { return tradeFunding(t.Funding) }

// fundingForAd is the funding a trade opened on ad must have.
func fundingForAd(ad *models.P2PAd) tradeFunding {
	if ad.Side == models.P2PAdSideBuy {
		return fundingJIT
	}
	return fundingPooled
}

func (f tradeFunding) JIT() bool { return f == fundingJIT }

// OpenerUserID is the party who funds and broadcasts the open.
func (f tradeFunding) OpenerUserID(t *models.P2PTrade) uuid.UUID {
	if f.JIT() {
		return t.SellerID
	}
	return t.BuyerID
}

// OnChainOpener is the opener's wallet as the escrow records it (msg.sender).
func (f tradeFunding) OnChainOpener(oc *OnChainP2PTrade) common.Address {
	if f.JIT() {
		return oc.Seller
	}
	return oc.Buyer
}

// OnChainCounterparty is the ad's wallet as the escrow records it.
func (f tradeFunding) OnChainCounterparty(oc *OnChainP2PTrade) common.Address {
	if f.JIT() {
		return oc.Buyer
	}
	return oc.Seller
}

// verifyOpen checks txHash emitted this funding's open event for tradeID
// (TradeOpenedWithDeposit for JIT, TradeOpened for pooled).
func (f tradeFunding) verifyOpen(escrow P2PEscrowAdapter, txHash string, tradeID [32]byte) error {
	if f.JIT() {
		if _, err := escrow.VerifyTradeOpenedWithDeposit(txHash, tradeID); err != nil {
			return fmt.Errorf("verify on-chain open (deposit): %w", err)
		}
		return nil
	}
	if _, err := escrow.VerifyTradeOpened(txHash, tradeID); err != nil {
		return fmt.Errorf("verify on-chain open: %w", err)
	}
	return nil
}
