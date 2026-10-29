package services

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// p2p_trade_key.go — which escrow a trade or ad lives on and the key the escrow stores a trade
// under (slice 8 of the contracts v2 migration: P2PTradeEscrow).

// ErrRetiredEscrow is returned for a trade or sell-ad recorded on an escrow other than its chain's
// current one (the V3 escrows, drained before the core set took over; boot refuses while any is
// live). A sell-ad's voucher is signed for one escrow: such an ad must be published again.
var ErrRetiredEscrow = errors.New("p2p: this trade or ad is on a retired escrow")

// newTradeKeyHex is TradeKey(opener, HashID(id)) as stored in p2p_trades.on_chain_trade_id.
func newTradeKeyHex(opener string, id uuid.UUID) string {
	key := TradeKey(common.HexToAddress(opener), HashID(id))
	return hexutil.Encode(key[:])
}

// tradeKeyOf is the key the escrow stores t under (written at prepare as on_chain_trade_id).
func tradeKeyOf(t *models.P2PTrade) ([32]byte, error) {
	var key [32]byte
	raw, err := hexutil.Decode(t.OnChainTradeID)
	if err != nil || len(raw) != 32 {
		return key, fmt.Errorf("%w: trade %s has no escrow key", ErrInvalidTransition, t.ID)
	}
	copy(key[:], raw)
	return key, nil
}

// onEscrow reports whether a row recorded on `recorded` lives on `escrow`.
func onEscrow(recorded string, escrow P2PEscrowAdapter) bool {
	return recorded != "" && strings.EqualFold(recorded, escrow.ContractAddress())
}

// tradeEscrow is the escrow adapter and key for t: its chain's current escrow, which must be the
// one t was opened on.
func (s *P2PTradeService) tradeEscrow(t *models.P2PTrade) (P2PEscrowAdapter, [32]byte, error) {
	escrow, _, _, err := s.resolve(t.ChainID)
	if err != nil {
		return nil, [32]byte{}, err
	}
	if !onEscrow(t.EscrowAddress, escrow) {
		return nil, [32]byte{}, fmt.Errorf("%w: trade %s is on %q, chain %d runs %s", ErrRetiredEscrow, t.ID, t.EscrowAddress, t.ChainID, escrow.ContractAddress())
	}
	key, err := tradeKeyOf(t)
	if err != nil {
		return nil, [32]byte{}, err
	}
	return escrow, key, nil
}

// adEscrow is the escrow adapter and EIP-712 domain for ad's chain. A sell-ad's voucher must be
// signed for that escrow; a buy-ad has no voucher, so its trades open on whatever escrow is current.
func (s *P2PTradeService) adEscrow(ad *models.P2PAd) (P2PEscrowAdapter, int64, string, error) {
	escrow, domainChainID, version, err := s.resolve(ad.ChainID)
	if err != nil {
		return nil, 0, "", err
	}
	if ad.Side != models.P2PAdSideBuy && !onEscrow(ad.EscrowAddress, escrow) {
		return nil, 0, "", fmt.Errorf("%w: ad %s was signed for %q, chain %d runs %s", ErrRetiredEscrow, ad.ID, ad.EscrowAddress, ad.ChainID, escrow.ContractAddress())
	}
	return escrow, domainChainID, version, nil
}
