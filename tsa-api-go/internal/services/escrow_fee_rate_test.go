package services

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	testEscrow     = "0x42C3c60F40c48497D023166C6088e27872D63fcb"
	testFeeManager = "0xfaF9Fd9a1a75fEF9030B1be426c5330C85f326c4"
	testUSDC       = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
)

// fakeEscrowChain answers the reads the escrow rate lookups make, per feature,
// and records the rateFor keys it was asked for.
type fakeEscrowChain struct {
	feeManager common.Address
	symbol     string
	rates      map[string][4]interface{} // feature → bps uint16, flat uint64, exempt bool, active bool
	fail       bool
	calls      int
	askedFor   [][]interface{}
}

func (f *fakeEscrowChain) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("rpc down")
	}
	sel := call.Data[:4]
	switch {
	case bytes.Equal(sel, parsedEscrowABI.Methods["feeManager"].ID):
		return parsedEscrowABI.Methods["feeManager"].Outputs.Pack(f.feeManager)
	case bytes.Equal(sel, parsedEscrowABI.Methods["tokenSymbol"].ID):
		return parsedEscrowABI.Methods["tokenSymbol"].Outputs.Pack(f.symbol)
	case bytes.Equal(sel, parsedFeeManagerABI.Methods["rateFor"].ID):
		args, _ := parsedFeeManagerABI.Methods["rateFor"].Inputs.Unpack(call.Data[4:])
		f.askedFor = append(f.askedFor, args)
		r, ok := f.rates[args[0].(string)]
		if !ok {
			r = [4]interface{}{uint16(0), uint64(0), false, false}
		}
		return parsedFeeManagerABI.Methods["rateFor"].Outputs.Pack(r[:]...)
	}
	return nil, errors.New("unexpected call")
}

func withFeeManager(rates map[string][4]interface{}) fakeEscrowChain {
	return fakeEscrowChain{feeManager: common.HexToAddress(testFeeManager), symbol: "USDC", rates: rates}
}

func row(bps uint16, flat uint64, exempt, active bool) [4]interface{} {
	return [4]interface{}{bps, flat, exempt, active}
}

// newOrderTestEscrow is an EscrowService over chain whose OrderEscrow and FeeConfig are set.
func newOrderTestEscrow(chain *FakeEscrowChain) *EscrowService {
	return &EscrowService{escrowAddress: testEscrow, feeConfig: testFeeManager, chainID: 146, caller: chain, receipts: chain,
		fees: NewFeeConfigReader(map[int64]FeeManagerRPCClient{146: chain})}
}

// Pricing reads FeeConfig's order row: the merchant fee (refused above MAX_FEE_BPS), the flat fee
// and maxPerOrder; an inactive row or missing limits mean the token cannot be checked out in.
func TestOrderRates_ReadFeeConfig(t *testing.T) {
	ctx := context.Background()
	chain := NewFakeEscrowChain()
	es := newOrderTestEscrow(chain)
	bps, err := es.PlatformFeeBPS(ctx, testUSDC)
	flat, err2 := es.GasFeeWei(ctx, testUSDC)
	max, err3 := es.MaxOrderAmount(ctx, testUSDC)
	if err != nil || err2 != nil || err3 != nil || bps != 200 || flat.Int64() != 100_000 || max.Int64() != 1_000_000_000 {
		t.Fatalf("rates = %d %v %v (%v %v %v)", bps, flat, max, err, err2, err3)
	}

	for name, set := range map[string]func(*FakeEscrowChain){
		"inactive row": func(c *FakeEscrowChain) { c.Rate.Active = false },
		"no limits":    func(c *FakeEscrowChain) { c.Limits = nil },
	} {
		c := NewFakeEscrowChain()
		set(c)
		if _, err := newOrderTestEscrow(c).PlatformFeeBPS(ctx, testUSDC); !errors.Is(err, ErrOrderFeeNotConfigured) {
			t.Errorf("%s: err = %v, want ErrOrderFeeNotConfigured", name, err)
		}
	}
	high := NewFakeEscrowChain()
	high.Rate.BPS = MaxOrderFeeBPS + 1
	if _, err := newOrderTestEscrow(high).PlatformFeeBPS(ctx, testUSDC); !errors.Is(err, ErrEscrowFeeAboveMax) {
		t.Fatalf("over the cap: err = %v", err)
	}
	if _, err := (&EscrowService{}).PlatformFeeBPS(ctx, testUSDC); !errors.Is(err, ErrOrderEscrowUnavailable) {
		t.Fatalf("no order escrow: err = %v", err)
	}
}

