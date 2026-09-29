package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// OTC sell cap (owner rule 2026-09-28): wallet ERC-20 MCGP is not usable in
// the app, except that a wallet may sell back on the OTC desk what it bought
// on the OTC desk. The contract keeps no per-user accounting, so the cap is
// rebuilt from its own Bought/Sold logs:
//
//	sellable = min(wallet MCGP balance, max(0, Σ Bought − Σ Sold))
//
// Keyed per WALLET, not per user: the tokens a sell moves leave the signing
// wallet, so only that wallet's own purchases can justify them. Pooling a
// user's wallets would let MCGP that arrived in wallet B from anywhere be sold
// against a purchase wallet A made.
//
// Sold counts in full, including sells made before this rule of MCGP that was
// never bought here — those already used the desk's USDC, so they use up the
// allowance too, and the net is floored at zero rather than going negative.

// ErrOTCPositionUnavailable means the desk's history or the wallet's balance
// could not be read. A caller must refuse the sell, never assume a cap.
var ErrOTCPositionUnavailable = errors.New("OTC position unavailable")

const (
	// otcLogOverlapBlocks is how far behind head logs stay provisional. They
	// are re-read on every refresh rather than committed, so a load-balanced
	// RPC whose nodes disagree about the newest blocks cannot make us skip one
	// for good. Sonic finalises in about a second; this is a minute or more.
	otcLogOverlapBlocks uint64 = 128
)

// Time bounds (vars so tests can shorten them).
var (
	// otcPositionBudget bounds one Position call — waiting for the index lock
	// plus any refresh — so an OTC request answers "unavailable" instead of
	// hanging behind a slow RPC or the boot warm-up.
	otcPositionBudget = 15 * time.Second
	// otcRPCCallTimeout bounds a single eth_blockNumber / eth_getLogs call; a
	// timed-out getLogs is treated like any refusal and the span halves.
	otcRPCCallTimeout = 10 * time.Second
	// otcWarmSlice bounds one warm-up pass. Warm releases the lock between
	// passes so requests can interleave with a long catch-up.
	otcWarmSlice = 30 * time.Second
)

// OTCPosition is what one wallet may sell back on the OTC desk. Amounts are
// MCGP wei (18 decimals).
type OTCPosition struct {
	Wallet        string
	Bought        *big.Int
	Sold          *big.Int
	NetPurchased  *big.Int
	WalletBalance *big.Int
	Sellable      *big.Int
	// AsOfBlock is the newest block whose logs are counted.
	AsOfBlock uint64
}

// ComputeOTCSellable applies the sell-cap rule to raw totals. nil reads as 0.
func ComputeOTCSellable(bought, sold, balance *big.Int) (netPurchased, sellable *big.Int) {
	netPurchased = new(big.Int).Sub(otcAmount(bought), otcAmount(sold))
	if netPurchased.Sign() < 0 {
		netPurchased.SetInt64(0)
	}
	sellable = new(big.Int).Set(netPurchased)
	if b := otcAmount(balance); b.Cmp(sellable) < 0 {
		sellable.Set(b)
	}
	if sellable.Sign() < 0 {
		sellable.SetInt64(0)
	}
	return netPurchased, sellable
}

func otcAmount(v *big.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v
}

// OTCLogSource is the slice of an Ethereum RPC client the scanner needs.
// *ethclient.Client (EVMClient.Client) satisfies it.
type OTCLogSource interface {
	BlockNumber(ctx context.Context) (uint64, error)
	logFilterer
}

// OTCTokenBalanceReader reads an ERC-20 balance. *blockchain.EVMClient
// satisfies it.
type OTCTokenBalanceReader interface {
	GetTokenBalance(tokenAddress, walletAddress string) (*big.Int, error)
}

type otcTotals struct {
	bought *big.Int
	sold   *big.Int
}

type otcDelta struct {
	wallet common.Address
	amount *big.Int
	bought bool
}

// OTCPositionService keeps a running index of every Bought/Sold log the desk
// has emitted and answers per-wallet positions from it.
//
// One contract-wide index rather than a per-address scan: the desk emits a
// handful of events, so the whole history is small, and an RPC with a
// block-range cap then pays the long catch-up once per process instead of
// once per user. After catch-up a refresh reads only the blocks since the
// last one. Progress is committed chunk by chunk, so a request that times out
// mid catch-up still leaves the next one less to do.
type OTCPositionService struct {
	logs      OTCLogSource
	balances  OTCTokenBalanceReader
	contract  common.Address
	mcgpToken string
	now       func() time.Time

	// lock is a one-slot semaphore rather than a mutex so a waiting request
	// gives up with its own context instead of queueing behind a long scan.
	lock chan struct{}

	// Guarded by lock.
	logScanner        // eth_getLogs chunking, kept across refreshes
	next       uint64 // first block not yet committed
	committed  map[common.Address]*otcTotals
	// tail holds provisional logs for blocks [next, head]. It is only valid
	// while refreshedAt is set: a refresh that fails part-way clears both,
	// because committed may have advanced into blocks tail still counts.
	tail        map[common.Address]*otcTotals
	head        uint64
	refreshedAt time.Time
}

