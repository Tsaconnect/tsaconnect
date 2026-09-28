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
	// otcLogMinChunk is the smallest span the scanner halves down to before
	// it gives up and reports the RPC as unreadable.
	otcLogMinChunk uint64 = 128
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
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
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
	chunk       uint64 // current eth_getLogs span; 0 = unbounded
	next        uint64 // first block not yet committed
	committed   map[common.Address]*otcTotals
	tail        map[common.Address]*otcTotals // provisional: blocks >= next
	head        uint64
	refreshedAt time.Time
}

// NewOTCPositionService builds the index for the OTC contract. Nothing is read
// until the first Position or Warm call.
func NewOTCPositionService(logs OTCLogSource, balances OTCTokenBalanceReader, contractAddress, mcgpToken string, deployBlock, maxBlockRange uint64) *OTCPositionService {
	return &OTCPositionService{
		logs:      logs,
		balances:  balances,
		contract:  common.HexToAddress(contractAddress),
		mcgpToken: strings.TrimSpace(mcgpToken),
		now:       time.Now,
		lock:      make(chan struct{}, 1),
		chunk:     maxBlockRange,
		next:      deployBlock,
		committed: map[common.Address]*otcTotals{},
		tail:      map[common.Address]*otcTotals{},
	}
}

func (s *OTCPositionService) configured() bool {
	return s != nil && s.logs != nil && s.balances != nil && s.mcgpToken != "" && s.contract != (common.Address{})
}

// Warm runs the catch-up scan ahead of the first request.
func (s *OTCPositionService) Warm(ctx context.Context) error {
	if !s.configured() {
		return fmt.Errorf("%w: not configured", ErrOTCPositionUnavailable)
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	return s.refresh(ctx)
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
func (s *OTCPositionService) refresh(ctx context.Context) error {
	head, err := s.logs.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("%w: block number: %v", ErrOTCPositionUnavailable, err)
	}
	if !s.refreshedAt.IsZero() && head <= s.head {
		// Nothing new — or a lagging node reporting an older head. Never step
		// backwards: that would drop provisional logs already counted.
		s.refreshedAt = s.now()
		return nil
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

// scan reads [from, to] in chunks, halving the span whenever the RPC refuses
// one (range caps, result-count caps and timeouts all look alike from here).
// apply runs once per successful chunk, after the whole chunk has decoded.
func (s *OTCPositionService) scan(ctx context.Context, from, to uint64, apply func([]otcDelta, uint64)) error {
	for from <= to {
		end := to
		if s.chunk > 0 && to-from+1 > s.chunk {
			end = from + s.chunk - 1
		}
		logs, err := s.logs.FilterLogs(ctx, ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(from),
			ToBlock:   new(big.Int).SetUint64(end),
			Addresses: []common.Address{s.contract},
			Topics:    [][]common.Hash{{otcBoughtTopic, otcSoldTopic}},
		})
		if err != nil {
			span := end - from + 1
			if ctx.Err() != nil || span <= otcLogMinChunk {
				return fmt.Errorf("%w: eth_getLogs %d-%d: %v", ErrOTCPositionUnavailable, from, end, err)
			}
			s.chunk = span / 2
			continue
		}
		deltas, err := s.decode(logs)
		if err != nil {
			return err
		}
		apply(deltas, end)
		from = end + 1
	}
	return nil
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
