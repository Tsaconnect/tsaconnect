package handlers

import (
	"context"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/chains"
)

// stubTron is a hand fake of chains.ChainClient for handler wiring tests.
type stubTron struct{ chains.ChainClient }

func (stubTron) Family() string                { return "tron" }
func (stubTron) ValidateAddress(a string) error { return nil }
func (stubTron) GetBalances(context.Context, string) ([]chains.Balance, error) {
	return []chains.Balance{{Symbol: "TRX", Raw: "5", Decimals: 6, IsNative: true}}, nil
}

func TestRegistryResolvesFamily(t *testing.T) {
	r := chains.NewRegistry()
	r.Register("tron", stubTron{})
	c, ok := r.ForKey("tron")
	if !ok {
		t.Fatal("tron not registered")
	}
	bals, _ := c.GetBalances(context.Background(), "TXYZ")
	if len(bals) != 1 || bals[0].Symbol != "TRX" {
		t.Fatalf("unexpected balances: %+v", bals)
	}
}