// NewOTCPositionService builds the index for the OTC contract. Nothing is read
// until the first Position or Warm call.
func NewOTCPositionService(logs OTCLogSource, balances OTCTokenBalanceReader, contractAddress, mcgpToken string, deployBlock, maxBlockRange uint64) *OTCPositionService {
	return &OTCPositionService{
		logs:       logs,
		balances:   balances,
		contract:   common.HexToAddress(contractAddress),
		mcgpToken:  strings.TrimSpace(mcgpToken),
		now:        time.Now,
		lock:       make(chan struct{}, 1),
		logScanner: newLogScanner(maxBlockRange),
		next:       deployBlock,
		committed:  map[common.Address]*otcTotals{},
		tail:       map[common.Address]*otcTotals{},
	}
}

func (s *OTCPositionService) configured() bool {
	return s != nil && s.logs != nil && s.balances != nil && s.mcgpToken != "" && s.contract != (common.Address{})
}

// Warm runs the catch-up scan ahead of the first request. It works in
// slices of otcWarmSlice, releasing the lock between them so a request can
// get in, and keeps going while each slice commits progress. Committed
// progress survives a failed slice, so the next one (or a request) resumes.
func (s *OTCPositionService) Warm(ctx context.Context) error {
	if !s.configured() {
		return fmt.Errorf("%w: not configured", ErrOTCPositionUnavailable)
	}
	for {
		if err := s.acquire(ctx); err != nil {
			return err
		}
		before := s.next
		sliceCtx, cancel := context.WithTimeout(ctx, otcWarmSlice)
		err := s.refresh(sliceCtx)
		cancel()
		progressed := s.next > before
		s.release()
		if err == nil || ctx.Err() != nil || !progressed {
			return err
		}
	}
}

// Position returns what wallet may sell back on the desk. The index is
// refreshed first when it is older than maxAge; pass 0 to always refresh,
// which is what enforcing a sell must do. Any read failure returns an error
// wrapping ErrOTCPositionUnavailable.
func (s *OTCPositionService) Position(ctx context.Context, wallet string, maxAge time.Duration) (*OTCPosition, error) {
	if !s.configured() {
		return nil, fmt.Errorf("%w: not configured", ErrOTCPositionUnavailable)
	}
	if !common.IsHexAddress(wallet) {
		return nil, fmt.Errorf("invalid wallet address %q", wallet)
	}
	addr := common.HexToAddress(wallet)

	ctx, cancel := context.WithTimeout(ctx, otcPositionBudget)
	defer cancel()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	if s.refreshedAt.IsZero() || s.now().Sub(s.refreshedAt) > maxAge {
		if err := s.refresh(ctx); err != nil {
			s.release()
			return nil, err
		}
	}
	bought, sold := new(big.Int), new(big.Int)
	for _, m := range []map[common.Address]*otcTotals{s.committed, s.tail} {
		if t := m[addr]; t != nil {
			bought.Add(bought, t.bought)
			sold.Add(sold, t.sold)
		}
	}
	asOf := s.head
	s.release()

	balance, err := s.balances.GetTokenBalance(s.mcgpToken, addr.Hex())
	if err != nil {
		return nil, fmt.Errorf("%w: MCGP balance: %v", ErrOTCPositionUnavailable, err)
	}
	net, sellable := ComputeOTCSellable(bought, sold, balance)
	return &OTCPosition{
		Wallet:        addr.Hex(),
		Bought:        bought,
		Sold:          sold,
		NetPurchased:  net,
		WalletBalance: balance,
		Sellable:      sellable,
		AsOfBlock:     asOf,
	}, nil
}

func (s *OTCPositionService) acquire(ctx context.Context) error {
	select {
	case s.lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrOTCPositionUnavailable, ctx.Err())
	}
}

func (s *OTCPositionService) release() { <-s.lock }

