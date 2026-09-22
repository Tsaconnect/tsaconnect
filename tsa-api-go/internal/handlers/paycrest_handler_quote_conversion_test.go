package handlers

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// paycrest_handler_quote_conversion_test.go — Task 12b: wiring
// PaycrestQuoteService.ConvertedVenues into GET /api/paycrest/quote.
//
// The app (tsa-app/services/paycrestApi.ts, getQuote) sends the pair it
// actually wants as requestedChain/requestedToken query params, mirroring the
// POST /buy body. These tests pin the wiring: a request naming a pair
// Paycrest cannot fill directly gets a converted venue back, and everything
// else — no requested pair, or conversion unconfigured, or a sell — is
// byte-for-byte what the handler already returned before this feature
// existed.

// stubPaycrestQuoter is a minimal services.PaycrestQuoter fake. The services
// package already has a fuller one (stubQuoter in paycrest_quote_test.go),
// but it is unexported there, so this package needs its own to build a real
// *services.PaycrestQuoteService for the handler to call.
type stubPaycrestQuoter struct {
	market float64
	book   []services.PaycrestBookEntry
}

func (s *stubPaycrestQuoter) Markets(context.Context) (*services.PaycrestMarkets, error) {
	return &services.PaycrestMarkets{Book: s.book}, nil
}

func (s *stubPaycrestQuoter) Rates(context.Context, string, string, float64, string) (*services.PaycrestRates, error) {
	return nil, services.ErrPaycrestNoProvider
}

func (s *stubPaycrestQuoter) MarketRate(context.Context, string, string) (float64, error) {
	return s.market, nil
}

// fakeLiFiQuoterForQuote stands in for LiFi's /quote client, needed here to
// call PaycrestQuoteService.SetLiFiQuoter from outside the services package
// (services.fakeLiFiQuoter serves the same role but is unexported there).
type fakeLiFiQuoterForQuote struct {
	route *services.LiFiRoute
	err   error
	calls int
}

func (f *fakeLiFiQuoterForQuote) Quote(context.Context, services.LiFiQuoteRequest) (*services.LiFiRoute, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.route, nil
}

// testConversionChainRegistry gives ConvertedVenues chain IDs/token addresses
// without a live RPC. Mirrors testChainRegistry in
// internal/services/paycrest_quote_conversion_test.go, rebuilt through the
// exported constructor since BlockchainService's fields are unexported to
// this package. NewEVMClient dials lazily for an http:// URL (no real network
// call at construction time), so a bogus RPC URL is safe here.
func testConversionChainRegistry() *services.BlockchainService {
	return services.NewBlockchainService(&config.Config{Networks: map[string]config.NetworkConfig{
		"mainnet": {
			Chains: map[string]config.ChainConfig{
				"base":     {ChainID: 8453, RPCURL: "http://127.0.0.1:1"},
				"optimism": {ChainID: 10, RPCURL: "http://127.0.0.1:1"},
			},
			TokenAddresses: map[string]string{
				"base:USDC":     "0xBaseUSDC00000000000000000000000000001",
				"optimism:USDT": "0xOptUSDT000000000000000000000000000001",
			},
		},
	}})
}

// fakeTreasuryLaneChecker stands in for *services.Treasury in
// ConvertedVenues' own gate (services.PaycrestQuoteService.SetTreasuryLanes)
// — needed here to exercise the handler's conversion wiring from outside the
// services package, where services.treasuryLaneChecker is unexported but its
// method set is still satisfiable structurally.
type fakeTreasuryLaneChecker struct {
	laned map[string]bool
}

func (f *fakeTreasuryLaneChecker) CanPayForGas(_ context.Context, chainKey string, _ uint64) bool {
	return f.laned[chainKey]
}

func (f *fakeTreasuryLaneChecker) Lane(chainKey string) *services.TreasuryLane {
	if f.laned[chainKey] {
		return &services.TreasuryLane{}
	}
	return nil
}

