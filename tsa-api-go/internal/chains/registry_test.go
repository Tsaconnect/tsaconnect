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

func TestRegistryLookupByKeyAndFamily(t *testing.T) {
	r := NewRegistry()
	r.Register("tron", stubClient{family: "tron"})

	if _, ok := r.ForKey("tron"); !ok {
		t.Fatal("expected tron by key")
	}
	if _, ok := r.ForFamily("tron"); !ok {
		t.Fatal("expected tron by family")
	}
	if _, ok := r.ForKey("solana"); ok {
		t.Fatal("solana must be dormant (unregistered)")
	}
}
