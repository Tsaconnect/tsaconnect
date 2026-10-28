package services

import (
	"context"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// Where a FeeSinkStatus.SystemWallet was read from.
const (
	FeeSinkSourceSystemWallet = "systemWallet"
	FeeSinkSourceOwner        = "owner"
	// FeeSinkSourceFeeRecipient: a core set's FeeConfig.feeRecipient(), which every core contract pays.
	FeeSinkSourceFeeRecipient = "feeRecipient"
)

// FeeSinkContract is a fee-collecting contract other than the P2P escrows.
type FeeSinkContract struct {
	Label    string
	ChainKey string
	ChainID  int64
	Address  string
	Caller   contractCaller
}

// FeeSinks reports where ProductEscrowV2 and ServiceContactV2 pay their fees.
// A contract that predates setSystemWallet reverts on systemWallet(); it pays
// owner(), which is reported as such so the admin can see the fee is not yet
// going to the assigned wallet.
type FeeSinks struct {
	contracts []FeeSinkContract
}

func NewFeeSinks(contracts ...FeeSinkContract) *FeeSinks {
	return &FeeSinks{contracts: contracts}
}

func (f *FeeSinks) SystemWallets() []FeeSinkStatus {
	if f == nil {
		return nil
	}
	// Each contract costs up to two 10s calls, so read them side by side: a slow
	// RPC delays the admin panel by one timeout, not one per contract.
	out := make([]FeeSinkStatus, len(f.contracts))
	var wg sync.WaitGroup
	for i, c := range f.contracts {
		wg.Add(1)
		go func(i int, c FeeSinkContract) {
			defer wg.Done()
			row := FeeSinkStatus{Label: c.Label, ChainID: c.ChainID, ChainKey: c.ChainKey, Contract: c.Address}
			row.SystemWallet, row.Source, row.Reason = readFeeDestination(c)
			out[i] = row
		}(i, c)
	}
	wg.Wait()
	return out
}

// isRevert reports whether err is the contract rejecting the call (the method
// does not exist on this implementation) rather than a failure to ask.
func isRevert(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "execution reverted")
}

// readFeeDestination answers where c pays its fees. Only a revert from
// systemWallet() (a contract that predates setSystemWallet) falls back to
// owner(); a timeout or a malformed reply is "could not check", never a
// confident answer. An upgraded contract whose systemWallet is still zero pays
// owner() by design (_feeRecipient), so that is reported as owner too.
func readFeeDestination(c FeeSinkContract) (addr, source, reason string) {
	if c.Address == "" {
		return "", "", "contract address not configured"
	}
	// callP2PView packs against the P2P ABI; systemWallet() and owner() share
	// their selectors across the escrows (pinned by TestFeeSinkSelectors).
	ctx := context.Background()
	vals, err := callP2PView(ctx, c.Caller, c.Address, "systemWallet")
	switch {
	case err == nil:
		if a, ok := vals[0].(common.Address); ok && a != (common.Address{}) {
			return a.Hex(), FeeSinkSourceSystemWallet, ""
		}
	case !isRevert(err):
		return "", "", err.Error()
	}
	vals, err = callP2PView(ctx, c.Caller, c.Address, "owner")
	if err != nil {
		return "", "", err.Error()
	}
	a, ok := vals[0].(common.Address)
	if !ok || strings.EqualFold(a.Hex(), (common.Address{}).Hex()) {
		return "", "", "contract returned no fee destination"
	}
	return a.Hex(), FeeSinkSourceOwner, ""
}

// CombinedFeeSinks concatenates several readers into one, in order. Readers
// must tolerate a nil receiver: the nil check below does not catch a typed nil
// pointer stored in the interface (the P2P registry is one).
type CombinedFeeSinks []interface{ SystemWallets() []FeeSinkStatus }

func (c CombinedFeeSinks) SystemWallets() []FeeSinkStatus {
	var out []FeeSinkStatus
	for _, r := range c {
		if r != nil {
			out = append(out, r.SystemWallets()...)
		}
	}
	return out
}