// A read is cached for a minute and survives a short RPC blip; past the stale bound it errors.
func TestOrderRates_CacheAndStaleBound(t *testing.T) {
	ctx := context.Background()
	chain := NewFakeEscrowChain()
	es := newOrderTestEscrow(chain)
	if _, err := es.PlatformFeeBPS(ctx, testUSDC); err != nil {
		t.Fatal(err)
	}
	chain.Err = errors.New("rpc down")
	entry, _ := es.rates.Load(strings.ToLower(testUSDC))
	e := entry.(rateEntry)
	e.readAt = time.Now().Add(-2 * time.Minute) // past the TTL, inside the stale bound
	es.rates.Store(strings.ToLower(testUSDC), e)
	if bps, err := es.PlatformFeeBPS(ctx, testUSDC); err != nil || bps != 200 {
		t.Fatalf("blip: %d %v, want the cached read", bps, err)
	}
	e.readAt = time.Now().Add(-escrowRateMaxStale - time.Minute)
	es.rates.Store(strings.ToLower(testUSDC), e)
	if _, err := es.PlatformFeeBPS(ctx, testUSDC); err == nil {
		t.Fatal("a read older than the stale bound must not stand in")
	}
}

// OrderKey matches OrderEscrow.orderKey: keccak256(abi.encode(buyer, orderId)).
func TestOrderKey_MatchesTheContract(t *testing.T) {
	id := GenerateOrderID(uuid.MustParse("6f1c2e1a-0000-4000-8000-000000000001"))
	addrT, _ := abi.NewType("address", "", nil)
	b32, _ := abi.NewType("bytes32", "", nil)
	enc, _ := abi.Arguments{{Type: addrT}, {Type: b32}}.Pack(common.HexToAddress(testBuyerWallet), id)
	if want := crypto.Keccak256Hash(enc); OrderKey(testBuyerWallet, id) != [32]byte(want) {
		t.Fatalf("key = %x, want %x", OrderKey(testBuyerWallet, id), want)
	}
}

// The delivery window: the listing's handling days (default 7), a booking's slot end + 2 days,
// always inside the contract's 1–30.
func TestHandlingDaysFor(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	for _, tc := range []struct {
		days    int
		slotEnd *time.Time
		want    uint8
	}{
		{0, nil, DefaultOrderHandlingDays},
		{3, nil, 3},
		{45, nil, MaxOrderHandlingDays},
		{0, at(3 * time.Hour), 3},                      // slot today: 2 days grace + part of a day
		{0, at(10 * 24 * time.Hour), 12},               // 10 days out + 2
		{0, at(28 * 24 * time.Hour), 30},               // the booking lead cap lands on the contract max
		{0, at(-72 * time.Hour), MinOrderHandlingDays}, // a past slot still gets a day
	} {
		if got := HandlingDaysFor(tc.days, tc.slotEnd, now); got != tc.want {
			t.Errorf("HandlingDaysFor(%d, %v) = %d, want %d", tc.days, tc.slotEnd, got, tc.want)
		}
	}
}

// OrderCreated is matched on this order's id and decoded field by field.
func TestFindOrderCreated(t *testing.T) {
	id, other := GenerateOrderID(uuid.New()), GenerateOrderID(uuid.New())
	buyer, seller, token := common.HexToAddress(testBuyerWallet), common.HexToAddress(testSellerWallet), common.HexToAddress(testUSDC)
	deliverBy := time.Unix(1_800_000_000, 0).UTC()
	mine := OrderCreatedLog(testEscrow, id, buyer, seller, token, big.NewInt(78400), big.NewInt(500), big.NewInt(1568), big.NewInt(100_000), deliverBy)
	theirs := OrderCreatedLog(testEscrow, other, buyer, seller, token, big.NewInt(1), big.NewInt(0), big.NewInt(0), big.NewInt(0), deliverBy)
	got, err := findOrderCreated([]*types.Log{theirs, mine}, common.HexToAddress(testEscrow), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != OrderKey(testBuyerWallet, id) || got.Buyer != buyer || got.Seller != seller || got.Token != token ||
		got.ProductAmount.Int64() != 78400 || got.ShippingAmount.Int64() != 500 || got.MerchantFee.Int64() != 1568 ||
		got.FlatFee.Int64() != 100_000 || !got.DeliverBy.Equal(deliverBy) {
		t.Fatalf("decoded %+v", got)
	}
	if _, err := findOrderCreated([]*types.Log{theirs}, common.HexToAddress(testEscrow), id); err == nil {
		t.Fatal("another order's OrderCreated must not pay this order")
	}
	if _, err := findOrderCreated([]*types.Log{mine}, common.HexToAddress(testFeeManager), id); err == nil {
		t.Fatal("an OrderCreated from another contract must not pay this order")
	}
}

// The window recorded at prepare wins: a listing or booking edited before submit must not make the
// buyer's correct payment look like a mismatch.
func TestEscrowHandlingDays_RecordedWindowWins(t *testing.T) {
	days := 3
	got, err := EscrowHandlingDays(nil, &models.Order{EscrowHandlingDays: &days}, time.Now())
	if err != nil || got != 3 {
		t.Fatalf("got %d, %v", got, err)
	}
}