// newConvertibleQuoteService wires a service that quotes 100 USDC on base for
// NGN 150,000 (rate 1,500), with conversion fully configured (LiFi quoter,
// treasury address, chain registry, treasury lanes) — everything
// buildRampConversion wires at boot when RAMP_CONVERSION_BUY is on.
func newConvertibleQuoteService(t *testing.T) (*services.PaycrestQuoteService, *fakeLiFiQuoterForQuote) {
	t.Helper()
	pc := &stubPaycrestQuoter{
		market: 1500,
		book: []services.PaycrestBookEntry{
			{ProviderID: "p1", Side: "buy", Token: "USDC", Fiat: "NGN", Network: "base", Rate: "1500", Min: "0.5", Max: "100000"},
			{ProviderID: "p1", Side: "sell", Token: "USDC", Fiat: "NGN", Network: "base", Rate: "1490", Min: "0.5", Max: "100000"},
		},
	}
	svc := services.NewPaycrestQuoteService(pc, []string{"USDC"}, []string{"base"})
	lifi := &fakeLiFiQuoterForQuote{}
	svc.SetLiFiQuoter(lifi)
	svc.SetTreasuryAddress("0xTreasuryAddress0000000000000000000001")
	svc.SetChainRegistry(testConversionChainRegistry())
	svc.SetTreasuryLanes(&fakeTreasuryLaneChecker{laned: map[string]bool{"base": true, "optimism": true}})
	svc.SetRoundTripBudgetBps(50)
	return svc, lifi
}

// optimismUSDTRoute is a route the venue pricer will actually accept: priced
// in USD (so its cost is knowable), with a gas limit, and with calldata that
// decodes to a plain bridge onto chain 10 and no destination call.
func optimismUSDTRoute(t *testing.T) *services.LiFiRoute {
	t.Helper()
	return &services.LiFiRoute{
		ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000,
		ETASeconds: 30, Tool: "across",
		Data: testBridgeCalldataHex(t, 10, false),
	}
}

// testBridgeCalldataHex builds an ILiFi.BridgeData call the way
// services.testBridgeCalldata does — rebuilt here because that helper, and
// the ABI type it packs, are unexported to this package.
func testBridgeCalldataHex(t *testing.T, destChainID int64, hasDestinationCall bool) string {
	t.Helper()
	tuple, err := abi.NewType("tuple", "", []abi.ArgumentMarshaling{
		{Name: "transactionId", Type: "bytes32"},
		{Name: "bridge", Type: "string"},
		{Name: "integrator", Type: "string"},
		{Name: "referrer", Type: "address"},
		{Name: "sendingAssetId", Type: "address"},
		{Name: "receiver", Type: "address"},
		{Name: "minAmount", Type: "uint256"},
		{Name: "destinationChainId", Type: "uint256"},
		{Name: "hasSourceSwaps", Type: "bool"},
		{Name: "hasDestinationCall", Type: "bool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	packed, err := abi.Arguments{{Type: tuple}}.Pack(struct {
		TransactionId      [32]byte
		Bridge             string
		Integrator         string
		Referrer           common.Address
		SendingAssetId     common.Address
		Receiver           common.Address
		MinAmount          *big.Int
		DestinationChainId *big.Int
		HasSourceSwaps     bool
		HasDestinationCall bool
	}{
		Bridge: "across", Integrator: "tsa",
		Receiver:           common.HexToAddress(quoteTestUserWallet),
		MinAmount:          big.NewInt(1),
		DestinationChainId: big.NewInt(destChainID),
		HasDestinationCall: hasDestinationCall,
	})
	if err != nil {
		t.Fatal(err)
	}
	return hexutil.Encode(append([]byte{0x17, 0x94, 0x95, 0x8f}, packed...))
}

const quoteTestUserWallet = "0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A"

type quoteResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Currency   string                   `json:"currency"`
		Amount     string                   `json:"amount"`
		Side       string                   `json:"side"`
		MarketRate float64                  `json:"marketRate"`
		Venues     []services.PaycrestVenue `json:"venues"`
		Best       *services.PaycrestVenue  `json:"best"`
	} `json:"data"`
}

func doQuoteRequest(t *testing.T, h *PaycrestHandler, rawQuery string) quoteResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/paycrest/quote?"+rawQuery, nil)

	h.Quote(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp quoteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	return resp
}

// doConvertedQuoteRequest calls the authenticated converted quote with a
// signed-in user in context, the way the auth middleware leaves it.
func doConvertedQuoteRequest(t *testing.T, h *PaycrestHandler, rawQuery string) quoteResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/paycrest/converted-quote?"+rawQuery, nil)
	c.Set("user", &models.User{ID: uuid.New(), WalletAddress: quoteTestUserWallet})

	h.ConvertedQuote(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp quoteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	return resp
}

