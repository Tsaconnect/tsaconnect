package chains

import (
	"context"
	"testing"
)

type stubClient struct{ family string }

func (s stubClient) Family() string                                          { return s.family }
func (s stubClient) ValidateAddress(string) error                            { return nil }
func (s stubClient) GetBalances(context.Context, string) ([]Balance, error)  { return nil, nil }
func (s stubClient) EstimateFee(context.Context, TransferRequest) (FeeQuote, error) {
	return FeeQuote{}, nil
}
func (s stubClient) PrepareTransfer(context.Context, TransferRequest) (PreparedTx, error) {
	return PreparedTx{}, nil
}
func (s stubClient) AssembleAndBroadcast(context.Context, PreparedTx, []Signature) (string, error) {
	return "", nil
}
func (s stubClient) GetTxStatus(context.Context, string) (TxStatus, error) { return TxStatus{}, nil }
func (s stubClient) History(context.Context, string, string) ([]Activity, string, error) {
	return nil, "", nil
}

func TestRegistryLookupByNetworkAndKey(t *testing.T) {
	r := NewRegistry()
	r.Register("mainnet", "tron", stubClient{family: "tron"})
	r.Register("testnet", "tron", stubClient{family: "tron"})

	if _, ok := r.ForKey("mainnet", "tron"); !ok {
		t.Fatal("expected mainnet tron")
	}
	if _, ok := r.ForKey("testnet", "tron"); !ok {
		t.Fatal("expected testnet tron")
	}
	// Same chainKey on an unconfigured network stays dormant.
	if _, ok := r.ForKey("mainnet", "solana"); ok {
		t.Fatal("solana must be dormant (unregistered)")
	}
}
