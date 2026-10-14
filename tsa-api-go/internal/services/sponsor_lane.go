package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
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
	tokenDecimalsReader
	// GetTokenBalance exists for the treasury's kept-USDC float check
	// (Treasury.FloatBalances): the same widen-not-duplicate reasoning as the
	// ERC-20 methods above.
	GetTokenBalance(tokenAddress, walletAddress string) (*big.Int, error)
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

	// KeyLane is the key's one send lane on this chain, from the boot
	// SignerRegistry: the lock from nonce read to broadcast and the nonce
	// floor and fee gate, shared with every other role signing with this key on this chain
	// (PendingNonceAt hands two concurrent builds the same nonce; one then
	// fails as "nonce too low" after we already told the user it was sent).
	// Signer must be KeyLane.Signer() (a test may swap Signer for a fake).
	// Required: a SponsorLane without one refuses every send.
	KeyLane *KeyLane
}

// errNoKeyLane: a SponsorLane built without its key's registry lane never
// sends (boot always sets KeyLane; a lane outside the registry would hold a
// private nonce lock).
var errNoKeyLane = notBroadcast(errors.New("sponsor lane has no key lane from the signer registry"))

// callCancelled is the key lane's verdict (CallOutcome, re-proven on chain
// at the finality depth) that the transaction hash this sponsor lane sent
// never executed — its claim on a sponsorship may be given back. A read
// error is "not cancelled" (in flight).
func (l *SponsorLane) callCancelled(ctx context.Context, hash string) bool {
	if l == nil || l.KeyLane == nil || hash == "" {
		return false
	}
	c, err := l.KeyLane.CallCancelled(ctx, hash)
	return err == nil && c
}

// nonceLane is the lane every send on this SponsorLane goes through (nil
// without one; KeyLane's methods are nil-safe where a quote asks).
func (l *SponsorLane) nonceLane() *KeyLane { return l.KeyLane }

// send builds and broadcasts one tx on the key's lane (lock + nonce floor).
// broadcastAttempted separates a build failure (nothing left this process,
// safe to free the order's slot) from a broadcast failure, which is not proof
// of rejection: an RPC timeout can hide a tx the node accepted, so callers
// must keep the slot on that path. The one exception is a refusal the signer
// marks ErrTxNotBroadcast (e.g. the node said the sender cannot pay): that tx
// provably never left, so it is reported as not attempted.
func (l *SponsorLane) send(ctx context.Context, build func() ([]byte, error)) (txHash string, broadcastAttempted bool, err error) {
	return l.sendRecorded(ctx, build, nil)
}

// sendRecorded is send with hook as the pre-broadcast write, inside the key
// lane's allocation transaction (KeyLane.SendRecorded). A nil hook keeps one
// the caller attached to ctx.
func (l *SponsorLane) sendRecorded(ctx context.Context, build func() ([]byte, error), hook LaneHook) (string, bool, error) {
	if l.KeyLane == nil {
		return "", false, errNoKeyLane
	}
	if hook != nil {
		ctx = WithPreBroadcast(ctx, hook)
	}
	return l.KeyLane.sendWith(ctx, l.Signer, build)
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