// The public quote serves DIRECT venues only. Pricing a converted venue calls
// LiFi for a client-chosen token, which must never be free to trigger — so
// the requested-asset params are ignored here however they are sent, and the
// converted price lives behind auth on /converted-quote.
func TestPaycrestHandler_Quote_IsDirectVenuesOnly(t *testing.T) {
	svc, lifi := newConvertibleQuoteService(t)
	lifi.route = optimismUSDTRoute(t)
	h := NewPaycrestHandler(nil, svc, nil)

	resp := doQuoteRequest(t, h, "side=buy&amount=150000&currency=NGN&requestedChain=optimism&requestedToken=USDT")

	if !resp.Success {
		t.Fatalf("expected success response")
	}
	if len(resp.Data.Venues) != 1 || resp.Data.Venues[0].Kind != models.RouteKindDirect {
		t.Fatalf("the public quote must serve direct venues only, got %+v", resp.Data.Venues)
	}
	if lifi.calls != 0 {
		t.Errorf("the public quote spent %d LiFi calls; it must spend none", lifi.calls)
	}
}

// The authenticated converted quote prices ONE requested asset, named by
// chain id and token address, and appends it to the direct venues.
func TestPaycrestHandler_ConvertedQuote_PricesTheRequestedAsset(t *testing.T) {
	svc, lifi := newConvertibleQuoteService(t)
	lifi.route = optimismUSDTRoute(t)
	h := NewPaycrestHandler(nil, svc, nil)

	resp := doConvertedQuoteRequest(t, h,
		"amount=150000&requestedChainId=10&requestedTokenAddress=0xOptUSDT000000000000000000000000000001")

	if !resp.Success {
		t.Fatalf("expected success response")
	}
	var converted *services.PaycrestVenue
	for i := range resp.Data.Venues {
		if resp.Data.Venues[i].Kind == models.RouteKindConverted {
			converted = &resp.Data.Venues[i]
		}
	}
	if converted == nil {
		t.Fatalf("no converted venue in response: %+v", resp.Data.Venues)
	}
	if converted.RequestedChain != "optimism" || converted.RequestedChainID != 10 || converted.RequestedToken != "USDT" {
		t.Fatalf("converted venue = %+v, want the asset resolved from chain id 10", converted)
	}
	if converted.ViaChain != "base" || converted.ViaToken != "USDC" {
		t.Fatalf("converted venue must say what it settles as: %+v", converted)
	}
	if converted.PromisedAmount == "" {
		t.Fatalf("a stablecoin destination must carry a promised amount: %+v", converted)
	}
	// Best must stay the direct venue — a converted route is never preferred
	// over one Paycrest can fill outright.
	if resp.Data.Best == nil || resp.Data.Best.Kind == models.RouteKindConverted {
		t.Fatalf("best must remain the direct venue, got %+v", resp.Data.Best)
	}
}

// An unresolvable asset is not an error for the whole quote: the direct
// venues are still the right answer, and no LiFi call is spent on it.
func TestPaycrestHandler_ConvertedQuote_UnknownAssetStillReturnsDirectVenues(t *testing.T) {
	svc, lifi := newConvertibleQuoteService(t)
	lifi.route = optimismUSDTRoute(t)
	h := NewPaycrestHandler(nil, svc, nil)

	resp := doConvertedQuoteRequest(t, h, "amount=150000&requestedChainId=424242&requestedTokenAddress="+
		"0x0000000000000000000000000000000000000000")

	if len(resp.Data.Venues) != 1 || resp.Data.Venues[0].Kind != models.RouteKindDirect {
		t.Fatalf("want only the direct venue, got %+v", resp.Data.Venues)
	}
	if lifi.calls != 0 {
		t.Errorf("spent a LiFi call on a chain id that does not resolve")
	}
}

// The merge-safety property for the whole branch: with no requestedChain/
// requestedToken — an app build that predates conversion, or simply a plain
// direct-fill request — the response must be exactly what the handler
// returned before this feature existed: same venue count, no extra work, no
// error. This holds regardless of whether the quote service happens to have
// conversion wired up.
func TestPaycrestHandler_Quote_NoRequestedAsset_LeavesResponseUnchanged(t *testing.T) {
	svc, lifi := newConvertibleQuoteService(t)
	lifi.route = &services.LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000)}
	h := NewPaycrestHandler(nil, svc, nil)

	resp := doQuoteRequest(t, h, "side=buy&amount=150000&currency=NGN")

	if len(resp.Data.Venues) != 1 {
		t.Fatalf("want exactly the 1 direct venue with no requested asset, got %d: %+v", len(resp.Data.Venues), resp.Data.Venues)
	}
	if resp.Data.Venues[0].Kind != models.RouteKindDirect {
		t.Fatalf("the one venue must be direct: %+v", resp.Data.Venues[0])
	}
	if lifi.calls != 0 {
		t.Errorf("spent a LiFi call although no requestedChain/requestedToken was asked for")
	}
}

