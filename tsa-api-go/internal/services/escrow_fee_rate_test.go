package services

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

const (
	testEscrow     = "0x42C3c60F40c48497D023166C6088e27872D63fcb"
	testFeeManager = "0xfaF9Fd9a1a75fEF9030B1be426c5330C85f326c4"
	testUSDC       = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
)

// fakeEscrowChain answers the three reads PlatformFeeBPS makes, and records the
// rateFor key it was asked for.
type fakeEscrowChain struct {
	feeManager common.Address
	symbol     string
	rate       [4]interface{} // bps uint16, flat uint64, exempt bool, active bool
	fail       bool
	calls      int
	askedFor   []interface{}
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
		f.askedFor, _ = parsedFeeManagerABI.Methods["rateFor"].Inputs.Unpack(call.Data[4:])
		return parsedFeeManagerABI.Methods["rateFor"].Outputs.Pack(f.rate[:]...)
	}
	return nil, errors.New("unexpected call")
}

func newFeeTestEscrow(chain *fakeEscrowChain) *EscrowService {
	return &EscrowService{escrowAddress: testEscrow, caller: chain}
}

// Every branch of ProductEscrowV2._calculatePlatformFee (MCGP aside, which the
// caller zeroes), so the backend charges exactly what createOrder pulls.
func TestPlatformFeeBPS_MirrorsContract(t *testing.T) {
	cases := []struct {
		name  string
		chain fakeEscrowChain
		want  int
	}{
		{"no FeeManager → built-in 2%", fakeEscrowChain{}, 200},
		{"row not active → built-in 2%", fakeEscrowChain{
			feeManager: common.HexToAddress(testFeeManager), symbol: "USDC",
			rate: [4]interface{}{uint16(150), uint64(0), false, false},
		}, 200},
		{"active row → its bps", fakeEscrowChain{
			feeManager: common.HexToAddress(testFeeManager), symbol: "USDC",
			rate: [4]interface{}{uint16(150), uint64(0), false, true},
		}, 150},
		{"exempt row → 0, whatever the token", fakeEscrowChain{
			feeManager: common.HexToAddress(testFeeManager), symbol: "USDC",
			rate: [4]interface{}{uint16(150), uint64(0), true, true},
		}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chain := c.chain
			got, err := newFeeTestEscrow(&chain).PlatformFeeBPS(context.Background(), testUSDC)
			if err != nil || got != c.want {
				t.Fatalf("got %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

// The contract keys rateFor by ITS tokenSymbol for the address — not by our
// name for the token, and never the "*" wildcard.
func TestPlatformFeeBPS_AsksForTheEscrowsOwnSymbol(t *testing.T) {
	chain := &fakeEscrowChain{
		feeManager: common.HexToAddress(testFeeManager), symbol: "USDC.e",
		rate: [4]interface{}{uint16(100), uint64(0), false, true},
	}
	if _, err := newFeeTestEscrow(chain).PlatformFeeBPS(context.Background(), testUSDC); err != nil {
		t.Fatal(err)
	}
	if len(chain.askedFor) != 2 || chain.askedFor[0] != "product_merchant" || chain.askedFor[1] != "USDC.e" {
		t.Fatalf("rateFor asked with %v, want [product_merchant USDC.e]", chain.askedFor)
	}
}

func TestPlatformFeeBPS_CachesAndSurvivesAnRPCBlip(t *testing.T) {
	chain := &fakeEscrowChain{
		feeManager: common.HexToAddress(testFeeManager), symbol: "USDC",
		rate: [4]interface{}{uint16(150), uint64(0), false, true},
	}
	es := newFeeTestEscrow(chain)
	if got, _ := es.PlatformFeeBPS(context.Background(), testUSDC); got != 150 {
		t.Fatalf("first read = %d", got)
	}
	reads := chain.calls
	if got, _ := es.PlatformFeeBPS(context.Background(), testUSDC); got != 150 || chain.calls != reads {
		t.Fatalf("within TTL: got %d with %d extra calls, want cached 150", got, chain.calls-reads)
	}

	// Expired and the RPC is down: the last read rate, no error.
	v, _ := es.feeBPS.Load("0x29219dd400f2bf60e5a23d13be72b486d4038894")
	entry := v.(feeBPSEntry)
	entry.readAt = entry.readAt.Add(-2 * escrowFeeBPSTTL)
	es.feeBPS.Store("0x29219dd400f2bf60e5a23d13be72b486d4038894", entry)
	chain.fail = true
	if got, err := es.PlatformFeeBPS(context.Background(), testUSDC); got != 150 || err != nil {
		t.Fatalf("stale fallback = %d, %v; want 150, nil", got, err)
	}
}

func TestPlatformFeeBPS_ErrorsWithNothingToFallBackOn(t *testing.T) {
	if _, err := newFeeTestEscrow(&fakeEscrowChain{fail: true}).PlatformFeeBPS(context.Background(), testUSDC); err == nil {
		t.Fatal("expected an error with no prior read")
	}
	if _, err := (&EscrowService{escrowAddress: testEscrow}).PlatformFeeBPS(context.Background(), testUSDC); err == nil {
		t.Fatal("expected an error without an RPC client")
	}
}
