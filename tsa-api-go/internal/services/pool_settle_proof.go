package services

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// topicTradeReleased is P2PEscrowV3's TradeReleased(bytes32 indexed tradeId,
// address indexed buyer, uint256, uint256) (P2PEscrowV3.sol:215). Hashed
// here, not read from parsedP2PEscrowABI: that is parsed in init(), after
// package variables.
var topicTradeReleased = crypto.Keccak256Hash([]byte("TradeReleased(bytes32,address,uint256,uint256)"))

// proveSliceRelease is §12.4.3 (plan C2 P3): the slice's USDT reached the
// conversion treasury, proven on its own receipt need blocks deep and on one
// RPC endpoint — never by a balance (R3: the lane holds other orders' money).
func (d *PoolDesk) proveSliceRelease(ctx context.Context, sl *models.P2PTrade, need uint64) (bool, error) {
	gen := d.c.Probe.EndpointGeneration()
	t, err := d.c.Escrow.ReadTrade(HashID(sl.ID))
	if err != nil {
		return false, err
	}
	switch {
	case t.Status == OnChainP2PStatusReleased:
	case t.Status == OnChainP2PStatusResolved && sl.DroppedAt == nil: // F21 to the treasury
	default:
		return false, nil
	}
	hash := sl.ReleaseTxHash
	if !d.hashMovesSlice(ctx, sl, hash, t.Status) {
		if t.Status != OnChainP2PStatusReleased {
			return false, nil // a resolve we did not send: reconcileSliceResolved alerts it
		}
		found, err := d.findReleaseTx(ctx, sl)
		if err != nil || found == "" {
			return false, err
		}
		// The chain says Released and its own log names the transaction: the
		// recorded value (none, "reconciled", or an attempt that did not
		// release it) is replaced only if no one changed it meanwhile.
		res := d.c.DB.WithContext(ctx).Model(&models.P2PTrade{}).Where("id = ? AND release_tx_hash = ?", sl.ID, sl.ReleaseTxHash).
			Update("release_tx_hash", found)
		if res.Error != nil || res.RowsAffected != 1 {
			return false, res.Error // changed under us: the next tick re-reads it
		}
		hash = found
		sl.ReleaseTxHash = hash
	}
	deep, err := receiptDeep(ctx, d.c.Probe, hash, types.ReceiptStatusSuccessful, need, gen)
	if err != nil || !deep {
		return false, err
	}
	units, ok := new(big.Int).SetString(sl.TokenAmount, 10)
	if !ok {
		return false, fmt.Errorf("liquidity pool: slice %s has unusable amount %q", sl.ID, sl.TokenAmount)
	}
	got, err := d.c.Verifier.VerifyTransferFrom(ctx, hash, d.c.Token.Address, d.c.EscrowAddr, d.c.Treasury.Address())
	if err != nil || got == nil || got.Cmp(units) < 0 {
		return false, nil // not (yet) a transfer of the whole slice to the treasury
	}
	return endpointSteady(d.c.Probe, gen) == nil, nil
}

// hashMovesSlice: hash's receipt carries this slice's release (or F21's
// resolve to the treasury). One non-blocking receipt read parsed with the
// escrow's own pure parsers — never VerifyTrade*, which waits up to 30 s for
// a receipt (review I-8).
func (d *PoolDesk) hashMovesSlice(ctx context.Context, sl *models.P2PTrade, hash string, st OnChainP2PTradeStatus) bool {
	if hash == "" || hash == "reconciled" {
		return false
	}
	r, err := d.c.Probe.TransactionReceipt(ctx, common.HexToHash(hash))
	if err != nil || r == nil || r.Status != types.ReceiptStatusSuccessful {
		return false
	}
	escrow := common.HexToAddress(d.c.EscrowAddr)
	if st == OnChainP2PStatusResolved {
		ev, err := parseTradeResolvedFromReceipt(r, escrow, HashID(sl.ID))
		return err == nil && ev.ReleasedToBuyer
	}
	_, err = parseTradeReleasedFromReceipt(r, escrow, HashID(sl.ID))
	return err == nil
}

// findReleaseTx finds a slice's release transaction from the escrow's
// TradeReleased(tradeId) log, scanned in LogChunk windows from the open's
// block (an LP self-release, F22, or a release whose recorded attempt was
// superseded). "" means not found — never a proof.
func (d *PoolDesk) findReleaseTx(ctx context.Context, sl *models.P2PTrade) (string, error) {
	if sl.OpenTxHash == "" {
		return "", nil
	}
	r, err := d.c.Probe.TransactionReceipt(ctx, common.HexToHash(sl.OpenTxHash))
	if err != nil || r == nil || r.BlockNumber == nil {
		return "", err
	}
	head, err := d.c.Probe.BlockNumber(ctx)
	if err != nil {
		return "", err
	}
	id := HashID(sl.ID)
	q := ethereum.FilterQuery{Addresses: []common.Address{common.HexToAddress(d.c.EscrowAddr)},
		Topics: [][]common.Hash{{topicTradeReleased}, {common.BytesToHash(id[:])}}}
	sc := newLogScanner(d.c.LogChunk)
	found := ""
	err = sc.scanLogs(ctx, d.c.Probe, q, r.BlockNumber.Uint64(), head, saleScanCallTimeout, func(logs []types.Log, _ uint64) error {
		for _, lg := range logs {
			if !lg.Removed {
				found = lg.TxHash.Hex()
			}
		}
		return nil
	})
	return found, err
}