// refresh brings the index up to the chain head. Caller holds lock.
//
// Committed progress is kept even when a later step fails (each chunk is
// applied whole), but the provisional tail is not: it covered blocks from the
// OLD next, which committed may now include, so reading committed+tail would
// count those logs twice. On any error the tail and refreshedAt are cleared,
// so nothing is read until a refresh rebuilds the tail from the new next.
func (s *OTCPositionService) refresh(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			s.tail = map[common.Address]*otcTotals{}
			s.refreshedAt = time.Time{}
		}
	}()

	callCtx, cancel := context.WithTimeout(ctx, otcRPCCallTimeout)
	head, err := s.logs.BlockNumber(callCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("%w: block number: %v", ErrOTCPositionUnavailable, err)
	}
	if head <= s.head && !s.refreshedAt.IsZero() {
		// Nothing new, or a lagging node reporting an older head, and the
		// tail is intact. Never step backwards: that would drop provisional
		// logs already counted.
		s.refreshedAt = s.now()
		return nil
	}
	if head < s.head {
		// The tail must be rebuilt, and this node has not reached blocks
		// already seen: rebuilding to its head would drop their logs.
		return fmt.Errorf("%w: RPC head %d is behind the indexed head %d", ErrOTCPositionUnavailable, head, s.head)
	}

	var safe uint64
	if head > otcLogOverlapBlocks {
		safe = head - otcLogOverlapBlocks
	}
	if safe >= s.next {
		err := s.scan(ctx, s.next, safe, func(deltas []otcDelta, chunkEnd uint64) {
			applyOTCDeltas(s.committed, deltas)
			s.next = chunkEnd + 1
		})
		if err != nil {
			return err
		}
	}

	tail := map[common.Address]*otcTotals{}
	if head >= s.next {
		err := s.scan(ctx, s.next, head, func(deltas []otcDelta, _ uint64) {
			applyOTCDeltas(tail, deltas)
		})
		if err != nil {
			return err
		}
	}
	s.tail = tail
	s.head = head
	s.refreshedAt = s.now()
	return nil
}

// scan reads the desk's Bought/Sold logs in [from, to] with the shared
// chunked scanner. apply runs once per successful chunk, after the whole chunk
// has decoded.
func (s *OTCPositionService) scan(ctx context.Context, from, to uint64, apply func([]otcDelta, uint64)) error {
	q := ethereum.FilterQuery{Addresses: []common.Address{s.contract}, Topics: [][]common.Hash{{otcBoughtTopic, otcSoldTopic}}}
	err := s.scanLogs(ctx, s.logs, q, from, to, otcRPCCallTimeout, func(logs []types.Log, end uint64) error {
		deltas, err := s.decode(logs)
		if err != nil {
			return err
		}
		apply(deltas, end)
		return nil
	})
	if err != nil && !errors.Is(err, ErrOTCPositionUnavailable) {
		return fmt.Errorf("%w: %v", ErrOTCPositionUnavailable, err)
	}
	return err
}

// Event topics, set by otc_service.go's init once the ABI is parsed (a
// package-level initializer here would run before it and read an empty ABI).
var otcBoughtTopic, otcSoldTopic common.Hash

func (s *OTCPositionService) decode(logs []types.Log) ([]otcDelta, error) {
	deltas := make([]otcDelta, 0, len(logs))
	for _, l := range logs {
		if l.Removed || l.Address != s.contract || len(l.Topics) == 0 {
			continue
		}
		var event string
		switch l.Topics[0] {
		case otcBoughtTopic:
			event = "Bought"
		case otcSoldTopic:
			event = "Sold"
		default:
			continue
		}
		// A log we cannot read is an error, not a skip: dropping a Sold
		// would raise someone's cap.
		if len(l.Topics) < 2 {
			return nil, fmt.Errorf("%w: %s log %s#%d has no address topic", ErrOTCPositionUnavailable, event, l.TxHash.Hex(), l.Index)
		}
		values, err := parsedOTCABI.Unpack(event, l.Data)
		if err != nil || len(values) == 0 {
			return nil, fmt.Errorf("%w: decode %s log %s#%d: %v", ErrOTCPositionUnavailable, event, l.TxHash.Hex(), l.Index, err)
		}
		amount, ok := values[0].(*big.Int)
		if !ok {
			return nil, fmt.Errorf("%w: %s log %s#%d: unexpected amount type", ErrOTCPositionUnavailable, event, l.TxHash.Hex(), l.Index)
		}
		deltas = append(deltas, otcDelta{
			wallet: common.BytesToAddress(l.Topics[1].Bytes()),
			amount: amount,
			bought: event == "Bought",
		})
	}
	return deltas, nil
}

func applyOTCDeltas(into map[common.Address]*otcTotals, deltas []otcDelta) {
	for _, d := range deltas {
		t := into[d.wallet]
		if t == nil {
			t = &otcTotals{bought: new(big.Int), sold: new(big.Int)}
			into[d.wallet] = t
		}
		if d.bought {
			t.bought.Add(t.bought, d.amount)
		} else {
			t.sold.Add(t.sold, d.amount)
		}
	}
}
