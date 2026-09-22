package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

var (
	ErrSponsorNotAvailable   = errors.New("gas sponsorship is not available for this order")
	ErrSponsorOrderState     = errors.New("this order is not waiting for a deposit")
	ErrSponsorWallet         = errors.New("this order belongs to a different wallet")
	ErrSponsorshipInProgress = errors.New("a sponsored transaction for this order is already being sent")
)

// sponsorChain is the read side of a chain. EVMRelayChain satisfies it.
type sponsorChain interface {
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	BalanceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error)
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
	ChainID() *big.Int
}

// sponsorTxBuilder builds a contract-call tx. EVMRelayTxBuilder satisfies it.
type sponsorTxBuilder interface {
	BuildUnsignedTx(from, to string, callData []byte) ([]byte, error)
	// BuildUnsignedCall is BuildUnsignedTx with an explicit value and gas
	// limit, for signing a LiFi route's transactionRequest as given. A zero
	// gasLimit means estimate.
	BuildUnsignedCall(from, to string, callData []byte, value *big.Int, gasLimit uint64) ([]byte, error)
}

// sponsorNativeSender is the rest of *blockchain.EVMClient a lane needs.
//
// The ERC-20 methods (GetTokenAllowance/PrepareERC20Approve/PrepareERC20Transfer)
// exist for the treasury lane, which reuses this exact interface — and this
// exact SponsorLane.Native field — rather than defining a second one:
// *blockchain.EVMClient already implements all three, so widening the
// interface costs nothing in production and only requires extending the one
// test fake (fakeSponsorNative).
type sponsorNativeSender interface {
	PrepareNativeTransfer(from, to string, amountWei *big.Int) ([]byte, error)
	EstimateGas(msg ethereum.CallMsg) (uint64, error)
	SuggestGasPrice() (*big.Int, error)
	WaitForReceiptContext(ctx context.Context, txHash string, timeout time.Duration) (*types.Receipt, error)
	GetTokenDecimals(tokenAddress string) (int, error)
	GetTokenAllowance(tokenAddress, owner, spender string) (*big.Int, error)
	PrepareERC20Approve(tokenAddress, owner, spender string, amount *big.Int) ([]byte, error)
	PrepareERC20Transfer(tokenAddress, from, to string, amount *big.Int) ([]byte, error)
}

type sponsorOrderReader interface {
	Get(ctx context.Context, userID, orderID uuid.UUID) (*models.PaycrestOrderRecord, error)
}

// SponsorLane is one sponsoring key on one chain.
type SponsorLane struct {
	ChainKey    string
	Chain       sponsorChain
	Builder     sponsorTxBuilder
	Native      sponsorNativeSender
	Signer      ReleaseSigner
	Guard       *SponsorGuard
	Tokens      map[string]string // SYMBOL -> contract address, from BlockchainService.TokenAddress
	TopUpMaxWei *big.Int          // refuse a single top-up above this

	// sendMu serialises nonce read -> broadcast. PendingNonceAt hands two
	// concurrent builds the same nonce; one then fails as "nonce too low"
	// after we already told the user it was sent. Same fix as
	// MCGPSwapService.signMu.
	sendMu sync.Mutex
}

// send builds and broadcasts one tx. broadcastAttempted separates a build
// failure (nothing left this process, safe to free the order's slot) from a
// broadcast failure, which is not proof of rejection: an RPC timeout can hide
// a tx the node accepted, so callers must keep the slot on that path. The one
// exception is a refusal the signer marks ErrTxNotBroadcast (the node said
// the sender cannot pay): that tx provably never left, so it is reported as
// not attempted.
func (l *SponsorLane) send(ctx context.Context, build func() ([]byte, error)) (txHash string, broadcastAttempted bool, err error) {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	unsigned, err := build()
	if err != nil {
		return "", false, err
	}
	txHash, err = l.Signer.SignAndBroadcast(ctx, unsigned)
	if errors.Is(err, ErrTxNotBroadcast) {
		return "", false, err
	}
	return txHash, true, err
}

func (l *SponsorLane) tokenAddress(symbol string) string {
	return l.Tokens[strings.ToUpper(strings.TrimSpace(symbol))]
}

// gasCost is what `units` of gas cost on this lane at the current price —
// the one "limit × SuggestGasPrice" every lane-side affordability check uses.
func (l *SponsorLane) gasCost(units uint64) (*big.Int, error) {
	price, err := l.Native.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("gas price on %s: %w", l.ChainKey, err)
	}
	return new(big.Int).Mul(new(big.Int).SetUint64(units), price), nil
}

