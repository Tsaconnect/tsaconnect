package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// service_contact_transfer.go — the contact fee as a plain token transfer to the core set's
// FeeConfig fee recipient (slice 5 of docs/plans/2026-10-27-contracts-v2-migration.md; spec D5).
// It replaces ServiceContactV2's payContactFee once the core set on the contact chain is ready;
// until then the contract path is unchanged. The amount is still the service_contact fee row.

// ErrContactTransferUnproven: the tx is mined but did not pay any of the caller's quoted intents.
var ErrContactTransferUnproven = errors.New("contact fee transfer does not match the quote")

// ContactChainReader reads a fee tx's receipt and its block on the contact chain; *ethclient.Client is one.
type ContactChainReader interface {
	ethLogReader
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
}

// contactQuoteSkew is how much earlier than its quote a transfer's block may be: block timestamps
// and our clock differ by seconds, and a quote's row is written just after the user saw it.
const contactQuoteSkew = 2 * time.Minute

// ServiceContactTransfer prepares and proves contact-fee transfers on one chain.
type ServiceContactTransfer struct {
	core     *CoreRegistry
	chainID  int64
	rpc      ContactChainReader
	verifier ERC20SourcedTransferVerifier
}

// NewServiceContactTransfer returns nil when no core set is selected. rpc reads receipts on chainID.
func NewServiceContactTransfer(core *CoreRegistry, chainID int64, rpc ContactChainReader) *ServiceContactTransfer {
	if core == nil || rpc == nil {
		return nil
	}
	return &ServiceContactTransfer{core: core, chainID: chainID, rpc: rpc, verifier: NewLogTransferVerifier(rpc)}
}

// Active reports whether contact fees are paid by transfer now: the core set on the contact chain
// has passed its gates. A set that falls out of service sends new contacts back to the contract.
func (t *ServiceContactTransfer) Active() bool {
	if t == nil {
		return false
	}
	_, ok := t.core.Ready(t.chainID)
	return ok
}

// ChainID is the chain contact fees are paid on.
func (t *ServiceContactTransfer) ChainID() int64 { return t.chainID }

// Recipient reads where the fee must go: the set's FeeConfig.feeRecipient().
func (t *ServiceContactTransfer) Recipient(ctx context.Context) (common.Address, error) {
	set, ok := t.core.Ready(t.chainID)
	if !ok {
		return common.Address{}, fmt.Errorf("chain %d: %w", t.chainID, ErrCoreSetNotReady)
	}
	r, err := t.core.Fees().FeeRecipient(ctx, t.chainID, set.FeeConfig)
	if err != nil {
		return common.Address{}, err
	}
	if r == (common.Address{}) {
		return common.Address{}, errors.New("FeeConfig has no fee recipient")
	}
	return r, nil
}

// Match returns the intent txHash paid: a successful tx, mined no earlier than the intent was
// quoted, whose Transfer logs moved at least the intent's amount of its token from its payer to its
// recipient. Binding the tx to the quote's time means an older transfer to the shared fee recipient
// can never be passed off as this payment. Not mined or unreadable comes back as an error to retry;
// a tx that is mined and pays none of the intents is ErrContactTransferUnproven.
func (t *ServiceContactTransfer) Match(ctx context.Context, txHash string, intents []models.ServiceContactIntent) (*models.ServiceContactIntent, error) {
	if len(intents) == 0 {
		return nil, fmt.Errorf("%w: no quote for this contact", ErrContactTransferUnproven)
	}
	receipt, err := t.rpc.TransactionReceipt(ctx, common.HexToHash(txHash))
	if err != nil {
		return nil, fmt.Errorf("read receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("%w: the tx reverted", ErrContactTransferUnproven)
	}
	header, err := t.rpc.HeaderByNumber(ctx, receipt.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("read block %s: %w", receipt.BlockNumber, err)
	}
	minedAt := time.Unix(int64(header.Time), 0)

	lastErr := fmt.Errorf("%w: no quote on chain %d", ErrContactTransferUnproven, t.chainID)
	for i := range intents {
		in := &intents[i]
		want, ok := new(big.Int).SetString(in.Amount, 10)
		if in.ChainID != t.chainID || !ok {
			continue
		}
		if minedAt.Before(in.CreatedAt.Add(-contactQuoteSkew)) {
			lastErr = fmt.Errorf("%w: the tx was mined before this contact was quoted", ErrContactTransferUnproven)
			continue
		}
		paid, err := t.verifier.VerifyTransferFrom(ctx, txHash, in.TokenAddress, in.PayerWallet, in.FeeRecipient)
		switch ClassifyTransfer(paid, err) {
		case TransferProven:
			if paid.Cmp(want) >= 0 {
				return in, nil
			}
			lastErr = fmt.Errorf("%w: paid %s, quoted %s", ErrContactTransferUnproven, paid, want)
		case TransferRefuted:
			return nil, fmt.Errorf("%w: %v", ErrContactTransferUnproven, err)
		default:
			if !errors.Is(err, ErrTransferNoMatch) {
				return nil, err // unreadable: ask again later
			}
			lastErr = fmt.Errorf("%w: %v", ErrContactTransferUnproven, err)
		}
	}
	return nil, lastErr
}
