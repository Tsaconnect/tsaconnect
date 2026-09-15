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
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

var (
	ErrKYCRequired       = errors.New("identity verification is required for gas sponsorship")
	ErrTopUpUnusedCap    = errors.New("too many gas top-ups went unused today; complete a sale or try again tomorrow")
	ErrTopUpTooExpensive = errors.New("network gas is too expensive to sponsor right now")
	ErrTopUpUnconfirmed  = errors.New("gas is on its way but not confirmed yet; try again in a moment")
	ErrTopUpFailed       = errors.New("gas top-up failed on-chain")
)

const (
	topUpGasUnits = 21_000
	// topUpFallbackTransferGas covers an ERC-20 transfer when estimation fails
	// (BSC USDT measures ~50k; a first transfer to a fresh address costs more).
	topUpFallbackTransferGas = 80_000
	// topUpGasBuffer multiplies today's transfer cost. The app prices its own
	// send seconds later (prepare-tx re-reads the gas price and estimate), and a
	// 1.5x buffer left only ~1.37x of that measured cost in a fork rehearsal.
	// 2x costs a fraction of a cent and survives a price move twice as large.
	topUpGasBuffer = 2
)

type GasTopUpResult struct {
	TxHash    string `json:"txHash,omitempty"`
	AmountWei string `json:"amountWei,omitempty"`
	Skipped   bool   `json:"skipped"`
}

// GasTopUp sends a user just enough native gas to make their own deposit
// transfer, for tokens with no EIP-3009 (BSC USDT/USDC, Polygon USDT) and for
// wallets with code, which cannot use it on any token (see orderMode). Worth
// ~$0.003 on these chains, so the controls are about scripted farming, not the
// unit cost: KYC, once per open order, and a cap on top-ups that went unused.
type GasTopUp struct {
	orders         sponsorOrderReader
	store          sponsorshipStore
	lanes          map[string]*SponsorLane
	unusedCap      int
	receiptTimeout time.Duration
	now            func() time.Time
}

func NewGasTopUp(orders sponsorOrderReader, store sponsorshipStore, lanes map[string]*SponsorLane, unusedCap int, receiptTimeout time.Duration, now func() time.Time) *GasTopUp {
	if now == nil {
		now = time.Now
	}
	if unusedCap <= 0 {
		unusedCap = 2
	}
	if receiptTimeout <= 0 {
		receiptTimeout = 30 * time.Second
	}
	return &GasTopUp{orders: orders, store: store, lanes: lanes, unusedCap: unusedCap, receiptTimeout: receiptTimeout, now: now}
}

func (g *GasTopUp) TopUp(ctx context.Context, user *models.User, orderID uuid.UUID) (GasTopUpResult, error) {
	if user == nil {
		return GasTopUpResult{}, ErrSponsorWallet
	}
	if user.VerificationStatus != models.VerificationStatusVerified {
		return GasTopUpResult{}, ErrKYCRequired
	}
	now := g.now()
	order, err := g.orders.Get(ctx, user.ID, orderID)
	if err != nil {
		return GasTopUpResult{}, err
	}
	if err := sponsorableOrder(order, user.WalletAddress, now); err != nil {
		return GasTopUpResult{}, err
	}
	lane := g.lanes[strings.ToLower(order.ChainKey)]
	if lane == nil || lane.Guard.Killed() || lane.tokenAddress(order.Token) == "" || lane.orderMode(ctx, order) != SponsorModeTopUp {
		return GasTopUpResult{}, ErrSponsorNotAvailable
	}

	row := &models.GasSponsorship{
		UserID: user.ID, OrderID: orderID, Kind: models.GasSponsorshipKindTopUp,
		ChainKey: lane.ChainKey, Token: strings.ToUpper(order.Token), Wallet: order.WalletAddress,
	}
	existing, err := g.store.Reserve(ctx, row)
	if err != nil {
		return GasTopUpResult{}, fmt.Errorf("sponsor: reserve: %w", err)
	}
	if existing != nil {
		if existing.TxHash == "" {
			// A broadcast whose outcome was unknown keeps this row with no hash,
			// but that top-up usually landed. If the gas is there, let the user
			// make their own transfer rather than dead-ending the sale. need
			// carries topUpGasBuffer, so compare against the unbuffered cost:
			// the price may have moved since the top-up. The row stays put so a
			// retry still cannot send a second top-up.
			if need, nerr := g.gasNeeded(lane, order); nerr == nil {
				have, berr := lane.Chain.BalanceAt(ctx, common.HexToAddress(order.WalletAddress), nil)
				if berr == nil && new(big.Int).Mul(have, big.NewInt(topUpGasBuffer)).Cmp(need) >= 0 {
					return GasTopUpResult{Skipped: true}, nil
				}
			}
			return GasTopUpResult{}, ErrSponsorshipInProgress
		}
		prior := GasTopUpResult{TxHash: existing.TxHash, AmountWei: existing.AmountWei}
		return prior, g.confirm(ctx, lane, prior.TxHash)
	}
	releaseRow := func() { releaseSponsorshipRow(ctx, g.store, row.ID) }

	need, err := g.gasNeeded(lane, order)
	if err != nil {
		releaseRow()
		return GasTopUpResult{}, err
	}
	userAddr := common.HexToAddress(order.WalletAddress)
	have, err := lane.Chain.BalanceAt(ctx, userAddr, nil)
	if err != nil {
		releaseRow()
		return GasTopUpResult{}, fmt.Errorf("sponsor: read user balance: %w", err)
	}
	if have.Cmp(need) >= 0 {
		releaseRow()
		return GasTopUpResult{Skipped: true}, nil
	}
	amount := new(big.Int).Sub(need, have)
	if lane.TopUpMaxWei != nil && amount.Cmp(lane.TopUpMaxWei) > 0 {
		releaseRow()
		log.Printf("[ramp-sponsor] ALERT ramp_topup_too_expensive_total=1 chain=%s amount=%s max=%s", lane.ChainKey, amount, lane.TopUpMaxWei)
		return GasTopUpResult{}, ErrTopUpTooExpensive
	}

	// The unused cap only applies when gas would actually be sent. Checked after
	// the balance read so a capped user who already holds enough gas still gets
	// Skipped and can make their own transfer, rather than losing the sale.
	// This order is still awaiting deposit, so it never counts toward the cap;
	// a user holds one open sell order at a time, so at most one of their
	// top-ups is ever undecided.
	unused, err := g.store.CountUnusedTopUpsSince(ctx, user.ID, now.Add(-24*time.Hour))
	if err != nil {
		releaseRow()
		return GasTopUpResult{}, fmt.Errorf("sponsor: count unused top-ups: %w", err)
	}
	if unused >= int64(g.unusedCap) {
		releaseRow()
		return GasTopUpResult{}, ErrTopUpUnusedCap
	}

	sponsorBal, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(lane.Signer.Address()), nil)
	if err != nil {
		releaseRow()
		return GasTopUpResult{}, fmt.Errorf("sponsor: read sponsor balance: %w", err)
	}
	if err := lane.Guard.CheckFloor(sponsorBal); err != nil {
		releaseRow()
		return GasTopUpResult{}, err
	}
	gas, err := lane.Guard.ReserveGas(topUpGasUnits)
	if err != nil {
		releaseRow()
		return GasTopUpResult{}, err
	}

	txHash, broadcastAttempted, err := lane.send(ctx, func() ([]byte, error) {
		return lane.Native.PrepareNativeTransfer(lane.Signer.Address(), order.WalletAddress, amount)
	})
	if err != nil {
		if broadcastAttempted {
			// The top-up may have landed; keep the row so a retry cannot send
			// the user a second one.
			log.Printf("[ramp-sponsor] ALERT ramp_sponsor_broadcast_uncertain_total=1 order=%s chain=%s err=%v", orderID, lane.ChainKey, err)
			return GasTopUpResult{}, fmt.Errorf("%w: broadcast outcome unknown: %v", ErrSponsorshipInProgress, err)
		}
		lane.Guard.ReleaseGas(gas)
		releaseRow()
		return GasTopUpResult{}, fmt.Errorf("sponsor: broadcast top-up: %w", err)
	}
	recordSponsorshipTx(ctx, g.store, row.ID, txHash, amount.String())
	log.Printf("[ramp-sponsor] topped up order=%s chain=%s wallet=%s wei=%s tx=%s", orderID, lane.ChainKey, order.WalletAddress, amount, txHash)

	result := GasTopUpResult{TxHash: txHash, AmountWei: amount.String()}
	return result, g.confirm(ctx, lane, txHash)
}

