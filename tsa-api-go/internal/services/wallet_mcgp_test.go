package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// wallet_mcgp_test.go — owner decision 2026-09-28: the plain ERC-20 MCGP in a
// user's own wallet is never used to pay or sold for naira. Only Spendable
// MCGP is. The gate is RampSellQuoter.RefuseSource, which every sale and every
// TSA Pay payment passes.

// Sonic mainnet's wallet MCGP and USDT, as the config presets carry them.
const (
	sonicMCGP = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21"
	sonicUSDT = "0x6047828dc181963ba44974801ff68e538da5eaf9"
)

// walletMCGPRegistry is testChainRegistry plus Sonic mainnet with its MCGP and
// USDT.
func walletMCGPRegistry() *BlockchainService {
	bs := testChainRegistry()
	net := bs.cfg.Networks["mainnet"]
	net.Chains["sonic"] = config.ChainConfig{ChainID: 146}
	net.TokenAddresses["sonic:MCGP"] = sonicMCGP
	net.TokenAddresses["sonic:USDT"] = sonicUSDT
	return bs
}

// A sale from wallet MCGP is refused before LiFi is asked anything — the one
// check the sell quote, the sell intent and every TSA Pay route go through.
// USDT on the same chain is still quoted.
func TestRampSellQuote_RefusesWalletMCGPBeforeAskingLiFi(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	q.chains = walletMCGPRegistry()
	q.SetRouterAllowlist(map[int64][]common.Address{146: {common.HexToAddress(sellTestDiamond)}})
	in := sellIn
	in.FromChainID = 146
	for _, tok := range []string{sonicMCGP, strings.ToLower(sonicMCGP), " " + sonicMCGP + " "} {
		in.FromToken = tok
		if res, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellWalletMCGP) {
			t.Fatalf("%q: res = %+v err = %v, want ErrSellWalletMCGP", tok, res, err)
		}
	}
	if lifi.quoteCalls() != 0 {
		t.Fatalf("LiFi was asked %d times for a token that can never be sold", lifi.quoteCalls())
	}

	in.FromToken = sonicUSDT
	if _, err := q.Quote(context.Background(), in); err != nil {
		t.Fatalf("Sonic USDT must still sell: %v", err)
	}
	if got := lifi.lastRequest(); got.FromChainID != 146 || got.FromToken != sonicUSDT {
		t.Fatalf("LiFi was asked %+v", got)
	}
}

// The any-token naira sell starts no sale from wallet MCGP: Create prices
// through the real quoter, which refuses it, and no intent is written. USDT
// on the same chain still opens one.
func TestSellIntent_CreateRefusesWalletMCGP(t *testing.T) {
	r := newSellIntentRig(t)
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	q.chains = walletMCGPRegistry()
	q.SetRouterAllowlist(map[int64][]common.Address{146: {common.HexToAddress(sellTestDiamond)}})
	r.svc.quoter = q
	r.user.WalletAddress = sellTestAddress
	create := func(token string) (*models.RampSellIntent, error) {
		in, _, err := r.svc.Create(context.Background(), r.user, CreateSellIntentInput{
			SourceChainID: 146, SourceToken: token, SourceAmount: "10000000",
			SourceAddress: sellTestAddress, FiatCode: "NGN", BankCode: "044", AccountNumber: "0123456789"})
		return in, err
	}
	if in, err := create(sonicMCGP); !errors.Is(err, ErrSellWalletMCGP) || in != nil {
		t.Fatalf("intent = %+v err = %v, want ErrSellWalletMCGP", in, err)
	}
	var n int64
	r.svc.db.Model(&models.RampSellIntent{}).Count(&n)
	if n != 0 || lifi.quoteCalls() != 0 {
		t.Fatalf("intents = %d, LiFi asks = %d; a refused token must leave nothing behind", n, lifi.quoteCalls())
	}
	if in, err := create(sonicUSDT); err != nil || in == nil || !strings.EqualFold(in.SourceTokenAddress, sonicUSDT) {
		t.Fatalf("Sonic USDT: intent = %+v err = %v", in, err)
	}
}

// A token merely CALLED MCGP at another address is a different token: the gate
// matches the configured address, never the symbol.
func TestRampSellQuoter_RefuseSourceMatchesTheAddressNotTheSymbol(t *testing.T) {
	q := &RampSellQuoter{chains: walletMCGPRegistry()}
	if err := q.RefuseSource(146, "0x000000000000000000000000000000000000bEEF"); err != nil {
		t.Fatalf("a look-alike is not wallet MCGP: %v", err)
	}
	if err := q.RefuseSource(56, sonicMCGP); err != nil {
		t.Fatalf("the MCGP address on another chain is not wallet MCGP: %v", err)
	}
	if err := (*RampSellQuoter)(nil).RefuseSource(146, sonicMCGP); err != nil {
		t.Fatalf("a nil quoter refuses nothing (it prices nothing either): %v", err)
	}
}