// releaseSponsorshipRow releases a sponsorship reservation on a context that
// survives the caller's own cancellation/deadline. A request that fails
// because ITS ctx died (e.g. a slow simulate call past the request's
// deadline) must still be able to clean up the row it reserved — releasing
// on the dead ctx fails the DB delete silently and strands the once-per-order
// row as ErrSponsorshipInProgress forever. Shared with Task 5's top-up path.
func releaseSponsorshipRow(ctx context.Context, store sponsorshipStore, id uuid.UUID) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := store.Release(releaseCtx, id); err != nil {
		log.Printf("[ramp-sponsor] ALERT ramp_sponsor_release_failed_total=1 id=%s err=%v", id, err)
	}
}

// recordSponsorshipTx writes the broadcast tx hash on a context that survives
// the caller's own cancellation/deadline, for the same reason
// releaseSponsorshipRow does: the broadcast already happened by the time this
// runs, so a request ctx that dies between broadcast and this write must not
// leave the row stuck with an empty tx hash — that stalls the once-per-order
// slot as ErrSponsorshipInProgress forever and drops the top-up out of the
// daily-cap count.
func recordSponsorshipTx(ctx context.Context, store sponsorshipStore, id uuid.UUID, txHash, amountWei string) {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := store.SetTxHash(recordCtx, id, txHash, amountWei); err != nil {
		log.Printf("[ramp-sponsor] ALERT ramp_sponsor_record_failed_total=1 id=%s tx=%s err=%v", id, txHash, err)
	}
}

const sponsorTokenABI = `[
{"name":"transferWithAuthorization","type":"function","stateMutability":"nonpayable","inputs":[{"name":"from","type":"address"},{"name":"to","type":"address"},{"name":"value","type":"uint256"},{"name":"validAfter","type":"uint256"},{"name":"validBefore","type":"uint256"},{"name":"nonce","type":"bytes32"},{"name":"v","type":"uint8"},{"name":"r","type":"bytes32"},{"name":"s","type":"bytes32"}],"outputs":[]},
{"name":"authorizationState","type":"function","stateMutability":"view","inputs":[{"name":"authorizer","type":"address"},{"name":"nonce","type":"bytes32"}],"outputs":[{"name":"","type":"bool"}]},
{"name":"transfer","type":"function","stateMutability":"nonpayable","inputs":[{"name":"to","type":"address"},{"name":"value","type":"uint256"}],"outputs":[{"name":"","type":"bool"}]}
]`

var sponsorTokenABIParsed = func() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(sponsorTokenABI))
	if err != nil {
		panic("sponsor token ABI: " + err.Error())
	}
	return parsed
}()

// sponsorableOrder is the precondition every sponsored path shares. The order
// is loaded through Get(userID, orderID), so ownership is already enforced.
// Which mechanism applies is orderMode's call: it depends on the wallet too.
func sponsorableOrder(order *models.PaycrestOrderRecord, wallet string, now time.Time) error {
	if order.Direction != models.PaycrestDirectionSell || order.Status != models.PaycrestStatusAwaitingDeposit {
		return ErrSponsorOrderState
	}
	if order.ValidUntil == nil || !order.ValidUntil.After(now) || order.DepositAddress == "" || order.TokenAmount == "" {
		return ErrSponsorOrderState
	}
	if !strings.EqualFold(strings.TrimSpace(order.WalletAddress), strings.TrimSpace(wallet)) {
		return ErrSponsorWallet
	}
	return nil
}

// orderMode is how this order's deposit is sponsored. A wallet with code (an
// EIP-7702 delegation, or a contract) cannot use EIP-3009: USDC and USDT0 check
// its signature through ERC-1271, which the delegate rejects, so a relay would
// only simulate a revert. Such a wallet gets gas and sends the token itself.
func (l *SponsorLane) orderMode(ctx context.Context, order *models.PaycrestOrderRecord) SponsorMode {
	mode := RampSponsorMode(order.ChainKey, order.Token)
	if mode != SponsorModeAuthorization {
		return mode
	}
	code, err := l.Chain.CodeAt(ctx, common.HexToAddress(order.WalletAddress), nil)
	if err != nil {
		// Keep authorization: the relay simulates before broadcasting, so a
		// wrong guess costs a refused request, not gas.
		log.Printf("[ramp-sponsor] wallet code unreadable chain=%s wallet=%s err=%v", l.ChainKey, order.WalletAddress, err)
		return mode
	}
	if len(code) > 0 {
		return SponsorModeTopUp
	}
	return mode
}
