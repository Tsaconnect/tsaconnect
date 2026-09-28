package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

var (
	otcTestContract = common.HexToAddress("0x00000000000000000000000000000000000000c0")
	otcTestMCGP     = "0x00000000000000000000000000000000000000d0"
	otcWalletA      = common.HexToAddress("0x000000000000000000000000000000000000000a")
	otcWalletB      = common.HexToAddress("0x000000000000000000000000000000000000000b")
	otcWalletC      = common.HexToAddress("0x000000000000000000000000000000000000000c")
)

func otcMCGP(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

// fakeOTCChain serves Bought/Sold logs the way eth_getLogs does, with an
// optional block-range cap and injected failures.
type fakeOTCChain struct {
	head       uint64
	logs       []types.Log
	rangeCap   uint64 // 0 = unlimited
	failLogs   error
	failHead   error
	logCalls   int
	balances   map[common.Address]*big.Int
	failBal    error
	queryAddrs []common.Address
}

func (f *fakeOTCChain) BlockNumber(context.Context) (uint64, error) {
	if f.failHead != nil {
		return 0, f.failHead
	}
	return f.head, nil
}

func (f *fakeOTCChain) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	f.logCalls++
	f.queryAddrs = q.Addresses
	if f.failLogs != nil {
		return nil, f.failLogs
	}
	from, to := q.FromBlock.Uint64(), q.ToBlock.Uint64()
	if f.rangeCap > 0 && to-from+1 > f.rangeCap {
		return nil, fmt.Errorf("block range too large (max %d)", f.rangeCap)
	}
	var out []types.Log
	for _, l := range f.logs {
		if l.BlockNumber < from || l.BlockNumber > to || l.Address != q.Addresses[0] {
			continue
		}
		match := false
		for _, t := range q.Topics[0] {
			if l.Topics[0] == t {
				match = true
			}
		}
		if match {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeOTCChain) GetTokenBalance(token, wallet string) (*big.Int, error) {
	if f.failBal != nil {
		return nil, f.failBal
	}
	if !strings.EqualFold(token, otcTestMCGP) {
		return nil, fmt.Errorf("unexpected token %s", token)
	}
	if b := f.balances[common.HexToAddress(wallet)]; b != nil {
		return b, nil
	}
	return new(big.Int), nil
}

func (f *fakeOTCChain) add(event string, who common.Address, amount *big.Int, block uint64) {
	data, err := parsedOTCABI.Events[event].Inputs.NonIndexed().Pack(amount, big.NewInt(1))
	if err != nil {
		panic(err)
	}
	f.logs = append(f.logs, types.Log{
		Address:     otcTestContract,
		Topics:      []common.Hash{parsedOTCABI.Events[event].ID, common.BytesToHash(who.Bytes()), common.BigToHash(big.NewInt(0))},
		Data:        data,
		BlockNumber: block,
		Index:       uint(len(f.logs)),
	})
}

func newTestPositions(f *fakeOTCChain, deploy, maxRange uint64) *OTCPositionService {
	return NewOTCPositionService(f, f, otcTestContract.Hex(), otcTestMCGP, deploy, maxRange)
}

func mustPosition(t *testing.T, s *OTCPositionService, who common.Address) *OTCPosition {
	t.Helper()
	p, err := s.Position(context.Background(), who.Hex(), 0)
	if err != nil {
		t.Fatalf("Position(%s): %v", who.Hex(), err)
	}
	return p
}

func TestComputeOTCSellable(t *testing.T) {
	cases := []struct {
		name                  string
		bought, sold, balance *big.Int
		wantNet, wantSellable *big.Int
	}{
		{"bought only, holds it all", otcMCGP(100), nil, otcMCGP(100), otcMCGP(100), otcMCGP(100)},
		{"partial sell", otcMCGP(100), otcMCGP(30), otcMCGP(500), otcMCGP(70), otcMCGP(70)},
		{"holds less than net purchase", otcMCGP(100), otcMCGP(30), otcMCGP(20), otcMCGP(70), otcMCGP(20)},
		{"sold more than bought (other sources)", otcMCGP(50), otcMCGP(80), otcMCGP(1000), otcMCGP(0), otcMCGP(0)},
		{"never bought, holds MCGP", nil, nil, otcMCGP(200), otcMCGP(0), otcMCGP(0)},
		{"all nil", nil, nil, nil, otcMCGP(0), otcMCGP(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			net, sellable := ComputeOTCSellable(tc.bought, tc.sold, tc.balance)
			if net.Cmp(tc.wantNet) != 0 || sellable.Cmp(tc.wantSellable) != 0 {
				t.Fatalf("net=%s sellable=%s, want net=%s sellable=%s", net, sellable, tc.wantNet, tc.wantSellable)
			}
		})
	}
}

func TestOTCPosition_BuysSellsAndMultipleWallets(t *testing.T) {
	f := &fakeOTCChain{head: 10_000, balances: map[common.Address]*big.Int{
		otcWalletA: otcMCGP(500), // holds more than it bought — only the purchase is sellable
		otcWalletB: otcMCGP(200), // never bought on the desk
		otcWalletC: otcMCGP(1000),
	}}
	f.add("Bought", otcWalletA, otcMCGP(60), 1_100)
	f.add("Bought", otcWalletA, otcMCGP(40), 1_200)
	f.add("Sold", otcWalletA, otcMCGP(30), 1_300)
	f.add("Bought", otcWalletC, otcMCGP(50), 1_400)
	f.add("Sold", otcWalletC, otcMCGP(80), 1_500)  // sold MCGP it got elsewhere
	f.add("Bought", otcWalletA, otcMCGP(999), 999) // before the deploy block: ignored

	s := newTestPositions(f, 1_000, 0)

	a := mustPosition(t, s, otcWalletA)
	if a.Bought.Cmp(otcMCGP(100)) != 0 || a.Sold.Cmp(otcMCGP(30)) != 0 || a.Sellable.Cmp(otcMCGP(70)) != 0 {
		t.Fatalf("wallet A: bought=%s sold=%s sellable=%s, want 100/30/70 MCGP", a.Bought, a.Sold, a.Sellable)
	}
	if b := mustPosition(t, s, otcWalletB); b.Sellable.Sign() != 0 || b.WalletBalance.Cmp(otcMCGP(200)) != 0 {
		t.Fatalf("wallet B: sellable=%s balance=%s, want 0 sellable despite 200 held", b.Sellable, b.WalletBalance)
	}
	if c := mustPosition(t, s, otcWalletC); c.Sellable.Sign() != 0 || c.NetPurchased.Sign() != 0 {
		t.Fatalf("wallet C: net=%s sellable=%s, want 0 (sold more than bought)", c.NetPurchased, c.Sellable)
	}

	// Per-wallet, not pooled: A's purchases never lift B's cap, and A is
	// capped by what it still holds.
	f.balances[otcWalletA] = otcMCGP(20)
	if a := mustPosition(t, s, otcWalletA); a.Sellable.Cmp(otcMCGP(20)) != 0 {
		t.Fatalf("wallet A holding 20: sellable=%s, want 20", a.Sellable)
	}
	if len(f.queryAddrs) != 1 || f.queryAddrs[0] != otcTestContract {
		t.Fatalf("logs must be filtered to the OTC contract, got %v", f.queryAddrs)
	}
}

func TestOTCPosition_ChunksUnderRPCRangeCap(t *testing.T) {
	f := &fakeOTCChain{head: 50_000, rangeCap: 2_000, balances: map[common.Address]*big.Int{otcWalletA: otcMCGP(1000)}}
	f.add("Bought", otcWalletA, otcMCGP(10), 1_500)
	f.add("Bought", otcWalletA, otcMCGP(20), 25_000)
	f.add("Sold", otcWalletA, otcMCGP(5), 49_990) // inside the provisional tail

	s := newTestPositions(f, 1_000, 0)
	p := mustPosition(t, s, otcWalletA)
	if p.Sellable.Cmp(otcMCGP(25)) != 0 {
		t.Fatalf("sellable=%s, want 25", p.Sellable)
	}
	if f.logCalls < 25 {
		t.Fatalf("expected the scan to be chunked under the 2000-block cap, got %d calls", f.logCalls)
	}
}

func TestOTCPosition_IncrementalRefreshCountsNewLogsOnce(t *testing.T) {
	f := &fakeOTCChain{head: 5_000, balances: map[common.Address]*big.Int{otcWalletA: otcMCGP(1000)}}
	f.add("Bought", otcWalletA, otcMCGP(100), 4_950) // provisional on the first read
	s := newTestPositions(f, 0, 0)

	if p := mustPosition(t, s, otcWalletA); p.Sellable.Cmp(otcMCGP(100)) != 0 {
		t.Fatalf("first read sellable=%s, want 100", p.Sellable)
	}

	// Head moves past the overlap: the buy is committed, and a new sell lands.
	f.head = 6_000
	f.add("Sold", otcWalletA, otcMCGP(40), 5_990)

	// A display read inside maxAge does not rescan.
	calls := f.logCalls
	if p, err := s.Position(context.Background(), otcWalletA.Hex(), time.Hour); err != nil || p.Sellable.Cmp(otcMCGP(100)) != 0 {
		t.Fatalf("cached read: sellable=%v err=%v, want 100 from cache", p, err)
	}
	if f.logCalls != calls {
		t.Fatalf("cached read scanned logs (%d -> %d calls)", calls, f.logCalls)
	}

	// Enforcement (maxAge 0) always refreshes, and the buy is not double counted.
	if p := mustPosition(t, s, otcWalletA); p.Bought.Cmp(otcMCGP(100)) != 0 || p.Sellable.Cmp(otcMCGP(60)) != 0 {
		t.Fatalf("after refresh bought=%s sellable=%s, want 100/60", p.Bought, p.Sellable)
	}
	// And again, with no new blocks: still exactly once.
	if p := mustPosition(t, s, otcWalletA); p.Bought.Cmp(otcMCGP(100)) != 0 || p.Sold.Cmp(otcMCGP(40)) != 0 {
		t.Fatalf("repeat refresh bought=%s sold=%s, want 100/40", p.Bought, p.Sold)
	}
}

func TestOTCPosition_LaggingHeadDoesNotDropCountedLogs(t *testing.T) {
	f := &fakeOTCChain{head: 5_000, balances: map[common.Address]*big.Int{otcWalletA: otcMCGP(1000)}}
	f.add("Bought", otcWalletA, otcMCGP(100), 1_000)
	f.add("Sold", otcWalletA, otcMCGP(90), 4_999)
	s := newTestPositions(f, 0, 0)
	if p := mustPosition(t, s, otcWalletA); p.Sellable.Cmp(otcMCGP(10)) != 0 {
		t.Fatalf("sellable=%s, want 10", p.Sellable)
	}
	f.head = 4_990 // a load-balanced node that has not seen the sell yet
	if p := mustPosition(t, s, otcWalletA); p.Sellable.Cmp(otcMCGP(10)) != 0 {
		t.Fatalf("lagging node raised the cap: sellable=%s, want 10", p.Sellable)
	}
}

func TestOTCPosition_FailsClosed(t *testing.T) {
	rpcDown := errors.New("connection refused")
	cases := map[string]func(*fakeOTCChain){
		"logs unreadable":    func(f *fakeOTCChain) { f.failLogs = rpcDown },
		"head unreadable":    func(f *fakeOTCChain) { f.failHead = rpcDown },
		"balance unreadable": func(f *fakeOTCChain) { f.failBal = rpcDown },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeOTCChain{head: 100_000, balances: map[common.Address]*big.Int{otcWalletA: otcMCGP(10)}}
			f.add("Bought", otcWalletA, otcMCGP(10), 50)
			breakIt(f)
			_, err := newTestPositions(f, 0, 0).Position(context.Background(), otcWalletA.Hex(), 0)
			if !errors.Is(err, ErrOTCPositionUnavailable) {
				t.Fatalf("err=%v, want ErrOTCPositionUnavailable", err)
			}
		})
	}

	t.Run("unconfigured service", func(t *testing.T) {
		var s *OTCPositionService
		if _, err := s.Position(context.Background(), otcWalletA.Hex(), 0); !errors.Is(err, ErrOTCPositionUnavailable) {
			t.Fatalf("nil service err=%v, want ErrOTCPositionUnavailable", err)
		}
	})

	t.Run("undecodable log", func(t *testing.T) {
		f := &fakeOTCChain{head: 1_000, balances: map[common.Address]*big.Int{otcWalletA: otcMCGP(10)}}
		f.add("Sold", otcWalletA, otcMCGP(10), 10)
		f.logs[0].Data = []byte{0x01} // truncated
		_, err := newTestPositions(f, 0, 0).Position(context.Background(), otcWalletA.Hex(), 0)
		if !errors.Is(err, ErrOTCPositionUnavailable) {
			t.Fatalf("err=%v, want ErrOTCPositionUnavailable — a skipped Sold would raise the cap", err)
		}
	})
}
