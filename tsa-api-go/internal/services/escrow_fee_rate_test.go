package services

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
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

func newFeeTestEscrow(chain *fakeEscrowChain) *EscrowService {
	return &EscrowService{escrowAddress: testEscrow, caller: chain}
}

func withFeeManager(rates map[string][4]interface{}) fakeEscrowChain {
	return fakeEscrowChain{feeManager: common.HexToAddress(testFeeManager), symbol: "USDC", rates: rates}
}

func row(bps uint16, flat uint64, exempt, active bool) [4]interface{} {
	return [4]interface{}{bps, flat, exempt, active}
}

// Every branch of ProductEscrowV2._calculatePlatformFee (MCGP aside, which the
// caller zeroes), so the backend charges exactly what createOrder pulls.
func TestPlatformFeeBPS_MirrorsContract(t *testing.T) {
	cases := []struct {
		name    string
		chain   fakeEscrowChain
		want    int
		wantErr error
	}{
		{"no FeeManager → built-in 2%", fakeEscrowChain{}, 200, nil},
		{"row not active → built-in 2%", withFeeManager(map[string][4]interface{}{"product_merchant": row(150, 0, false, false)}), 200, nil},
		{"active row → its bps", withFeeManager(map[string][4]interface{}{"product_merchant": row(150, 0, false, true)}), 150, nil},
		{"exempt row → 0, whatever the token", withFeeManager(map[string][4]interface{}{"product_merchant": row(150, 0, true, true)}), 0, nil},
		{"at the contract max → allowed", withFeeManager(map[string][4]interface{}{"product_merchant": row(300, 0, false, true)}), 300, nil},
		{"above the contract max → escrow reverts", withFeeManager(map[string][4]interface{}{"product_merchant": row(301, 0, false, true)}), 0, ErrEscrowFeeAboveMax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chain := c.chain
			got, err := newFeeTestEscrow(&chain).PlatformFeeBPS(context.Background(), testUSDC)
			if !errors.Is(err, c.wantErr) || got != c.want {
				t.Fatalf("got %d, %v; want %d, %v", got, err, c.want, c.wantErr)
			}
		})
	}
}

// Every branch of ProductEscrowV2._gasFee.
func TestGasFeeWei_MirrorsContract(t *testing.T) {
	cases := []struct {
		name  string
		chain fakeEscrowChain
		want  int64
	}{
		{"no FeeManager → GAS_FEE", fakeEscrowChain{}, DefaultEscrowGasFee},
		{"row not active → GAS_FEE", withFeeManager(map[string][4]interface{}{"product": row(0, 50000, false, false)}), DefaultEscrowGasFee},
		{"active row → its flat", withFeeManager(map[string][4]interface{}{"product": row(0, 50000, false, true)}), 50000},
		{"exempt row → 0", withFeeManager(map[string][4]interface{}{"product": row(0, 50000, true, true)}), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chain := c.chain
			got, err := newFeeTestEscrow(&chain).GasFeeWei(context.Background(), testUSDC)
			if err != nil || got.Int64() != c.want {
				t.Fatalf("got %v, %v; want %d", got, err, c.want)
			}
		})
	}
}

// The contract keys rateFor by ITS tokenSymbol for the address — not by our
// name for the token, and never the "*" wildcard.
func TestPlatformFeeBPS_AsksForTheEscrowsOwnSymbol(t *testing.T) {
	chain := withFeeManager(map[string][4]interface{}{"product_merchant": row(100, 0, false, true)})
	chain.symbol = "USDC.e"
	if _, err := newFeeTestEscrow(&chain).PlatformFeeBPS(context.Background(), testUSDC); err != nil {
		t.Fatal(err)
	}
	if len(chain.askedFor) != 1 || chain.askedFor[0][0] != "product_merchant" || chain.askedFor[0][1] != "USDC.e" {
		t.Fatalf("rateFor asked with %v, want [product_merchant USDC.e]", chain.askedFor)
	}
}

func ageCachedRate(es *EscrowService, feature string, by time.Duration) {
	key := feature + "|0x29219dd400f2bf60e5a23d13be72b486d4038894"
	v, _ := es.rates.Load(key)
	entry := v.(rateEntry)
	entry.readAt = entry.readAt.Add(-by)
	es.rates.Store(key, entry)
}

