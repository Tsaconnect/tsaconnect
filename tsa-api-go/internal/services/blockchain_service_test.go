package services

import (
	"errors"
	"math/big"
	"slices"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// TestTokenAddressForChainID verifies that the lookup returns the correct
// per-chain token address and does not bleed Sonic addresses into BSC lookups
// (or vice-versa).
func TestTokenAddressForChainID(t *testing.T) {
	sonicUSDT := "0xSonicUSDT0000000000000000000000000000000"
	bscUSDT := "0xBscUSDT0000000000000000000000000000000000"

	cfg := &config.Config{
		Networks: map[string]config.NetworkConfig{
			"mainnet": {
				Chains: map[string]config.ChainConfig{
					"sonic": {ChainID: 146},
					"bsc":   {ChainID: 56},
				},
				TokenAddresses: map[string]string{
					"sonic:USDT": sonicUSDT,
					"bsc:USDT":   bscUSDT,
				},
			},
		},
	}

	bs := &BlockchainService{cfg: cfg}

	got146 := bs.TokenAddressForChainID(146, "USDT")
	if got146 != sonicUSDT {
		t.Errorf("chainID 146 (Sonic): want %s, got %s", sonicUSDT, got146)
	}

	got56 := bs.TokenAddressForChainID(56, "USDT")
	if got56 != bscUSDT {
		t.Errorf("chainID 56 (BSC): want %s, got %s", bscUSDT, got56)
	}

	if got146 == got56 {
		t.Errorf("Sonic and BSC USDT addresses must differ, both resolved to %s", got146)
	}

	// Unknown chain returns empty string — no panic.
	if addr := bs.TokenAddressForChainID(999, "USDT"); addr != "" {
		t.Errorf("unknown chainID 999: want empty string, got %s", addr)
	}

	// Nil cfg is safe.
	bsNil := &BlockchainService{cfg: nil}
	if addr := bsNil.TokenAddressForChainID(146, "USDT"); addr != "" {
		t.Errorf("nil cfg: want empty string, got %s", addr)
	}
}

// TestIsNativeCurrency pins which symbol is a chain's own coin: the send
// path builds a native transfer for it and an ERC-20 transfer for anything else.
func TestIsNativeCurrency(t *testing.T) {
	cfg := &config.Config{
		Networks: map[string]config.NetworkConfig{
			"mainnet": {Chains: map[string]config.ChainConfig{
				"polygon": {ChainID: 137, NativeCurrency: "POL"},
				"base":    {ChainID: 8453, NativeCurrency: "ETH"},
				"bsc":     {ChainID: 56, NativeCurrency: "BNB"},
			}},
			"testnet": {Chains: map[string]config.ChainConfig{
				"bsc":  {ChainID: 97, NativeCurrency: "tBNB"},
				"bare": {ChainID: 1234},
			}},
		},
	}
	bs := &BlockchainService{cfg: cfg}

	cases := []struct {
		network, chain, symbol string
		want                   bool
	}{
		{"mainnet", "polygon", "POL", true},
		{"mainnet", "polygon", " pol ", true},
		{"mainnet", "base", "ETH", true},
		{"mainnet", "polygon", "ETH", false},
		{"mainnet", "polygon", "S", false},
		{"mainnet", "base", "WETH", false},
		{"mainnet", "bsc", "tBNB", true},
		{"testnet", "bsc", "BNB", true},
		{"testnet", "bsc", "TBNB", true},
		{"testnet", "bare", "", false},
		{"mainnet", "solana", "SOL", false},
		{"devnet", "polygon", "POL", false},
	}
	for _, tc := range cases {
		if got := bs.IsNativeCurrency(tc.network, tc.chain, tc.symbol); got != tc.want {
			t.Errorf("IsNativeCurrency(%q, %q, %q) = %v, want %v", tc.network, tc.chain, tc.symbol, got, tc.want)
		}
	}

	if (&BlockchainService{}).IsNativeCurrency("mainnet", "polygon", "POL") {
		t.Error("nil cfg: want false")
	}
}

// TestChainNameForID pins the mirror of ChainIDForName: the server must
// never trust a client-supplied chain name, so it derives one from a chain
// ID (e.g. a LiFi route's own chainId) instead.
func TestChainNameForID(t *testing.T) {
	b := testChainRegistry() // base 8453, bsc 56, optimism 10, linea 59144
	if name, ok := b.ChainNameForID(56); !ok || name != "bsc" {
		t.Fatalf("56 -> %q,%v want bsc", name, ok)
	}
	if _, ok := b.ChainNameForID(999999); ok {
		t.Fatal("an unknown chain id must not resolve")
	}
}

// testnetAwareRegistry is the real shape: a testnet reuses its mainnet's chain
// KEY ("bsc" is 56 on mainnet and 97 on testnet), and only some mainnet chains
// have a connected client.
func testnetAwareRegistry() *BlockchainService {
	return &BlockchainService{
		cfg: &config.Config{Networks: map[string]config.NetworkConfig{
			"mainnet": {Chains: map[string]config.ChainConfig{"bsc": {ChainID: 56}, "optimism": {ChainID: 10}}},
			"testnet": {Chains: map[string]config.ChainConfig{"bsc": {ChainID: 97}}},
		}},
		clients: map[string]*blockchain.EVMClient{"mainnet:bsc": {}, "testnet:bsc": {}},
	}
}

// MainnetChainNameForID is the set of chains a delivery can be confirmed on:
// mainnet, with a connected client (rampTransferVerifiers builds a verifier for
// exactly those). ChainNameForID, which scans testnets too, is not that set.
func TestMainnetChainNameForID_OnlyAMainnetChainWithAClient(t *testing.T) {
	b := testnetAwareRegistry()
	if name, ok := b.MainnetChainNameForID(56); !ok || name != "bsc" {
		t.Fatalf("56 -> %q,%v want bsc", name, ok)
	}
	if name, ok := b.ChainNameForID(97); !ok || name != "bsc" {
		t.Fatalf("the general resolver aliases the testnet onto %q,%v — the reason the pay paths must not use it", name, ok)
	}
	for name, id := range map[string]int64{"a testnet": 97, "mainnet without a client": 10, "unknown": 999999} {
		if got, ok := b.MainnetChainNameForID(id); ok {
			t.Fatalf("%s (%d) resolved to %q", name, id, got)
		}
	}
	if _, ok := (&BlockchainService{}).MainnetChainNameForID(56); ok {
		t.Fatal("a registry with no config resolved a chain")
	}
}

// MainnetChainIDs lists the same set MainnetChainNameForID resolves, in order.
func TestMainnetChainIDs_OnlyMainnetChainsWithAClient(t *testing.T) {
	b := testnetAwareRegistry()
	b.cfg.Networks["mainnet"].Chains["base"] = config.ChainConfig{ChainID: 8453}
	b.clients["mainnet:base"] = &blockchain.EVMClient{}
	if got := b.MainnetChainIDs(); !slices.Equal(got, []int64{56, 8453}) {
		t.Fatalf("ids %v, want [56 8453]", got)
	}
	var none *BlockchainService
	if got := none.MainnetChainIDs(); len(got) != 0 {
		t.Fatalf("a nil registry listed %v", got)
	}
}

type fakeBalanceChain struct {
	code     bool
	codeErr  error
	bal      *big.Int
	balErr   error
	gotToken string
	gotOwner string
}

func (f *fakeBalanceChain) HasContractCode(string) (bool, error) { return f.code, f.codeErr }
func (f *fakeBalanceChain) GetTokenBalance(token, owner string) (*big.Int, error) {
	f.gotToken, f.gotOwner = token, owner
	return f.bal, f.balErr
}

// A balance — zero above all — is only reported for a token address that is a
// contract on the chain; otherwise it is an error the reconciler waits on.
func TestDepositBalanceFrom_OnlyAContractCanAnswerEmpty(t *testing.T) {
	zero := big.NewInt(0)
	for name, tc := range map[string]struct {
		f       *fakeBalanceChain
		wantErr bool
	}{
		"a contract holding nothing":   {&fakeBalanceChain{code: true, bal: zero}, false},
		"a contract holding something": {&fakeBalanceChain{code: true, bal: big.NewInt(5)}, false},
		"no code at the token address": {&fakeBalanceChain{code: false, bal: zero}, true},
		"the code cannot be read":      {&fakeBalanceChain{codeErr: errors.New("rpc down"), bal: zero}, true},
		"the balance cannot be read":   {&fakeBalanceChain{code: true, balErr: errors.New("rpc down")}, true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := depositBalanceFrom(tc.f, "bsc", "USDT", "0xtoken", "0xdeposit")
			if tc.wantErr != (err != nil) {
				t.Fatalf("bal %v err %v", got, err)
			}
			if tc.wantErr && got != nil {
				t.Fatalf("an unprovable read returned a balance: %v", got)
			}
			if !tc.wantErr && (tc.f.gotToken != "0xtoken" || tc.f.gotOwner != "0xdeposit") {
				t.Fatalf("asked about %s / %s", tc.f.gotToken, tc.f.gotOwner)
			}
		})
	}
}