// Wallet MCGP never pays a TSA Pay payment, on either settlement route: every
// quote and start refuses it before a payment is locked or a route is priced.
// Another token on the same chain still pays.
func TestPay_WalletMCGPIsRefusedBeforeAnythingIsLockedOrPriced(t *testing.T) {
	ctx := context.Background()
	source := func(r *payCheckoutRig, token string) PayConvertedInput {
		return PayConvertedInput{SourceChainID: 146, SourceToken: token, SourceAddress: r.alice.WalletAddress}
	}
	stillFree := func(t *testing.T, r *payCheckoutRig) {
		t.Helper()
		if got := r.get(t); got.Status != models.PayStatusPending || got.PayerUserID != nil || got.LockedAt != nil {
			t.Fatalf("a refused token must not take the payment: %+v", got)
		}
		if r.quoter.in != (SellQuoteInput{}) || r.intents.in.SourceToken != "" {
			t.Fatalf("nothing may be priced or started: quote %+v intent %+v", r.quoter.in, r.intents.in)
		}
	}

	t.Run("naira", func(t *testing.T) {
		r := newPayCheckoutRig(t)
		for _, tok := range []string{sonicMCGP, strings.ToLower(sonicMCGP)} {
			if _, err := r.svc.Quote(ctx, r.alice, r.p.ID, source(r, tok)); !errors.Is(err, ErrSellWalletMCGP) {
				t.Fatalf("quote %s: err = %v", tok, err)
			}
			if _, _, err := r.svc.PayConverted(ctx, r.alice, r.p.ID, source(r, tok)); !errors.Is(err, ErrSellWalletMCGP) {
				t.Fatalf("pay %s: err = %v", tok, err)
			}
		}
		stillFree(t, r)
		if _, err := r.svc.Quote(ctx, r.alice, r.p.ID, source(r, sonicUSDT)); err != nil {
			t.Fatalf("Sonic USDT quote: %v", err)
		}
		if _, _, err := r.svc.PayConverted(ctx, r.alice, r.p.ID, source(r, sonicUSDT)); err != nil || r.intents.in.SourceToken != sonicUSDT {
			t.Fatalf("Sonic USDT pay: err = %v intent %+v", err, r.intents.in)
		}
	})

	t.Run("crypto", func(t *testing.T) {
		r := newCryptoRig(t)
		if _, err := r.svc.CryptoQuote(ctx, r.alice, r.p.ID, source(r.payCheckoutRig, sonicMCGP)); !errors.Is(err, ErrSellWalletMCGP) {
			t.Fatalf("quote: err = %v", err)
		}
		if _, _, err := r.svc.PayCrypto(ctx, r.alice, r.p.ID, source(r.payCheckoutRig, sonicMCGP)); !errors.Is(err, ErrSellWalletMCGP) {
			t.Fatalf("pay: err = %v", err)
		}
		stillFree(t, r.payCheckoutRig)
		if _, _, err := r.svc.PayCrypto(ctx, r.alice, r.p.ID, source(r.payCheckoutRig, sonicUSDT)); err != nil || r.intents.in.SourceToken != sonicUSDT {
			t.Fatalf("Sonic USDT pay: err = %v intent %+v", err, r.intents.in)
		}
	})
}

// L1: the token must be spelled as a 0x address. A bare-hex (or symbol)
// spelling of MCGP would slip past RefuseSource's address match and could be
// resolved by LiFi itself, so it never reaches LiFi.
func TestRampSellQuote_RefusesATokenThatIsNotA0xAddress(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	q.chains = walletMCGPRegistry()
	q.SetRouterAllowlist(map[int64][]common.Address{146: {common.HexToAddress(sellTestDiamond)}})
	in := sellIn
	in.FromChainID = 146
	for _, tok := range []string{strings.TrimPrefix(sonicMCGP, "0x"), "MCGP", "", "0x1234"} {
		in.FromToken = tok
		if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellBadSourceAddress) && !errors.Is(err, ErrSellWalletMCGP) {
			t.Fatalf("%q: err = %v, want a refusal", tok, err)
		}
	}
	if lifi.quoteCalls() != 0 {
		t.Fatalf("LiFi was asked %d times for a token that is not an address", lifi.quoteCalls())
	}
}

// L1: what the route actually spends is checked too. A route for USDT whose
// action.fromToken turns out to be wallet MCGP is refused before she signs.
func TestRampSellQuote_RefusesARouteThatSpendsWalletMCGP(t *testing.T) {
	r1, r2 := sellRoute(t, 9.98), sellRoute(t, 9.97)
	r1.ApprovalToken, r2.ApprovalToken = sonicMCGP, sonicMCGP
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	q.chains = walletMCGPRegistry()
	q.SetRouterAllowlist(map[int64][]common.Address{146: {common.HexToAddress(sellTestDiamond)}})
	in := sellIn
	in.FromChainID = 146
	in.FromToken = sonicUSDT
	if res, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellWalletMCGP) {
		t.Fatalf("res = %+v err = %v, want ErrSellWalletMCGP", res, err)
	}
}