func TestEscrowRate_CachesAndSurvivesAShortRPCBlip(t *testing.T) {
	chain := withFeeManager(map[string][4]interface{}{"product_merchant": row(150, 0, false, true)})
	es := newFeeTestEscrow(&chain)
	if got, _ := es.PlatformFeeBPS(context.Background(), testUSDC); got != 150 {
		t.Fatalf("first read = %d", got)
	}
	reads := chain.calls
	if got, _ := es.PlatformFeeBPS(context.Background(), testUSDC); got != 150 || chain.calls != reads {
		t.Fatalf("within TTL: got %d with %d extra calls, want cached 150", got, chain.calls-reads)
	}

	// Past the TTL and the RPC is down: the previous read stands in, no error.
	ageCachedRate(es, "product_merchant", 2*escrowRateTTL)
	chain.fail = true
	if got, err := es.PlatformFeeBPS(context.Background(), testUSDC); got != 150 || err != nil {
		t.Fatalf("stale fallback = %d, %v; want 150, nil", got, err)
	}
}

// A long outage must not keep serving an old rate forever.
func TestEscrowRate_StaleReadExpires(t *testing.T) {
	chain := withFeeManager(map[string][4]interface{}{"product_merchant": row(150, 0, false, true)})
	es := newFeeTestEscrow(&chain)
	if _, err := es.PlatformFeeBPS(context.Background(), testUSDC); err != nil {
		t.Fatal(err)
	}
	ageCachedRate(es, "product_merchant", escrowRateMaxStale+time.Second)
	chain.fail = true
	if _, err := es.PlatformFeeBPS(context.Background(), testUSDC); err == nil {
		t.Fatal("expected an error once the last read is older than escrowRateMaxStale")
	}
}

func TestEscrowRate_ErrorsWithNothingToFallBackOn(t *testing.T) {
	if _, err := newFeeTestEscrow(&fakeEscrowChain{fail: true}).PlatformFeeBPS(context.Background(), testUSDC); err == nil {
		t.Fatal("expected an error with no prior read")
	}
	if _, err := (&EscrowService{escrowAddress: testEscrow}).GasFeeWei(context.Background(), testUSDC); err == nil {
		t.Fatal("expected an error without an RPC client")
	}
	if _, err := newFeeTestEscrow(&fakeEscrowChain{}).PlatformFeeBPS(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an unconfigured token address")
	}
}

func orderCreatedLog(t *testing.T, orderID [32]byte, product, shipping, fee int64) *types.Log {
	t.Helper()
	event := parsedEscrowABI.Events["OrderCreated"]
	data, err := event.Inputs.NonIndexed().Pack(common.HexToAddress(testUSDC), big.NewInt(product), big.NewInt(shipping), big.NewInt(fee))
	if err != nil {
		t.Fatal(err)
	}
	return &types.Log{
		Address: common.HexToAddress(testEscrow),
		Topics: []common.Hash{
			event.ID,
			common.Hash(orderID),
			common.BytesToHash(common.HexToAddress("0x00000000000000000000000000000000000000b1").Bytes()),
			common.BytesToHash(common.HexToAddress("0x00000000000000000000000000000000000000a1").Bytes()),
		},
		Data: data,
	}
}

// A buyer must not be able to mark order A paid with order B's escrow tx.
func TestFindOrderCreated_MatchesOnlyThisOrder(t *testing.T) {
	var mine, other [32]byte
	mine[0], other[0] = 1, 2
	escrow := common.HexToAddress(testEscrow)

	logs := []*types.Log{orderCreatedLog(t, other, 1000, 0, 20)}
	if _, err := findOrderCreated(logs, escrow, mine); err == nil {
		t.Fatal("another order's OrderCreated was accepted for this order")
	}

	logs = append(logs, orderCreatedLog(t, mine, 19607844, 10000000, 392156))
	got, err := findOrderCreated(logs, escrow, mine)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProductAmount.Int64() != 19607844 || got.ShippingAmount.Int64() != 10000000 || got.PlatformFee.Int64() != 392156 {
		t.Fatalf("decoded %v / %v / %v", got.ProductAmount, got.ShippingAmount, got.PlatformFee)
	}
	if got.Buyer != common.HexToAddress("0x00000000000000000000000000000000000000b1") {
		t.Fatalf("buyer = %s", got.Buyer.Hex())
	}
}

func TestFindOrderCreated_IgnoresOtherContracts(t *testing.T) {
	var mine [32]byte
	mine[0] = 1
	l := orderCreatedLog(t, mine, 1000, 0, 20)
	l.Address = common.HexToAddress("0x0000000000000000000000000000000000000bad")
	if _, err := findOrderCreated([]*types.Log{l}, common.HexToAddress(testEscrow), mine); err == nil {
		t.Fatal("an OrderCreated from another contract was accepted")
	}
}