// Conversion disabled (no LiFi quoter / treasury / chain registry wired — the
// RAMP_CONVERSION_BUY-off boot shape) must return exactly today's response
// even when the caller DOES send requestedChain/requestedToken: ConvertedVenues
// itself is a documented no-op then, and the handler must not error or alter
// the venues on the strength of query params alone.
func TestPaycrestHandler_Quote_ConversionUnconfigured_RequestedAssetIsIgnored(t *testing.T) {
	pc := &stubPaycrestQuoter{
		market: 1500,
		book: []services.PaycrestBookEntry{
			{ProviderID: "p1", Side: "buy", Token: "USDC", Fiat: "NGN", Network: "base", Rate: "1500", Min: "0.5", Max: "100000"},
		},
	}
	svc := services.NewPaycrestQuoteService(pc, []string{"USDC"}, []string{"base"})
	// Deliberately no SetLiFiQuoter / SetTreasuryAddress / SetChainRegistry —
	// the RAMP_CONVERSION_BUY-off boot shape from cmd/server/ramp_conversion.go.
	h := NewPaycrestHandler(nil, svc, nil)

	withParams := doQuoteRequest(t, h, "side=buy&amount=150000&currency=NGN&requestedChain=optimism&requestedToken=USDT")
	withoutParams := doQuoteRequest(t, h, "side=buy&amount=150000&currency=NGN")

	if len(withParams.Data.Venues) != 1 || len(withoutParams.Data.Venues) != 1 {
		t.Fatalf("conversion-unconfigured quote must stay 1 direct venue regardless of requested-asset params: with=%+v without=%+v",
			withParams.Data.Venues, withoutParams.Data.Venues)
	}
	withJSON, _ := json.Marshal(withParams.Data.Venues[0])
	withoutJSON, _ := json.Marshal(withoutParams.Data.Venues[0])
	if string(withJSON) != string(withoutJSON) {
		t.Fatalf("requested-asset params changed the venue when conversion is unconfigured:\nwith:    %s\nwithout: %s",
			withJSON, withoutJSON)
	}
}

// Conversion is a buy-only capability — CreateSellInput/paycrestSellBody has
// no requestedChain/requestedToken, so an offramp quote must never gain a
// converted venue even if a caller sends the params.
func TestPaycrestHandler_Quote_SellSide_NeverOffersConvertedVenue(t *testing.T) {
	svc, lifi := newConvertibleQuoteService(t)
	lifi.route = &services.LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000)}
	h := NewPaycrestHandler(nil, svc, nil)

	resp := doQuoteRequest(t, h, "side=sell&amount=150000&currency=NGN&requestedChain=optimism&requestedToken=USDT")

	if len(resp.Data.Venues) != 1 {
		t.Fatalf("sell quote must never gain a converted venue, got %d: %+v", len(resp.Data.Venues), resp.Data.Venues)
	}
	if lifi.calls != 0 {
		t.Errorf("spent a LiFi call on a sell quote, which conversion never applies to")
	}
}

// A LiFi failure must degrade to "no converted venue offered", never an error
// for the whole quote — the direct venues are still a valid answer.
func TestPaycrestHandler_Quote_LiFiFailure_StillReturnsDirectVenues(t *testing.T) {
	svc, lifi := newConvertibleQuoteService(t)
	lifi.err = services.ErrLiFiNoRoute
	h := NewPaycrestHandler(nil, svc, nil)

	resp := doQuoteRequest(t, h, "side=buy&amount=150000&currency=NGN&requestedChain=optimism&requestedToken=USDT")

	if !resp.Success {
		t.Fatalf("expected success response")
	}
	if len(resp.Data.Venues) != 1 {
		t.Fatalf("a LiFi failure must leave only the direct venue, got %+v", resp.Data.Venues)
	}
}