// topUpUnused reports whether a top-up's gas was never used for a deposit: the
// sell order it was sent for ended expired, cancelled or failed with nothing
// paid. Any other outcome, or any amount paid, means the deposit happened.
// Statuses that are not yet decided (awaiting_deposit) are not unused.
//
// The order's outcome decides this rather than the wallet's history: it is
// authoritative and already in our DB, where reading what the wallet did with
// the gas would need an indexer per chain and is easy to disguise.
func topUpUnused(status, amountPaid string) bool {
	switch status {
	case models.PaycrestStatusExpired, models.PaycrestStatusCancelled, models.PaycrestStatusFailed:
	default:
		return false
	}
	paid, ok := new(big.Rat).SetString(strings.TrimSpace(amountPaid))
	return !ok || paid.Sign() == 0
}

// gasNeeded is the user's deposit transfer at today's price, times
// topUpGasBuffer so a price move between top-up and send does not strand them
// short: a retry reuses this order's top-up, so there is no second chance.
func (g *GasTopUp) gasNeeded(lane *SponsorLane, order *models.PaycrestOrderRecord) (*big.Int, error) {
	token := lane.tokenAddress(order.Token)
	decimals, err := lane.Native.GetTokenDecimals(token)
	if err != nil {
		return nil, fmt.Errorf("sponsor: token decimals: %w", err)
	}
	value, err := DecimalToBaseUnits(order.TokenAmount, decimals)
	if err != nil {
		return nil, fmt.Errorf("sponsor: order amount: %w", err)
	}
	data, err := sponsorTokenABIParsed.Pack("transfer", common.HexToAddress(order.DepositAddress), value)
	if err != nil {
		return nil, err
	}
	tokenAddr := common.HexToAddress(token)
	limit, err := lane.Native.EstimateGas(ethereum.CallMsg{From: common.HexToAddress(order.WalletAddress), To: &tokenAddr, Data: data})
	if err != nil || limit == 0 {
		limit = topUpFallbackTransferGas
	}
	price, err := lane.Native.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("sponsor: gas price: %w", err)
	}
	need := new(big.Int).Mul(new(big.Int).SetUint64(limit), price)
	return need.Mul(need, big.NewInt(topUpGasBuffer)), nil
}

// confirm waits for the top-up to land, so the app's own send that follows is
// not priced against a balance that has not arrived yet.
func (g *GasTopUp) confirm(ctx context.Context, lane *SponsorLane, txHash string) error {
	receipt, err := lane.Native.WaitForReceiptContext(ctx, txHash, g.receiptTimeout)
	if err != nil {
		return ErrTopUpUnconfirmed
	}
	if receipt.Status != 1 {
		log.Printf("[ramp-sponsor] ALERT ramp_topup_reverted_total=1 chain=%s tx=%s", lane.ChainKey, txHash)
		return ErrTopUpFailed
	}
	return nil
}
