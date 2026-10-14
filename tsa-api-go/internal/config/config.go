package config

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// chainPreset bundles every value that has to flip together for one chain's
// P2P deployment (Sonic or BSC). Flipping piecemeal makes the EIP-712 voucher
// domain mismatch what the contract recovers, so every signature gets rejected.
type chainPreset struct {
	rpcURL      string
	chainID     int64
	usdcAddress string
	usdtAddress string
	mcgpAddress string
	p2pEscrow   string
	// eip712Version is the EIP-712 domain `version` string the deployed
	// P2PEscrow at p2pEscrow was initialized with. It MUST move in lockstep
	// with p2pEscrow: v1 deployed "1", the two-sided v2 deployed "2". The
	// backend stamps this into the voucher typed-data the app signs; a value
	// that disagrees with the on-chain contract makes ecrecover resolve to a
	// wrong address and rejects every sell-ad signature.
	eip712Version string
	// purpose: P2PPurposeP2P (the default when empty). assets are the row's
	// tradeable symbols.
	purpose string
	assets  []string
	// Legacy env aliases (Sonic only): an operator hot-fix override of the
	// escrow, its EIP-712 version and the chain id, kept exactly as before.
	escrowEnv, versionEnv, chainIDEnv string
}

// sonicPresets holds the canonical Sonic P2P deployment for each network.
// To flip the server between testnet and mainnet, set SONIC_NETWORK and
// restart — no other env vars need to change. Individual addresses can
// still be overridden via the legacy SONIC_RPC_URL / *_TOKEN_ADDRESS /
// P2P_ESCROW_ADDRESS env vars (escape hatch for hot-fixes).
var sonicPresets = map[string]chainPreset{
	"mainnet": {
		rpcURL:      "https://rpc.soniclabs.com",
		chainID:     146,
		usdcAddress: "0x29219dd400f2Bf60E5a23d13Be72B486D4038894",
		usdtAddress: "0x6047828dc181963ba44974801ff68e538da5eaf9",
		mcgpAddress: "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
		// P2PEscrowV3 (two-sided) — deployed to Sonic mainnet 2026-06-30. The
		// contract initializes its EIP-712 domain as version "2"
		// (P2PEscrowV3.sol: __EIP712_init("TSAConnectP2PEscrow", "2")), so the
		// voucher domain version stays "2" despite the V3 contract name.
		p2pEscrow:     "0x7dAdae104930da7B62EA185fa697fCFA7f9eC324",
		eip712Version: "2",
		// assets gates NEW P2P activity only (ads, pool deposits, trade
		// opens). MCGP was removed 2026-09-28 by owner decision: wallet MCGP
		// must not be usable in P2P. Existing MCGP ads, pool balances and
		// in-flight trades still settle and withdraw — those exit paths
		// never consult this list.
		assets:     []string{"USDT", "USDC"},
		escrowEnv:  "P2P_ESCROW_ADDRESS",
		versionEnv: "P2P_ESCROW_EIP712_VERSION",
		chainIDEnv: "SONIC_CHAIN_ID",
	},
	"testnet": {
		rpcURL:      "https://rpc.testnet.soniclabs.com",
		chainID:     14601,
		usdcAddress: "0x685CD180e603A15507f575E7A347610b83d36d41",
		usdtAddress: "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad",
		mcgpAddress: "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
		// P2PEscrowV2 (two-sided) — deployed 2026-05-29, UUPS proxy,
		// initialized with EIP-712 domain version "2".
		p2pEscrow:     "0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7",
		eip712Version: "2",
		assets:        []string{"USDT", "USDC"},
		escrowEnv:     "P2P_ESCROW_ADDRESS",
		versionEnv:    "P2P_ESCROW_EIP712_VERSION",
		chainIDEnv:    "SONIC_CHAIN_ID",
	},
}

// P2PChainConfig is the per-chain P2P deployment the trade/escrow services
// resolve by chainID. ChainID + EscrowAddress + EIP712Version move in lockstep
// (a mismatch rejects every voucher). Assets is the curated allow-list of
// tradeable asset symbols on this chain — no asset is split across chains.
type P2PChainConfig struct {
	ChainID       int64
	ChainKey      string // blockchainService key: "sonic", "bsc"
	EscrowAddress string
	EIP712Version string
	Assets        []string // e.g. ["USDT","USDC"] on Sonic; a pool row's pool tokens
	// Purpose is P2PPurposeP2P; Network names the cfg.Networks entry the chain
	// was taken from.
	Purpose string
	Network string
}

// FeeManagerChainConfig is the per-chain FeeManager deployment the configurable-
// FeeManager admin flow + drift reconciler resolve by chainID (design §3/§5/§10).
// The FeeManager is the on-chain SOURCE OF TRUTH for every fee rate; the backend
// builds unsigned setRate txs against Address and reads back rateFor to confirm.
//
// Address is EMPTY until a FeeManager is deployed to that chain — an empty
// address keeps the whole flow DORMANT for that chain (Load omits it from
// FeeManagerChains, the admin endpoints surface "not configured", and the drift
// reconciler no-ops), exactly like the BSC P2P escrow dormant-by-default pattern.
type FeeManagerChainConfig struct {
	ChainID  int64
	ChainKey string // blockchainService chain key: "sonic", "bsc"
	Address  string // FeeManager proxy address on this chain
}

// validateFeeManagerAddress rejects a FEE_MANAGER_ADDRESS_* env value that is
// not a syntactically valid 20-byte hex address, or that is the zero address.
// common.HexToAddress NEVER errors — it silently pads/truncates — so the admin
// set-fee flow could otherwise build setRate txs against a wrong/zero contract.
// Returns a descriptive error; the caller fails the boot loudly (design §6).
func validateFeeManagerAddress(envName, addr string) error {
	if !common.IsHexAddress(addr) {
		return fmt.Errorf("%s=%q is not a valid hex address (common.HexToAddress would silently pad/truncate it); fix or unset it", envName, addr)
	}
	if common.HexToAddress(addr) == (common.Address{}) {
		return fmt.Errorf("%s=%q resolves to the zero address; a FeeManager address must be non-zero", envName, addr)
	}
	return nil
}

// ChainConfig holds the configuration for a single EVM-compatible blockchain.
type ChainConfig struct {
	Name           string
	RPCURL         string
	ChainID        int64
	NativeCurrency string // "S", "BNB", "tBNB", etc.
}

// NetworkConfig holds chains and token addresses for a single network environment.
type NetworkConfig struct {
	Chains         map[string]ChainConfig
	TokenAddresses map[string]string // "chain:symbol" → contract address
	// FlutterwavePolygonUSDC is the Polygon USDC contract Flutterwave delivers
	// a stable buy in. Converted route 2 (spec 2026-10-03 §15, N5) stays off
	// unless it equals the conversion treasury's Polygon USDC. "" = none.
	FlutterwavePolygonUSDC string
}

// IsWalletMCGP reports whether token on the chain with chainID is the plain
// ERC-20 MCGP a user holds in her own wallet (the configured "<chain>:MCGP"
// address, on any network). Owner decision 2026-09-28: that MCGP is never used
// to pay or sold for naira — only Spendable MCGP (the entitlement balance) is.
// It may still be seen, sent and swapped. Matched by address, never by symbol,
// so a look-alike token called "MCGP" is not mistaken for it.
func (c *Config) IsWalletMCGP(chainID int64, token string) bool {
	token = strings.TrimSpace(token)
	if c == nil || chainID <= 0 || token == "" {
		return false
	}
	for _, network := range c.Networks {
		for name, chain := range network.Chains {
			if chain.ChainID != chainID {
				continue
			}
			if addr := strings.TrimSpace(network.TokenAddresses[name+":MCGP"]); addr != "" && strings.EqualFold(addr, token) {
				return true
			}
		}
	}
	return false
}

// NonEVMChainConfig configures one non-EVM chain (Tron/BTC/Solana). Populated
// only when its RPC/indexer env is present, so absent chains stay dormant.
type NonEVMChainConfig struct {
	ChainKey       string
	Family         string
	Network        string // "mainnet" | "testnet"
	RPCURL         string // TronGrid base, mempool base, or Solana RPC
	APIKey         string // optional provider key
	NativeSymbol   string
	NativeDecimals int
	Tokens         map[string]string // symbol → on-chain token contract/mint
}

// FeeQuoteConfig tunes the deposit/withdrawal fee-quote engine (PM fee spec,
// batch 11). All rates are basis points (1% = 100 bps); integer math only —
// the calculator never touches float64 for money.
type FeeQuoteConfig struct {
	// PlatformStableUSD is the flat platform fee on stablecoin (USDC/USDT)
	// flows, in whole USD.
	// PM-UNRESOLVED: the spec gives both $1 and $2 — default $2 until the PM
	// settles it (FEE_PLATFORM_STABLE_USD).
	PlatformStableUSD int64
	// PlatformNgnBps / PlatformFxBps are the platform's own cut on the MCGP
	// fiat ramps, tiered by PAYMENT CURRENCY (PM spec, 28/07): 0.5% on NGN,
	// 2% on every other currency. This is the only line the platform keeps —
	// the provider fee is passthrough and the tax is remitted — so it is what
	// funds cashback, TradePoints and the ecosystem generally.
	//
	// It applies to BOTH legs ("same fees apply"): withheld as un-issued MCGP
	// on the contract for deposits, and left in the payout balance as fiat for
	// withdrawals. FEE_PLATFORM_NGN_BPS / FEE_PLATFORM_FX_BPS.
	PlatformNgnBps int64
	PlatformFxBps  int64
	// ProviderFallbackBps prices the provider (Flutterwave) fee when a live
	// GetFees quote is unavailable (client dormant or the call fails).
	ProviderFallbackBps int64
	// TaxBasis selects what the VAT + platform-tax percentages apply to:
	// "fee" — the provider fee (matches the PM's receipt: ₦0.75 VAT on a ₦10
	// provider fee for a ₦500 deposit), or "amount" — the gross amount
	// (matches the PM's prose).
	// PM-UNRESOLVED: default "fee", per the receipt evidence (FEE_TAX_BASIS).
	TaxBasis string
	// VATBps + PlatformTaxBps make up the "tax" line: 7.5% VAT + 0.5% platform.
	VATBps         int64
	PlatformTaxBps int64
	// McgpSwapBps is the MCGP→stable swap leg on MCGP withdrawals
	// (admin-adjustable); LifiRouteBps is the LiFi routing leg. An MCGP
	// withdrawal pays both (default 2% + 3% = 5%). A stablecoin withdrawal
	// pays only the LiFi leg when the funds are already on Polygon, both
	// otherwise.
	McgpSwapBps  int64
	LifiRouteBps int64
	// StableDepositConvBps is the conversion fee on stablecoin deposits.
	StableDepositConvBps int64
}

type Config struct {
	DatabaseURL string
	JWTSecret   string
	FrontendURL string
	AdminURL    string
	LandingURL  string
	// WebAppOrigin is the TSA Connect web app's origin (e.g.
	// https://app.tsaconnectworld.com). Unset = the web sign-in routes are not
	// registered.
	WebAppOrigin string
	// NetlifyProxySecret is the secret the web app's Netlify site signs its
	// /api proxy requests with (x-nf-sign). Set = a request whose signature
	// verifies is attributed to the visitor Netlify names, not to Netlify.
	// Unset = Netlify's own address is the client, and every web visitor
	// shares it for per-IP limits.
	NetlifyProxySecret string
	Port               string
	// Env is normalised at Load() time (trim, lower-case, "prod" -> "production")
	// so a stray space or differing case can never desync the checks below —
	// call IsProduction() rather than comparing this field directly.
	Env string
	// AllowRawSignerKeys opts a production process back into raw-env-var private
	// keys (IT operator, relayer, ramp sponsor, PM release local signer) until a
	// KMS-backed signer exists. Ignored outside production. See
	// rawSignerKeyAllowed in cmd/server for the single place this is consumed.
	AllowRawSignerKeys  bool
	CloudinaryCloudName string
	CloudinaryAPIKey    string
	CloudinaryAPISecret string

	// Sonic blockchain configuration
	SonicRPCURL         string
	SonicChainID        int64
	MCGPTokenAddress    string
	USDTTokenAddress    string
	USDCTokenAddress    string
	SystemWalletAddress string

	// BSC blockchain
	BSCRPCURL  string
	BSCChainID int64

	// Smart contract addresses
	ProductEscrowAddress   string
	ServiceContractAddress string
	OTCMarketplaceAddress  string
	P2PEscrowAddress       string

	// OTCMarketplaceDeployBlock is where the OTC sell cap starts reading the
	// desk's Bought/Sold logs. 0 = scan from genesis (correct, just slower);
	// the known mainnet desk defaults to its creation block.
	OTCMarketplaceDeployBlock uint64
	// OTCLogMaxBlockRange is the first eth_getLogs span tried; 0 = the whole
	// remaining range. The scanner halves on any error, so an RPC with a
	// range cap still works — this only saves the probing round trips.
	OTCLogMaxBlockRange uint64

	// Spendable MCGP checkout abuse bounds (services.OrderMcgpAbuseLimits).
	// A value <= 0 (or a share outside 1..100) keeps the built-in default.
	// OrderMcgpMaxOpenHolds: unfinished Spendable payments one buyer may hold.
	// OrderMcgpMaxRefundsPerDay: refunds/cancels in 24h after which the buyer
	// cannot start another Spendable payment. OrderMcgpWindowSharePct: the part
	// of the contract's debit and credit windows checkout may use; the rest is
	// kept for bills and IT.
	OrderMcgpMaxOpenHolds     int
	OrderMcgpMaxRefundsPerDay int
	OrderMcgpWindowSharePct   int

	// MCGPSwapBAddress is the deployed McgpEntitlementSwap ("Swap Contract B")
	// proxy/contract — the entitlement-ledger swap that mints spendable premium
	// MCGP (buyPremium) and redeems legacy holders at the legacy price. UNSET =
	// the /api/mcgp routes are dormant (not registered). See the approved plan
	// and project memory project_mcgp-differentiated-swap.
	MCGPSwapBAddress string

	// Gasless relaying (EIP-2771). All dormant unless BOTH the forwarder address
	// and the relayer key are set — with either missing the /mcgp/relay endpoints
	// report relayerEnabled:false and the app self-pays, exactly as before.
	//
	// RelayerPrivateKey MUST NOT be the owner, operator or guardian key: it is a
	// hot key broadcasting from a server, and sharing it would turn a relayer
	// compromise into an operator one. Asserted on-chain at boot.
	MCGPForwarderAddress   string
	RelayerPrivateKey      string
	RelayerMaxDeadline     time.Duration
	RelayerGasCeiling      uint64
	RelayerPerUserPerHour  int
	RelayerDailyGasBudget  string // decimal gas units per UTC day
	RelayerBalanceFloorWei string // refuse to relay below this native balance
	RelayerMinSponsored    string // smallest amount worth sponsoring (base units)

	// Ramp gas sponsorship — Paycrest sells pay no gas on these chains.
	// Dormant unless RAMP_GAS_SPONSORSHIP=true AND RAMP_SPONSOR_PRIVATE_KEY is set.
	RampGasSponsorshipEnabled bool
	RampSponsorPrivateKey     string   // MUST differ from relayer/IT operator/PM release keys
	RampSponsorChains         []string // chain keys
	// RampSponsorDailyGasBudget is gas units per UTC day, per chain. At ~120k
	// gas per relay the old 50M default capped each chain at ~416 sponsored
	// sales a day. It is a backstop against a runaway bug, not a spend control:
	// the balance floor and float alert are what protect the money.
	RampSponsorDailyGasBudget string
	// RampSponsorTopUpUnusedDailyCap is how many gas top-ups a user may leave
	// unused (their sell order expired, was cancelled or failed with nothing
	// paid) in a rolling 24h before further top-ups are refused. Top-ups that
	// led to a deposit never count, so users completing real sales are never
	// capped. Ramp lanes have no per-user hourly window.
	RampSponsorTopUpUnusedDailyCap int
	RampSponsorBalanceFloorWei     map[string]string // chain -> wei; sponsoring stops below this
	RampSponsorTopUpMaxWei         map[string]string // chain -> wei; largest single top-up

	// Marketplace checkout funding — "pay with another token" (owner decision
	// 2026-09-29). The buyer's token is swapped through LiFi into the order
	// token in HER OWN wallet on Sonic, then the unchanged escrow is paid from
	// it. On by default (MARKETPLACE_FUNDING=false turns it off); it needs no
	// credential beyond LiFi, which works anonymously.
	//
	// There is no cap on what the round trip may cost as a share of the order:
	// she sees LiFi's cost plus the gas we pay for her before signing, keeps no
	// margin for us, and is refused only when her wallet cannot cover it.
	// MarketplaceEscrowGasPerOrder is the gas one order's approve + createOrder
	// takes on Sonic, what the destination top-up is sized from (x2 buffer).
	//
	// Off by default (flag-off-first, like gas sponsorship and converted buys).
	MarketplaceFundingEnabled    bool
	MarketplaceEscrowGasPerOrder uint64
	// MarketplaceSonicGas* is the funding's OWN Sonic gas lane: S sent to a
	// buyer whose funds landed but who holds none to sign the escrow txs. It is
	// deliberately NOT a RAMP_SPONSOR_CHAINS lane — that would also give sells
	// and TSA Pay intents from Sonic a source-chain top-up. It signs with the
	// ramp sponsor key (RAMP_SPONSOR_PRIVATE_KEY) under its own guard, floor and
	// ceiling. Off by default; off, a buyer with no S is refused before signing.
	MarketplaceSonicGasEnabled bool
	// SonicGas* are the ONE shared Sonic sponsor lane's floor and per-send
	// ceiling (P2P top-ups and, while MARKETPLACE_SONIC_GAS is on, checkout
	// funding). Env SONIC_GAS_FLOOR_WEI / SONIC_GAS_TOPUP_MAX_WEI; the old
	// MARKETPLACE_SONIC_GAS_* names are read as deprecated fallbacks.
	SonicGasFloorWei    string
	SonicGasTopUpMaxWei string

	// Ramp treasury / converted buys — Paycrest delivers into our own
	// treasury instead of the user's wallet, and the treasury bridges it to
	// the requested chain/token via LiFi. Dormant unless RAMP_CONVERSION_BUY
	// is true AND RAMP_TREASURY_PRIVATE_KEY is set.
	//
	// RampTreasuryPrivateKey MUST differ from the relayer, IT operator, PM
	// release AND ramp sponsor keys: unlike the sponsor (which only ever
	// pays gas), the treasury holds USER FUNDS in transit, so sharing its
	// key with any other hot signer turns a single compromise into one that
	// drains customer money. Asserted at boot (assertDistinctKey).
	RampConversionBuyEnabled bool
	// Ramp router (docs/specs/2026-10-02-ramp-router-design.md §5).
	RampPriceToleranceBps  int
	RampPriceGuard         string // "log" (default) | "enforce"
	RampProviderTimeout    time.Duration
	RampOpenAttemptTimeout time.Duration
	RampOpenBudget         time.Duration
	// Flutterwave and the company wallet as ramp buy providers
	// (docs/specs/2026-10-03-ramp-flutterwave-provider-design.md §9).
	RampFlutterwaveBuy              bool
	RampFlutterwaveMinTokens        string
	RampFlutterwaveMaxUSD           string
	RampFlutterwaveSendFeeUSD       string
	RampFlutterwaveConversionFeeBps int64
	RampFlutterwaveSendFeeMode      string
	RampDailyAbandonCap             int
	RampHonourGrace                 time.Duration
	// Owner ruling 2026-10-07: every ramp provider prices at its base rate
	// plus RampCompanyTakeBps (our only margin; resolved ONCE here to
	// 1..MaxCompanyTakeBps, so every provider reads the same value) and passes
	// third-party costs on at cost. The funding fee is the cost of the channel
	// her naira arrives through: RampFundingFeeBps for the central account,
	// RampVAFundingFeeBps for her virtual account.
	RampCompanyTakeBps          int64
	RampFundingFeeBps           int64
	RampVAFundingFeeBps         int64
	RampTreasuryMinNGN          string
	RampTreasuryDailyUserMaxUSD string
	RampTreasuryLowBalanceUSD   float64
	RampTreasurySendStall       time.Duration
	RampTreasuryRateTolBps      int64
	RampTreasuryConfirmations   int
	RampTreasuryPrivateKey      string
	RampTreasuryChains          []string
	// RampTreasuryMaxInflightUSDPerOrder mirrors Paycrest's own onramp cap.
	// RampTreasuryMaxInflightUSDPerChain bounds this chain's TOTAL exposure
	// across every converted buy the treasury has not yet settled.
	RampTreasuryMaxInflightUSDPerOrder float64
	RampTreasuryMaxInflightUSDPerChain float64
	// RampTreasuryNativeFloorWei is the native balance below which a lane
	// raises a refill ALERT. It is a warning, not a stop: a lane keeps
	// sending as long as it can pay for the transaction in hand (see
	// Treasury.requireGas). Same shape as RampSponsorBalanceFloorWei but a
	// separate map: a treasury lane signs a bridge tx, not a simple relay.
	RampTreasuryNativeFloorWei map[string]string
	// RampTreasuryRouters is the per-chain allowlist of contract addresses a
	// LiFi route may ask the treasury to call or approve. A quote names its
	// own `to` and `approvalAddress`; without this the treasury would sign
	// whatever a compromised or misdirected LiFi answer returned, including
	// a token contract with transfer(attacker, balance) as its calldata,
	// which drains the whole chain float rather than one order's amount.
	// A chain with an empty allowlist gets no lane at all — fail closed.
	RampTreasuryRouters map[string][]string
	// RampConversionRetryWindow (Decision 10) is how long a converted buy may
	// sit with no broadcast bridge attempt before it is refunded instead of
	// retried again.
	RampConversionRetryWindow time.Duration
	// RampConversionMinUSD/RampConversionMinUSDByChain are Decision 6's floor
	// on a converted buy, measured on the settlement leg's stablecoin amount.
	// ByChain always holds an entry for every chain in RampTreasuryChains
	// (defaulting to RampConversionMinUSD unless overridden), so
	// PaycrestQuoteService.SetConversionMinimums never has to guess a chain
	// it wasn't told about.
	RampConversionMinUSD        float64
	RampConversionMinUSDByChain map[string]float64
	// RampConvertedFeeMaxBps/RampConvertedFeeMaxBpsByChain cap the round trip
	// a converted buy or swapped sell may pass on at cost (LiFi's cost plus
	// the gas we pay; a buy adds 10 bps headroom) — on top of the company
	// take, which is never in it — for the chain involved: a buy's
	// destination, a sell's source; past it the route is not offered. ByChain
	// holds every chain a buy can deliver to or a sell can start from. A TSA
	// Pay payout keeps its own split under the same cap (TSAPayFeePercent). 0
	// offers no route; a value outside 1..MaxConvertedFeeBps is loaded as 0.
	RampConvertedFeeMaxBps        int
	RampConvertedFeeMaxBpsByChain map[string]int
	// RampConversionStallWindow/RampConversionLegCeiling/
	// RampConversionMaxShortfallUSD retune ConversionOrchestrator's own code
	// defaults (defaultStallWindow, defaultLegCeiling, defaultMaxShortfallUSD)
	// via env instead of a code change.
	RampConversionStallWindow     time.Duration
	RampConversionLegCeiling      time.Duration
	RampConversionMaxShortfallUSD float64
	// RampTreasuryKeptUSDCAlert is the USD threshold above which the treasury
	// float monitor alerts on a lane's kept USDC (SponsorFloat.TokenUSD): a
	// converted buy leaves the round trip's unspent budget sitting there, and
	// nothing sweeps it automatically. Zero disables the check.
	RampTreasuryKeptUSDCAlert float64

	// Company wallet: the company sells its own stablecoin stock as a ramp
	// provider (docs/specs/2026-10-03-ramp-flutterwave-provider-design.md
	// §4.1). Its key MUST differ from every other hot key (asserted at boot)
	// and from the conversion treasury's: that one holds user funds in transit.
	RampTreasuryBuy              bool
	RampTreasuryWalletPrivateKey string
	RampTreasuryWalletChains     []string
	// RampTreasuryMaxUSD is the per-order maximum (quote, open, live pricing).
	RampTreasuryMaxUSD string

	// RampFlutterwaveHashWait is how long a hashless SUCCESSFUL Flutterwave
	// delivery waits for an on-chain hash before it parks (N5).
	RampFlutterwaveHashWait time.Duration

	// RampConversionSellEnabled is the sell half of the same round trip: she
	// swaps her own token into USDC on Base (in her own wallet, we never hold
	// it) and we offramp that. Independent of RAMP_CONVERSION_BUY — the sell
	// needs no treasury key, only a LiFi integrator fee wallet — so a deploy
	// that leaves it unset keeps behaving exactly as before.
	RampConversionSellEnabled bool
	// RampSellSponsoredGasUSD is the EIP-3009 relay on Base we pay to move
	// her USDC into the offramp, in USD. It is part of g, the gas we pay,
	// passed on at cost (design §3.2).
	RampSellSponsoredGasUSD float64

	// ── TSA Pay (merchant payments) ──
	// Test mode is on until the licensing question is answered; live mode stays
	// off until PM gives clearance on compliance.
	TSAPayEnabled          bool
	TSAPayLiveEnabled      bool
	TSAPayWebhookMasterKey string
	TSAPayCheckoutBaseURL  string
	// TSAPayFeePercent is F, TSA Pay's platform fee: what a payment is split
	// by and the Paycrest sender fee on its direct payout (KeepSurplus may
	// raise it). TSAPayRoundTripBudgetBps is B, the slice of F a converted
	// payment's swap may spend — its payout is sent F − B and the unspent rest
	// is our LiFi fee. Both are TSA Pay's own, carried over unchanged when the
	// ramp moved to the company take (owner ruling 2026-10-07).
	TSAPayFeePercent         string
	TSAPayRoundTripBudgetBps int

	// ITOperatorPrivateKey is the IT (Instant Transfer) operator EOA key (hex, no
	// 0x) that signs creditPremium/debitPremium/settleDebit/reverseDebit. Dev
	// only; prod MUST use KMS. UNSET ⇒ IT operator path + /api/it routes dormant.
	ITOperatorPrivateKey string

	// ITPMID names the company Payment Merchant (PaymentMerchant UUID) whose Nomba
	// inbound account IT buyers pay into. IT ALWAYS uses this exact PM — there is
	// no fallback. UNSET (or not an active PM with an inbound account) ⇒ IT buy
	// reports payments unavailable rather than misrouting to another merchant.
	ITPMID string

	// ITMinSellNgnKobo is the smallest NGN payout an IT sell may produce, in kobo
	// (default ₦100 = 10000, Flutterwave's TRANSFER_AMOUNT_LIMIT floor). Raise to
	// tighten the dust-sell guard without a code change.
	ITMinSellNgnKobo int64
	// ITSellFWAbsenceTrusted lets IT sell recovery act on "Flutterwave has no
	// transfer for this reference": the reconciler re-sends a previously
	// attempted payout (and reverses a sell whose retry was refused), and an
	// admin may reverse or retry a sell on that evidence. Off by default until
	// the /transfers listing shape is confirmed against the live API — a wrong
	// "absent" pays the user twice. IT_SELL_FW_ABSENCE_TRUSTED.
	ITSellFWAbsenceTrusted bool

	// ClubKonnect (Nellobyte) — prepaid utility-bill vendor. Bills are dormant
	// unless BOTH ClubkonnectUserID and ClubkonnectAPIKey are set AND the MCGP
	// operator signer (ITOperatorPrivateKey) is wired: users pay bills with
	// spendable MCGP via the operator debit path, so no operator ⇒ no bills.
	// The API key rides in the query string on every call — it must never be
	// logged (the client redacts it from errors).
	ClubkonnectUserID  string
	ClubkonnectAPIKey  string
	ClubkonnectBaseURL string
	// ClubkonnectDataplanField picks which catalog field is sent as the vend's
	// DataPlan param: "code" (PRODUCT_CODE) or "id" (PRODUCT_ID). The gated API
	// docs don't say which; the default is validated by the live smoke test and
	// this switch avoids a redeploy if it proves wrong.
	ClubkonnectDataplanField string

	// BillsDataMarkupBps is retail markup on data bundles, in basis points over
	// ClubKonnect's cost price (other categories charge face value and earn the
	// provider discount). BillsQuoteTTL bounds how long a ₦→MCGP quote stays
	// payable. BillsFloatAlertNgn is the ClubKonnect wallet balance (whole ₦)
	// below which the float monitor starts alerting.
	BillsDataMarkupBps int
	BillsQuoteTTL      time.Duration
	BillsFloatAlertNgn int64

	// Name change by re-verification (verified users) and merchant
	// business-name change. NameChangeEnabled (NAME_CHANGE_ENABLED, default off)
	// is the switch that lets the API deploy dark. NameChangeFeeUSD is the
	// re-verification fee as a plain decimal ("1.00"; "0" charges nothing), read
	// when a change is quoted and frozen on it. A paid change allows
	// NameChangeMaxAttempts Didit sessions within NameChangeTTL, and a user may
	// complete NameChangeMaxPerYear changes in any 12 months.
	NameChangeEnabled     bool
	NameChangeFeeUSD      string
	NameChangeMaxPerYear  int
	NameChangeMaxAttempts int
	NameChangeTTL         time.Duration
	// NameChangeMaxSessionsPer30Days caps Didit sessions per user (Didit bills
	// each one) whatever the fee. NameChangeMaxRefundedCancels fees may be
	// returned per user in NameChangeRefundWindow. NameChangeRequireFaceMatch
	// holds a change unless Didit's face search matched the account's original
	// KYC session (needs face search on in the Didit workflow). A stablecoin fee
	// counts once NameChangeMinConfirmations deep (per chain in
	// NameChangeMinConfirmationsByChain).
	NameChangeMaxSessionsPer30Days    int
	NameChangeMaxRefundedCancels      int
	NameChangeRefundWindow            time.Duration
	NameChangeRequireFaceMatch        bool
	NameChangeMinConfirmations        int
	NameChangeMinConfirmationsByChain map[string]int
	// NameChangeMinRefundUSD (NAME_CHANGE_MIN_REFUND_USD, default 0.50) is the
	// smallest stablecoin amount sent back on its own: a payment that bounced
	// (short, or late) or the overpaid part of a kept fee. Anything less is
	// kept and logged for support — a refund must not cost more than it returns.
	NameChangeMinRefundUSD string

	// TSAPayReservedBusinessNames are words no owner's business name may
	// contain (TSA_PAY_RESERVED_BUSINESS_NAMES, comma-separated), added to
	// the built-in list — never replacing it.
	TSAPayReservedBusinessNames []string

	// RewardDistributorAddress is the deployed MCGPRewardDistributor proxy/
	// contract (fee/TP overhaul SP6) the admin publishes cumulative merkle roots
	// to and users claim MCGP from. UNSET = SP6 root-publishing dormant (the
	// admin publish endpoint errors clearly, user claim-data returns empty). On
	// Sonic, same chain as MCGP.
	RewardDistributorAddress string
	// P2PEscrowEIP712Version is the voucher domain `version` matching the
	// deployed P2PEscrow (see chainPreset.eip712Version). Stamped into the
	// typed-data the app signs.
	P2PEscrowEIP712Version string

	// P2PChains is the per-chain P2P deployment registry keyed by chainID,
	// assembled in Load(). Sonic is always present; BSC is present only when
	// its escrow address is configured (dormant-by-default). Later refactors
	// resolve escrow/domain/version by trade.ChainID from this map.
	P2PChains map[int64]P2PChainConfig

	// FeeManagerChains is the per-chain FeeManager registry keyed by chainID
	// (configurable-FeeManager design §3/§5/§10), assembled in Load() from
	// FEE_MANAGER_ADDRESS_SONIC / FEE_MANAGER_ADDRESS_BSC. A chain appears here
	// ONLY when its FeeManager address is configured — UNSET keeps the admin
	// set-fee flow + drift reconciler DORMANT for that chain (dormant-by-default,
	// like the BSC P2P escrow).
	FeeManagerChains map[int64]FeeManagerChainConfig

	// p2pNetworks is each preset chain's selected network (<CHAIN>_NETWORK),
	// chosen once by assembleP2PChains so every later reader agrees and an
	// unknown value alerts once (review M4).
	p2pNetworks map[string]string
	// LiquidityPoolRow is the shared pool row with a vault, nil when dormant
	// (liquidity_pool.go). Read it through LiquidityPool().
	LiquidityPoolRow *LiquidityPoolConfig

	// SonicNetwork records which preset was active for this boot
	// ("mainnet" or "testnet"). Downstream code that needs to query the
	// right Networks[…] map for sonic-side lookups reads this rather than
	// re-parsing the SONIC_NETWORK env var.
	SonicNetwork string

	// Mailjet email service
	MailjetAPIKey    string
	MailjetSecretKey string

	// Expo push notifications. Optional — sends work unauthenticated; set to
	// enable Expo's enhanced push security. Dormant if unset.
	ExpoAccessToken string

	// Didit KYC verification
	DiditAPIKey        string
	DiditWorkflowID    string
	DiditBaseURL       string
	DiditWebhookSecret string
	DiditCallbackURL   string

	// LiFi swap aggregator
	LiFiAPIKey string
	// CoinGecko key (COINGECKO_API_KEY). COINGECKO_API_PLAN=pro selects the paid
	// host; anything else treats the key as a free Demo key. Unset = public API.
	CoinGeckoAPIKey   string
	CoinGeckoPro      bool
	LiFiIntegratorID  string
	LiFiIntegratorFee string
	// LiFiBaseURL is always resolved (never raw/empty) via ResolveLiFiBaseURL —
	// see that function's doc for why this exists and what it defaults to.
	LiFiBaseURL string

	// AllSwap server-verified settle (design §8, I8). The backend re-queries LiFi
	// to recompute the integrator fee, then rejects a client-supplied fee that
	// deviates by more than AllSwapFeeDeviationMax (fraction, default 0.10 = 10%).
	// When LiFi is unreachable it does NOT trust the client or charge 0: it
	// prices the from-amount itself and books the SAME rate, because the accrual
	// mirrors a fixed cut LiFi already took.
	AllSwapFeeDeviationMax float64
	// AllSwapNominalFeeBps is the integrator fee the backend recomputes when LiFi
	// is reachable but the response carries no usable fee figure (default 300 =
	// 3%). It is the verified-fee basis, never the client value.
	AllSwapNominalFeeBps int

	// Open Exchange Rates configuration
	OpenExchangeRatesAppID string
	SupportedCurrencies    []string

	// Currencies routed through Bybit P2P (others use OER)
	BybitP2PCurrencies []string

	// Per-currency retail probe amount sent as the `amount` filter on Bybit
	// queries. Picking a retail-tier amount (~$3–4 equivalent) gets us a
	// tight, consensus-driven price; the unfiltered API returns by merchant
	// rank, surfacing large-ticket whales whose prices drift from the
	// retail consensus.
	BybitProbeAmounts map[string]string

	// P2P float-rate offset cap in basis points (e.g. 1000 = ±10%). Sellers
	// publishing float-rate ads pick an offset within ±this value relative
	// to the reference feed. Tune via env to widen/tighten platform-wide.
	P2PFloatOffsetMaxBps int
	// P2PFixedRateBandBps: fixed-rate ads must price within this many bps of the market reference.
	P2PFixedRateBandBps int

	// A trade paid but not released for this long pages the operators once.
	P2PPaidUnreleasedAlertAfter time.Duration

	// P2PGasTopUpsPerDay caps the S top-ups one user is sent for P2P txs per
	// rolling 24h (POST /api/p2p/gas/top-up). Skips do not count.
	P2PGasTopUpsPerDay int

	// Nomba — used for Nigerian bank directory + account name enquiry today,
	// and the auto-payout path in Phase 2. Falls back to "feature disabled"
	// when credentials aren't set so dev environments work without them.
	NombaBaseURL      string
	NombaClientID     string
	NombaClientSecret string
	NombaAccountID    string
	// NombaSenderName is the senderName stamped on outgoing (sell-flow) transfers.
	NombaSenderName string

	// P2P merchant-reputation thresholds for the "Trusted" badge.
	P2PTrustedMinReleased   int64
	P2PTrustedMinCompletion float64
	P2PTrustedMinAgeDays    int

	// PM automation (Nomba-backed buy/sell auto-settle). All optional — absence
	// keeps the whole PM stack DORMANT (no signer ⇒ no automation ⇒ webhook +
	// admin PM routes surface "not configured"), matching the BSC-P2P / FeeManager
	// dormant-by-default pattern.
	//
	// PMAdminEOAAddress is the platform admin EOA that signs adminRelease txs
	// (informational; the signer derives its own address from the key).
	PMAdminEOAAddress string
	// PMReleaseSigner selects the signer backend: "local" (dev, in-process key)
	// or "kms" (prod, not yet implemented — release stays disabled).
	PMReleaseSigner string
	// PMReleasePrivateKey is the hex private key for the local signer (DEV ONLY).
	PMReleasePrivateKey string
	// NombaWebhookSecret is the shared HMAC secret used to verify inbound Nomba
	// webhooks. UNSET ⇒ webhook route dormant.
	NombaWebhookSecret string

	// Flutterwave — v4 fiat gateway replacing Nomba (Nomba declined production
	// API access, 2026-07). Dormant-by-default: missing creds ⇒ nil client,
	// routes surface "not configured". The production base URL is mid-migration
	// on Flutterwave's side, so it is ALWAYS config-driven.
	FlutterwaveBaseURL      string
	FlutterwaveIdpURL       string
	FlutterwaveClientID     string
	FlutterwaveClientSecret string
	// FlutterwaveWebhookSecret verifies the flutterwave-signature header
	// (Base64 HMAC-SHA256 of the raw body). UNSET ⇒ FW webhook route dormant.
	FlutterwaveWebhookSecret string
	// FlutterwaveMerchantID is OUR merchant id — the wallet identifier used as
	// the recipient of balance-to-balance currency conversions.
	FlutterwaveMerchantID string
	// FlutterwaveCustomerID is the company customer that IT dynamic virtual
	// accounts hang off (per-payer customers with BVN/NIN are a follow-up).
	// UNSET ⇒ IT buys fall back to the company PM's static VA.
	FlutterwaveCustomerID string
	// Company customer registered lazily when FlutterwaveCustomerID is unset —
	// the parent for all virtual accounts. Overridable; sane defaults.
	FlutterwaveCompanyName  string
	FlutterwaveCompanyEmail string
	FlutterwaveCompanyPhone string

	// InboundMode selects how inbound naira is attributed (central-account
	// design §9). "va" is today's behaviour: a per-user Flutterwave virtual
	// account, attribution by charge.completed webhook. "central" routes every
	// payer to the one company account, where attribution comes from polling
	// the wallet statement instead.
	//
	// It is a single global flag by decision 10 — a per-user rollout would put
	// two attribution paths over the ONE company account, where a sender name
	// is all there is to tell a rolled-out payer from a not-yet one, and a
	// credit both or neither path claims is the result.
	//
	// It does not retire the virtual accounts that already exist: nothing here
	// or anywhere else deactivates them at Flutterwave, and the charge-webhook
	// credit path is not gated on the mode, so a payer who kept an old account
	// number is still credited through it. Central mode only refuses to mint
	// NEW accounts (ITService.ActivateFiatVA) and stops the app showing the old
	// ones. The two paths cannot claim each other's money — they settle under
	// different on-chain ref domains (IT:va: vs IT:inbound:) — but they are both
	// live, and closing the accounts at the provider is a deliberate follow-up
	// with its own review, not something this flag does.
	//
	// Flipping back is this variable plus a restart.
	InboundMode string
	// InboundPollInterval is the wallet-statement poll period (default 30s,
	// decision 8). This is not a reconciler cadence: wallet funding fires no
	// webhook at all, so the poll is the ONLY way a credit is ever noticed and
	// the interval is the user's wait.
	InboundPollInterval time.Duration
	// InboundStatementLookback is how far back each poll reads. Overlap is free
	// (ingestion is idempotent on the NIP session id) whereas a window shorter
	// than an outage silently loses every credit that arrived during it — so
	// this is deliberately far wider than the poll interval.
	InboundStatementLookback time.Duration
	// InboundCheckCooldown is the shortest gap between a completed statement
	// poll and a user-triggered one ("I've sent the transfer"). It is global,
	// not per user, and is the knob that keeps taps inside Flutterwave's
	// statement rate limit.
	InboundCheckCooldown time.Duration
	// InboundAlertEmails are the operator mailboxes told, one email per credit,
	// whenever the matcher declines to attribute money and holds it.
	//
	// This is the first admin alert address in config, and it exists because a
	// held credit has NO automatic exit (decision 9): nothing expires it, nothing
	// refunds it, and the customer who sent the money is already waiting. Until
	// a human looks, the queue is only discoverable by someone choosing to open
	// it. Unset is a legitimate state — the queue still fills, nobody is paged.
	InboundAlertEmails []string

	// InboundCentralAccount* are the company bank account every payer is sent
	// to in central mode. They are config rather than constants because the
	// account is an operational fact that can be replaced without a code
	// change, and because a wrong hardcoded number is money sent to a stranger.
	//
	// Unset in VA mode, which is why consumers must check IsConfigured before
	// rendering them — see services.CentralAccount.
	InboundCentralAccountNumber string
	InboundCentralBankName      string
	InboundCentralAccountName   string

	// StableSpreadBps is the company margin on brokered stablecoin trades, in
	// basis points. It REPLACES the swap/routing bps legs for these flows
	// rather than stacking on them, and it is the buffer that absorbs FX drift
	// inside the rate-lock window and provider-fee estimation error.
	StableSpreadBps int64
	// StableRateLockTTL bounds how long a quoted rate is honoured. The lock is
	// ours, not Flutterwave's — /transfers/rates carries no expiry.
	StableRateLockTTL time.Duration
	// StableOverDeliveryTolBps bounds how far a sell deposit may exceed the
	// amount it was quoted for and still be paid out automatically. Beyond it
	// the intent is parked for an operator rather than paid: the payout scales
	// with what actually arrived, and an unbounded ratio would let a small
	// quote claim an arbitrarily large deposit.
	StableOverDeliveryTolBps int64
	// StableMinSell is the provider floor for a stablecoin sell, in major
	// units (e.g. "10"). Flutterwave rejects a conversion below its floor
	// AFTER the user has irreversibly sent funds, so the quote refuses first.
	// Empty leaves services.DefaultStableMinSellMajor in place; the floor
	// cannot be removed by config, only raised or lowered.
	StableMinSell string
	// FWStableDepositAddressPolygon is Flutterwave's own Polygon address that
	// user sell deposits are routed to. Flutterwave exposes it on the dashboard
	// only, so it is fetched once by hand and configured here. The platform
	// never holds an address in this flow.
	FWStableDepositAddressPolygon string

	// FeeQuote — deposit/withdrawal fee-quote engine (PM fee spec, batch 11).
	// Quote-only: powers GET /api/it/quote so the app can show the itemized
	// fee breakdown before confirm. NOT wired into money movement yet (that
	// lands after PM sign-off on the two UNRESOLVED values below).
	FeeQuote FeeQuoteConfig

	// Fee/TP overhaul (SP3) tunables.
	//
	// TPPerUSD is the TP minting rate: how many TP one USD of TP-pool value
	// mints. DEFAULT 100 (1 TP = $0.01, per the PM spec — consistent with the
	// level ladder's rewardUSD = thresholdTP/100). Tunable via FEE_TP_PER_USD;
	// it sets level PACING (see design §3), not the reward ratio.
	TPPerUSD float64

	// GlobalPoolSettleInterval is the cadence of the SP4 global-pool settlement
	// epoch (SettleGlobalPoolEpoch folds the pool delta into the cumulative
	// index once per interval). DEFAULT 24h (daily). Tunable via
	// FEE_GLOBAL_POOL_SETTLE_INTERVAL (a Go duration string, e.g. "1h", "15m").
	GlobalPoolSettleInterval time.Duration

	// Fee/TP overhaul (SP7) — claim-eligibility velocity heuristic tunables.
	//
	// The heuristic auto-flags a referrer whose DIRECT downline grew by more
	// than RewardVelocityMaxSignups within RewardVelocityWindow. It is cheap +
	// bounded (a single indexed COUNT over users referred by one referrer in a
	// time window — never a full-graph scan). An auto-flag writes an advisory
	// RewardFlag{source:auto} that gates the claim until an admin clears it.
	//
	// RewardVelocityEnabled gates the whole heuristic so it can be disabled
	// without code change (REWARD_VELOCITY_ENABLED=false). The KYC + admin-flag
	// + cycle gates in IsClaimEligible are independent of this flag.
	RewardVelocityEnabled    bool
	RewardVelocityMaxSignups int
	RewardVelocityWindow     time.Duration

	// FeeModelV2Enabled is the master kill-switch for the v2 fee/TP engine
	// (SP3). DEFAULT OFF: until an explicit cutover, the engine is NOT wired
	// into any live fee-settled path (checkout, withdrawal, swap, service,
	// wallet, P2P), so those paths behave exactly as the v1 codebase did. When
	// true, the engine is wired and the per-feature split + TP accrual go live.
	// Set FEE_MODEL_V2_ENABLED=true only as part of the documented cutover.
	FeeModelV2Enabled bool

	// Network configurations: "mainnet" and "testnet"
	Networks map[string]NetworkConfig

	// NonEVMChains is the registry of non-EVM chain configs keyed by chain key
	// (e.g. "tron"). A chain appears here ONLY when its RPC env is set —
	// absent chains stay dormant (dormant-by-default pattern).
	NonEVMChains map[string]NonEVMChainConfig

	// Legacy flat fields — kept for backward compat, point to mainnet
	Chains         map[string]ChainConfig
	TokenAddresses map[string]string
}

// Working defaults for the P2P launch knobs. They live only here: the services
// and jobs that take these values fall back to these constants.
const (
	DefaultP2PFixedRateBandBps         = 2000 // ±20% of the market reference
	DefaultP2PPaidUnreleasedAlertAfter = 2 * time.Hour
	DefaultP2PGasTopUpsPerDay          = 6
)

func Load() *Config {
	// Pick the Sonic deployment preset first; individual env vars layered on
	// top can override any single field. Default is "mainnet" so a production
	// server with no SONIC_NETWORK set keeps its current behaviour.
	sonicNetwork := strings.ToLower(strings.TrimSpace(getEnv("SONIC_NETWORK", "mainnet")))
	preset, ok := sonicPresets[sonicNetwork]
	if !ok {
		log.Printf("[config] SONIC_NETWORK=%q is unknown; falling back to 'mainnet'", sonicNetwork)
		sonicNetwork = "mainnet"
		preset = sonicPresets["mainnet"]
	}
	log.Printf("[config] Sonic network = %s (chain %d, P2PEscrow %s)",
		sonicNetwork, preset.chainID, preset.p2pEscrow)

	cfg := &Config{
		SonicNetwork:        sonicNetwork,
		DatabaseURL:         getEnv("DATABASE_URL", "postgres://localhost:5432/tsa?sslmode=disable"),
		JWTSecret:           getEnv("JWT_SECRET", ""),
		FrontendURL:         getEnv("FRONTEND_URL", "http://localhost:3000"),
		AdminURL:            getEnv("ADMIN_URL", "http://localhost:5173,https://admin.tsaconnectworld.com"),
		LandingURL:          getEnv("LANDING_URL", "https://tsaconnectworld.com"),
		WebAppOrigin:        normalizeOrigin(getEnv("WEB_APP_ORIGIN", "")),
		NetlifyProxySecret:  getEnv("NETLIFY_PROXY_SECRET", ""),
		Port:                getEnv("PORT", "5000"),
		Env:                 normalizeEnv(getEnv("ENV", "development")),
		AllowRawSignerKeys:  getEnvBool("ALLOW_RAW_SIGNER_KEYS", false),
		CloudinaryCloudName: getEnv("CLOUDINARY_CLOUD_NAME", ""),
		CloudinaryAPIKey:    getEnv("CLOUDINARY_API_KEY", ""),
		CloudinaryAPISecret: getEnv("CLOUDINARY_API_SECRET", ""),

		// Sonic blockchain — preset by SONIC_NETWORK, overridable per-field.
		SonicRPCURL:         getEnv("SONIC_RPC_URL", preset.rpcURL),
		SonicChainID:        getEnvInt64("SONIC_CHAIN_ID", preset.chainID),
		MCGPTokenAddress:    getEnv("MCGP_TOKEN_ADDRESS", preset.mcgpAddress),
		USDTTokenAddress:    getEnv("USDT_TOKEN_ADDRESS", preset.usdtAddress),
		USDCTokenAddress:    getEnv("USDC_TOKEN_ADDRESS", preset.usdcAddress),
		SystemWalletAddress: getEnv("SYSTEM_WALLET_ADDRESS", "0xaF326D5D242C9A55590540f14658adDDd3586A8d"),

		// BSC blockchain
		BSCRPCURL:  getEnv("BSC_RPC_URL", "https://data-seed-prebsc-1-s1.binance.org:8545"),
		BSCChainID: getEnvInt64("BSC_CHAIN_ID", 97),

		// Smart contract addresses
		ProductEscrowAddress:   getEnv("PRODUCT_ESCROW_ADDRESS", "0x42C3c60F40c48497D023166C6088e27872D63fcb"),
		ServiceContractAddress: getEnv("SERVICE_CONTACT_ADDRESS", "0xAb3574501d41940cD860Ecbfc535BFa6D9e13Ff3"),
	}
	cfg.OTCMarketplaceAddress = getEnv("OTC_MARKETPLACE_ADDRESS", "")
	cfg.OTCMarketplaceDeployBlock = uint64(max(getEnvInt64("OTC_MARKETPLACE_DEPLOY_BLOCK",
		int64(knownOTCDeployBlock(cfg.OTCMarketplaceAddress))), 0))
	cfg.OTCLogMaxBlockRange = uint64(max(getEnvInt64("OTC_LOG_MAX_BLOCK_RANGE", 0), 0))
	cfg.OrderMcgpMaxOpenHolds = int(getEnvInt64("ORDER_MCGP_MAX_OPEN_HOLDS", 3))
	cfg.OrderMcgpMaxRefundsPerDay = int(getEnvInt64("ORDER_MCGP_MAX_REFUNDS_PER_DAY", 5))
	cfg.OrderMcgpWindowSharePct = int(getEnvInt64("ORDER_MCGP_WINDOW_SHARE_PCT", 30))
	// McgpEntitlementSwap (Swap Contract B) — UNSET keeps the /api/mcgp routes dormant.
	cfg.MCGPSwapBAddress = strings.TrimSpace(getEnv("MCGP_SWAP_B_ADDRESS", ""))
	// Gasless relaying — dormant unless both of the first two are set.
	cfg.MCGPForwarderAddress = strings.TrimSpace(getEnv("MCGP_FORWARDER_ADDRESS", ""))
	cfg.RelayerPrivateKey = strings.TrimSpace(getEnv("RELAYER_PRIVATE_KEY", ""))
	// Short by design: a signed request is executable by ANYONE until it expires.
	cfg.RelayerMaxDeadline = time.Duration(getEnvInt64("RELAYER_MAX_DEADLINE_SECONDS", 300)) * time.Second
	cfg.RelayerGasCeiling = uint64(getEnvInt64("RELAYER_GAS_CEILING", 500_000))
	cfg.RelayerPerUserPerHour = int(getEnvInt64("RELAYER_PER_USER_PER_HOUR", 20))
	cfg.RelayerDailyGasBudget = strings.TrimSpace(getEnv("RELAYER_DAILY_GAS_BUDGET", "200000000"))
	cfg.RelayerBalanceFloorWei = strings.TrimSpace(getEnv("RELAYER_BALANCE_FLOOR_WEI", "1000000000000000000")) // 1 S
	cfg.RelayerMinSponsored = strings.TrimSpace(getEnv("RELAYER_MIN_SPONSORED", "0"))

	cfg.RampGasSponsorshipEnabled = getEnvBool("RAMP_GAS_SPONSORSHIP", false)
	cfg.RampSponsorPrivateKey = strings.TrimSpace(getEnv("RAMP_SPONSOR_PRIVATE_KEY", ""))
	cfg.RampSponsorChains = nil
	for _, chain := range strings.Split(getEnv("RAMP_SPONSOR_CHAINS", "base,arbitrum,polygon,bsc"), ",") {
		if chain = strings.ToLower(strings.TrimSpace(chain)); chain != "" {
			cfg.RampSponsorChains = append(cfg.RampSponsorChains, chain)
		}
	}
	cfg.RampSponsorDailyGasBudget = strings.TrimSpace(getEnv("RAMP_SPONSOR_DAILY_GAS_BUDGET", "500000000"))
	cfg.RampSponsorTopUpUnusedDailyCap = int(getEnvInt64("RAMP_SPONSOR_TOPUP_UNUSED_DAILY_CAP", 2))
	rampFloorDefaults := map[string]string{
		"base": "2000000000000000", "arbitrum": "2000000000000000", // 0.002 ETH
		"polygon": "5000000000000000000", // 5 POL
		"bsc":     "10000000000000000",   // 0.01 BNB
	}
	rampTopUpMaxDefaults := map[string]string{
		"base": "50000000000000", "arbitrum": "50000000000000", // 0.00005 ETH
		"polygon": "500000000000000000", // 0.5 POL
		"bsc":     "500000000000000",    // 0.0005 BNB
	}
	cfg.RampSponsorBalanceFloorWei = map[string]string{}
	cfg.RampSponsorTopUpMaxWei = map[string]string{}
	for _, chain := range cfg.RampSponsorChains {
		up := strings.ToUpper(chain)
		cfg.RampSponsorBalanceFloorWei[chain] = strings.TrimSpace(getEnv("RAMP_SPONSOR_BALANCE_FLOOR_WEI_"+up, rampFloorDefaults[chain]))
		cfg.RampSponsorTopUpMaxWei[chain] = strings.TrimSpace(getEnv("RAMP_SPONSOR_TOPUP_MAX_WEI_"+up, rampTopUpMaxDefaults[chain]))
	}

	cfg.MarketplaceFundingEnabled = getEnvBool("MARKETPLACE_FUNDING", false)
	cfg.MarketplaceSonicGasEnabled = getEnvBool("MARKETPLACE_SONIC_GAS", false)
	// ~400k gas per order (approve + createOrder) x2 buffer at ~55 gwei is
	// ~0.045 S an order: sponsoring stops below the floor (the float monitor
	// alerts at twice it), and the ceiling covers a ~10-order cart.
	cfg.SonicGasFloorWei = getEnvRenamed("SONIC_GAS_FLOOR_WEI", "MARKETPLACE_SONIC_GAS_FLOOR_WEI", "5000000000000000000")           // 5 S
	cfg.SonicGasTopUpMaxWei = getEnvRenamed("SONIC_GAS_TOPUP_MAX_WEI", "MARKETPLACE_SONIC_GAS_TOPUP_MAX_WEI", "500000000000000000") // 0.5 S
	cfg.MarketplaceEscrowGasPerOrder = uint64(max(getEnvInt64("MARKETPLACE_ESCROW_GAS_PER_ORDER", 400_000), 0))

	cfg.RampConversionBuyEnabled = getEnvBool("RAMP_CONVERSION_BUY", false)
	cfg.RampPriceToleranceBps = int(getEnvInt64("RAMP_PRICE_TOLERANCE_BPS", 50))
	cfg.RampPriceGuard = strings.ToLower(strings.TrimSpace(getEnv("RAMP_PRICE_GUARD", "log")))
	if g := cfg.RampPriceGuard; g != "" && g != "log" && g != "enforce" {
		log.Printf("[config] RAMP_PRICE_GUARD=%q is unknown; falling back to 'log'", g)
	}
	cfg.RampProviderTimeout = getEnvDuration("RAMP_PROVIDER_TIMEOUT", 8*time.Second)
	cfg.RampOpenAttemptTimeout = getEnvDuration("RAMP_OPEN_ATTEMPT_TIMEOUT", 15*time.Second)
	cfg.RampOpenBudget = getEnvDuration("RAMP_OPEN_BUDGET", 20*time.Second)
	cfg.RampFlutterwaveBuy = getEnvBool("RAMP_FLUTTERWAVE_BUY", false)
	cfg.RampFlutterwaveMinTokens = strings.TrimSpace(getEnv("RAMP_FLUTTERWAVE_MIN_TOKENS", "10"))
	cfg.RampFlutterwaveMaxUSD = strings.TrimSpace(getEnv("RAMP_FLUTTERWAVE_MAX_USD", "2000"))
	cfg.RampFlutterwaveSendFeeUSD = strings.TrimSpace(getEnv("RAMP_FLUTTERWAVE_SEND_FEE_USD", "1.50"))
	cfg.RampFlutterwaveConversionFeeBps = getEnvInt64("RAMP_FLUTTERWAVE_CONVERSION_FEE_BPS", 150)
	cfg.RampFlutterwaveSendFeeMode = strings.ToLower(strings.TrimSpace(getEnv("RAMP_FLUTTERWAVE_SEND_FEE_MODE", "wallet")))
	cfg.RampDailyAbandonCap = int(getEnvInt64("RAMP_DAILY_ABANDON_CAP", 5))
	cfg.RampHonourGrace = time.Duration(getEnvInt64("RAMP_HONOUR_GRACE_SECONDS", 600)) * time.Second
	cfg.RampCompanyTakeBps = companyTakeFromEnv()
	cfg.RampFundingFeeBps = getEnvInt64("RAMP_FUNDING_FEE_BPS", 140)
	cfg.RampVAFundingFeeBps = getEnvInt64("RAMP_VA_FUNDING_FEE_BPS", 215)
	warnRemovedPricingKeys()
	cfg.RampTreasuryMinNGN = strings.TrimSpace(getEnv("RAMP_TREASURY_MIN_NGN", "100"))
	cfg.RampTreasuryDailyUserMaxUSD = strings.TrimSpace(getEnv("RAMP_TREASURY_DAILY_USER_MAX_USD", "500"))
	cfg.RampTreasuryLowBalanceUSD = getEnvFloat64("RAMP_TREASURY_LOW_BALANCE_USD", 50)
	if v := cfg.RampTreasuryLowBalanceUSD; !(v > 0) { // also catches NaN
		log.Printf("[config] ALERT ramp_treasury_low_balance_invalid_total=1 RAMP_TREASURY_LOW_BALANCE_USD=%v must be positive — using 50", v)
		cfg.RampTreasuryLowBalanceUSD = 50
	}
	cfg.RampTreasurySendStall = time.Duration(getEnvInt64("RAMP_TREASURY_SEND_STALL_MINUTES", 30)) * time.Minute
	cfg.RampTreasuryRateTolBps = getEnvInt64("RAMP_TREASURY_RATE_TOL_BPS", 400)
	if cfg.RampTreasuryRateTolBps <= 0 || cfg.RampTreasuryRateTolBps > 1000 {
		// A wide tolerance defeats the cross-check that guards the company wallet's price.
		log.Printf("[config] ALERT ramp_treasury_rate_tol_invalid_total=1 RAMP_TREASURY_RATE_TOL_BPS=%d must be 1..1000 — using 400", cfg.RampTreasuryRateTolBps)
		cfg.RampTreasuryRateTolBps = 400
	}
	cfg.RampTreasuryConfirmations = int(getEnvInt64("RAMP_TREASURY_CONFIRMATIONS", 5))
	cfg.RampTreasuryPrivateKey = strings.TrimSpace(getEnv("RAMP_TREASURY_PRIVATE_KEY", ""))
	cfg.RampTreasuryChains = nil
	for _, chain := range strings.Split(getEnv("RAMP_TREASURY_CHAINS", "base,arbitrum,polygon,bsc,optimism,avalanche,linea,sonic,ethereum"), ",") {
		if chain = strings.ToLower(strings.TrimSpace(chain)); chain != "" {
			cfg.RampTreasuryChains = append(cfg.RampTreasuryChains, chain)
		}
	}
	cfg.RampTreasuryMaxInflightUSDPerOrder = getEnvFloat64("RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER", 2000)
	cfg.RampTreasuryMaxInflightUSDPerChain = getEnvFloat64("RAMP_TREASURY_MAX_INFLIGHT_USD_PER_CHAIN", 10000)
	// Order-of-magnitude placeholders (~0.005-0.01 of the chain's native gas
	// token, ~$5-10 at typical prices) covering a bridge transaction, which
	// costs more gas than the simple relay RampSponsorBalanceFloorWei bounds.
	// Retune per chain once real bridge gas costs are observed in production.
	rampTreasuryFloorDefaults := map[string]string{
		"base": "5000000000000000", "arbitrum": "5000000000000000", // 0.005 ETH
		"optimism": "5000000000000000", "linea": "5000000000000000", // 0.005 ETH
		"polygon":   "5000000000000000000", // 5 POL
		"bsc":       "10000000000000000",   // 0.01 BNB
		"avalanche": "100000000000000000",  // 0.1 AVAX
		"sonic":     "5000000000000000000", // 5 S
		"ethereum":  "10000000000000000",   // 0.01 ETH — mainnet gas is pricier
	}
	cfg.RampTreasuryNativeFloorWei = map[string]string{}
	for _, chain := range cfg.RampTreasuryChains {
		up := strings.ToUpper(chain)
		cfg.RampTreasuryNativeFloorWei[chain] = strings.TrimSpace(getEnv("RAMP_TREASURY_NATIVE_FLOOR_WEI_"+up, rampTreasuryFloorDefaults[chain]))
	}
	cfg.RampTreasuryBuy = getEnvBool("RAMP_TREASURY_BUY", false)
	if m := getEnvInt64("RAMP_FLUTTERWAVE_HASH_WAIT_MINUTES", 10); m > 0 {
		cfg.RampFlutterwaveHashWait = time.Duration(m) * time.Minute
	} else {
		log.Printf("[config] ALERT ramp_fw_hash_wait_invalid value=%d — using 10 minutes", m)
		cfg.RampFlutterwaveHashWait = 10 * time.Minute
	}
	cfg.RampTreasuryWalletPrivateKey = strings.TrimSpace(getEnv("RAMP_TREASURY_WALLET_PRIVATE_KEY", ""))
	cfg.RampTreasuryWalletChains = nil
	for _, chain := range strings.Split(getEnv("RAMP_TREASURY_WALLET_CHAINS", "base"), ",") {
		if chain = strings.ToLower(strings.TrimSpace(chain)); chain != "" {
			cfg.RampTreasuryWalletChains = append(cfg.RampTreasuryWalletChains, chain)
		}
	}
	// The wallet's gas-floor alert reuses RAMP_TREASURY_NATIVE_FLOOR_WEI_<CHAIN>
	// (spec §9), so the map covers its chains too.
	for _, chain := range cfg.RampTreasuryWalletChains {
		if _, ok := cfg.RampTreasuryNativeFloorWei[chain]; !ok {
			cfg.RampTreasuryNativeFloorWei[chain] = strings.TrimSpace(
				getEnv("RAMP_TREASURY_NATIVE_FLOOR_WEI_"+strings.ToUpper(chain), rampTreasuryFloorDefaults[chain]))
		}
	}
	cfg.RampTreasuryMaxUSD = strings.TrimSpace(getEnv("RAMP_TREASURY_MAX_USD", "200"))
	// LiFi's own diamond per chain, read from li.quest/v1/chains (last
	// checked 2026-09-22) and PINNED here deliberately: resolving the
	// allowlist at runtime from the same API whose responses it exists to
	// validate would defeat the check entirely. Linea's diamond differs from the shared one.
	// Override per chain with RAMP_TREASURY_ROUTERS_<CHAIN> (comma-separated)
	// when LiFi migrates a diamond.
	const lifiDiamond = "0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE"
	rampTreasuryRouterDefaults := map[string]string{
		"ethereum": lifiDiamond, "arbitrum": lifiDiamond, "base": lifiDiamond,
		"bsc": lifiDiamond, "optimism": lifiDiamond, "polygon": lifiDiamond,
		"avalanche": lifiDiamond, "sonic": lifiDiamond,
		"linea": "0xde1e598b81620773454588b85d6b5d4eec32573e",
	}
	// Filled for every treasury chain AND every chain with a pinned default:
	// swapped sells check her route against the same list, and a sell's
	// source chain does not stop being checkable because RAMP_TREASURY_CHAINS
	// was narrowed for buys. Treasury lanes still only read their own chains.
	routerChains := append([]string{}, cfg.RampTreasuryChains...)
	for chain := range rampTreasuryRouterDefaults {
		routerChains = append(routerChains, chain)
	}
	cfg.RampTreasuryRouters = map[string][]string{}
	for _, chain := range routerChains {
		up := strings.ToUpper(chain)
		raw := strings.TrimSpace(getEnv("RAMP_TREASURY_ROUTERS_"+up, rampTreasuryRouterDefaults[chain]))
		var routers []string
		for _, addr := range strings.Split(raw, ",") {
			if addr = strings.TrimSpace(addr); addr != "" {
				routers = append(routers, addr)
			}
		}
		cfg.RampTreasuryRouters[chain] = routers
	}
	cfg.RampConversionRetryWindow = time.Duration(getEnvInt64("RAMP_CONVERSION_RETRY_MINUTES", 30)) * time.Minute
	cfg.RampConversionMinUSD = getEnvFloat64("RAMP_CONVERSION_MIN_USD", 1)
	// Decision 6: Ethereum and Linea cost enough in gas that a $1 minimum
	// order would not be worth converting; every other chain uses the
	// RampConversionMinUSD default unless explicitly overridden below.
	rampConversionMinDefaults := map[string]float64{
		"ethereum": 5,
		"linea":    25,
	}
	cfg.RampConversionMinUSDByChain = map[string]float64{}
	for _, chain := range cfg.RampTreasuryChains {
		up := strings.ToUpper(chain)
		def := cfg.RampConversionMinUSD
		if d, ok := rampConversionMinDefaults[chain]; ok {
			def = d
		}
		cfg.RampConversionMinUSDByChain[chain] = getEnvFloat64("RAMP_CONVERSION_MIN_USD_"+up, def)
	}
	cfg.RampConvertedFeeMaxBps = convertedFeeCapFromEnv("RAMP_CONVERTED_FEE_MAX_BPS", 200)
	cfg.RampConvertedFeeMaxBpsByChain = map[string]int{}
	for _, chain := range append(append([]string{}, routerChains...), "solana") {
		cfg.RampConvertedFeeMaxBpsByChain[chain] = convertedFeeCapFromEnv("RAMP_CONVERTED_FEE_MAX_BPS_"+strings.ToUpper(chain), cfg.RampConvertedFeeMaxBps)
	}
	// ConversionOrchestrator's own code defaults are 6h/24h/$50 — see
	// defaultStallWindow, defaultLegCeiling, defaultMaxShortfallUSD in
	// internal/services/conversion_orchestrator.go. Reading them from env
	// here means retuning never needs a code change.
	cfg.RampConversionStallWindow = time.Duration(getEnvInt64("RAMP_CONVERSION_STALL_MINUTES", 360)) * time.Minute
	cfg.RampConversionLegCeiling = time.Duration(getEnvInt64("RAMP_CONVERSION_LEG_CEILING_HOURS", 24)) * time.Hour
	cfg.RampConversionMaxShortfallUSD = getEnvFloat64("RAMP_CONVERSION_MAX_SHORTFALL_USD", 50)
	cfg.RampTreasuryKeptUSDCAlert = getEnvFloat64("RAMP_TREASURY_KEPT_USDC_ALERT", 500)
	cfg.RampConversionSellEnabled = getEnvBool("RAMP_CONVERSION_SELL", false)
	cfg.RampSellSponsoredGasUSD = getEnvFloat64("RAMP_SELL_SPONSORED_GAS_USD", 0.02)

	// TSA Pay merchant-payments platform.
	cfg.TSAPayEnabled = getEnvBool("TSA_PAY_ENABLED", false)
	cfg.TSAPayLiveEnabled = getEnvBool("TSA_PAY_LIVE_ENABLED", false)
	cfg.TSAPayWebhookMasterKey = strings.TrimSpace(getEnv("TSA_PAY_WEBHOOK_MASTER_KEY", ""))
	checkoutBaseURL := strings.TrimSpace(getEnv("TSA_PAY_CHECKOUT_BASE_URL", ""))
	if checkoutBaseURL == "" {
		checkoutBaseURL = "https://tsaconnectworld.com/pay"
	}
	cfg.TSAPayCheckoutBaseURL = checkoutBaseURL
	// Defaults are production's values on 2026-10-07, so TSA Pay prices
	// exactly as it did before the ramp moved to the company take.
	cfg.TSAPayFeePercent = strings.TrimSpace(getEnv("TSA_PAY_FEE_PERCENT", ""))
	if cfg.TSAPayFeePercent == "" {
		cfg.TSAPayFeePercent = "1"
	}
	cfg.TSAPayRoundTripBudgetBps = int(getEnvInt64("TSA_PAY_ROUND_TRIP_BUDGET_BPS", 50))

	// IT (Instant Transfer) operator EOA key (an on-chain `operators[]` member).
	// Dev/staging only — prod MUST use KMS. UNSET keeps /api/it routes dormant.
	// TrimPrefix "0x": crypto.HexToECDSA rejects a 0x-prefixed string outright,
	// so an operator who pastes a key with the prefix (as ramp/PM keys already
	// tolerate) would otherwise leave IT — and utility bills, which need the
	// same operator signer — silently dormant.
	cfg.ITOperatorPrivateKey = strings.TrimPrefix(strings.TrimSpace(getEnv("IT_OPERATOR_PRIVATE_KEY", "")), "0x")
	cfg.ITPMID = strings.TrimSpace(getEnv("IT_PM_ID", ""))
	cfg.ITMinSellNgnKobo = getEnvInt64("IT_MIN_SELL_NGN_KOBO", 10000) // ₦100
	cfg.ITSellFWAbsenceTrusted = getEnvBool("IT_SELL_FW_ABSENCE_TRUSTED", false)

	cfg.ClubkonnectUserID = strings.TrimSpace(getEnv("CLUBKONNECT_USER_ID", ""))
	cfg.ClubkonnectAPIKey = strings.TrimSpace(getEnv("CLUBKONNECT_API_KEY", ""))
	cfg.ClubkonnectBaseURL = strings.TrimSpace(getEnv("CLUBKONNECT_BASE_URL", "https://www.nellobytesystems.com"))
	cfg.ClubkonnectDataplanField = strings.TrimSpace(getEnv("CLUBKONNECT_DATAPLAN_FIELD", "code"))
	cfg.BillsDataMarkupBps = int(getEnvInt64("BILLS_DATA_MARKUP_BPS", 300))
	cfg.BillsQuoteTTL = time.Duration(getEnvInt64("BILLS_QUOTE_TTL_SECONDS", 300)) * time.Second
	cfg.BillsFloatAlertNgn = getEnvInt64("BILLS_FLOAT_ALERT_NGN", 100000)
	cfg.NameChangeEnabled = getEnvBool("NAME_CHANGE_ENABLED", false)
	cfg.NameChangeFeeUSD = strings.TrimSpace(getEnv("NAME_CHANGE_FEE_USD", "1.00"))
	cfg.NameChangeMaxPerYear = int(getEnvInt64("NAME_CHANGE_MAX_PER_YEAR", 2))
	cfg.NameChangeMaxAttempts = int(getEnvInt64("NAME_CHANGE_MAX_ATTEMPTS", 3))
	cfg.NameChangeTTL = time.Duration(getEnvInt64("NAME_CHANGE_TTL_DAYS", 7)) * 24 * time.Hour
	cfg.NameChangeMaxSessionsPer30Days = int(getEnvInt64("NAME_CHANGE_MAX_SESSIONS_PER_30_DAYS", 6))
	cfg.NameChangeMaxRefundedCancels = int(getEnvInt64("NAME_CHANGE_MAX_REFUNDED_CANCELS", 1))
	cfg.NameChangeRefundWindow = time.Duration(getEnvInt64("NAME_CHANGE_REFUND_WINDOW_DAYS", 30)) * 24 * time.Hour
	cfg.NameChangeRequireFaceMatch = getEnvBool("NAME_CHANGE_REQUIRE_FACE_MATCH", true)
	cfg.NameChangeMinConfirmations = int(getEnvInt64("NAME_CHANGE_MIN_CONFIRMATIONS", 3))
	cfg.NameChangeMinRefundUSD = strings.TrimSpace(getEnv("NAME_CHANGE_MIN_REFUND_USD", "0.50"))
	// Chains whose blocks reorganise deeper wait longer by default.
	nameChangeConfirmationDefaults := map[string]int{"polygon": 10, "bsc": 5}
	cfg.NameChangeMinConfirmationsByChain = map[string]int{}
	for _, chain := range cfg.RampTreasuryChains {
		def := cfg.NameChangeMinConfirmations
		if d, ok := nameChangeConfirmationDefaults[chain]; ok {
			def = d
		}
		cfg.NameChangeMinConfirmationsByChain[chain] = int(getEnvInt64("NAME_CHANGE_MIN_CONFIRMATIONS_"+strings.ToUpper(chain), int64(def)))
	}
	if raw := strings.TrimSpace(getEnv("TSA_PAY_RESERVED_BUSINESS_NAMES", "")); raw != "" {
		for _, w := range strings.Split(raw, ",") {
			if w = strings.TrimSpace(w); w != "" {
				cfg.TSAPayReservedBusinessNames = append(cfg.TSAPayReservedBusinessNames, w)
			}
		}
	}
	// SP6 reward distributor — UNSET keeps root-publishing dormant.
	cfg.RewardDistributorAddress = strings.TrimSpace(getEnv("REWARD_DISTRIBUTOR_ADDRESS", ""))
	// P2PEscrow proxy address — preset by SONIC_NETWORK; override via env if
	// you need to point at a hot-fix deployment.
	cfg.P2PEscrowAddress = strings.TrimSpace(getEnv("P2P_ESCROW_ADDRESS", preset.p2pEscrow))
	// Must match the deployed contract's EIP-712 domain version (moves in
	// lockstep with P2P_ESCROW_ADDRESS; override only when overriding the addr).
	cfg.P2PEscrowEIP712Version = getEnv("P2P_ESCROW_EIP712_VERSION", preset.eip712Version)

	// Mailjet email service
	cfg.MailjetAPIKey = os.Getenv("MAILJET_API_KEY")
	cfg.MailjetSecretKey = os.Getenv("MAILJET_SECRET_KEY")

	// Expo push notifications (optional)
	cfg.ExpoAccessToken = os.Getenv("EXPO_ACCESS_TOKEN")

	// Didit KYC
	cfg.DiditAPIKey = os.Getenv("DIDIT_API_KEY")
	cfg.DiditWorkflowID = os.Getenv("DIDIT_WORKFLOW_ID")
	cfg.DiditBaseURL = getEnv("DIDIT_BASE_URL", "https://verification.didit.me/v3")
	cfg.DiditWebhookSecret = os.Getenv("DIDIT_WEBHOOK_SECRET")
	cfg.DiditCallbackURL = getEnv("DIDIT_CALLBACK_URL", "tsaconnect://kyc/callback")

	// LiFi swap aggregator. API key is optional — without it we hit the
	// anonymous tier (rate-limited). Integrator + fee are also optional;
	// when both are set we collect a percentage cut, which requires a
	// matching registration at portal.li.fi with a fee wallet configured.
	cfg.LiFiAPIKey = os.Getenv("LIFI_API_KEY")
	cfg.CoinGeckoAPIKey = strings.TrimSpace(os.Getenv("COINGECKO_API_KEY"))
	cfg.CoinGeckoPro = strings.EqualFold(strings.TrimSpace(os.Getenv("COINGECKO_API_PLAN")), "pro")
	cfg.LiFiIntegratorID = os.Getenv("LIFI_INTEGRATOR_ID")
	cfg.LiFiIntegratorFee = os.Getenv("LIFI_INTEGRATOR_FEE")
	// LIFI_BASE_URL exists so a local rehearsal can point every LiFi client at
	// a stand-in server — production must leave it unset. Resolved through
	// ResolveLiFiBaseURL so this is the one place the default/normalisation
	// logic lives; every call site downstream uses cfg.LiFiBaseURL directly.
	cfg.LiFiBaseURL = ResolveLiFiBaseURL(os.Getenv("LIFI_BASE_URL"))

	// AllSwap server-verified settle thresholds (design §8, I8).
	cfg.AllSwapFeeDeviationMax = getEnvFloat64("ALLSWAP_FEE_DEVIATION_MAX", 0.10)
	cfg.AllSwapNominalFeeBps = int(getEnvFloat64("ALLSWAP_NOMINAL_FEE_BPS", 300))

	// Open Exchange Rates API — free tier: 1,000 req/month, cached at 1 hour
	cfg.OpenExchangeRatesAppID = os.Getenv("OPEN_EXCHANGE_RATES_APP_ID")
	// "*" returns every ISO 4217 fiat currency in the metadata table; otherwise comma-separated.
	currenciesStr := getEnv("SUPPORTED_CURRENCIES", "*")
	cfg.SupportedCurrencies = splitAndTrim(currenciesStr, ",")

	// Bybit P2P routing — only currencies with deep P2P liquidity reflect real
	// market rates. GHS joins NGN here: Bybit has quoted a GHS book since 2022
	// and a live probe found it comfortably past the depth floor, so pricing it
	// off the parallel market rather than OER's official cedi rate avoids a
	// systematic margin error on every Ghanaian trade.
	bybitCurrenciesStr := getEnv("BYBIT_P2P_CURRENCIES", "NGN,GHS")
	cfg.BybitP2PCurrencies = splitAndTrim(bybitCurrenciesStr, ",")

	// Bybit probe amounts — "CUR:AMT,CUR:AMT", each roughly a $3–5 retail ticket.
	// NGN→5000, GHS→50 (~$4 at ~12 GHS/USD).
	cfg.BybitProbeAmounts = parseProbeAmounts(getEnv("BYBIT_PROBE_AMOUNTS", "NGN:5000,GHS:50"))

	// A Bybit-routed currency with no probe amount is a silent mispricing, not a
	// missing nicety: fetchBybitSide omits the amount filter entirely when the
	// probe is absent, which reads the merchant tier of the book instead of the
	// retail tier. Worse, parseProbeAmounts skips malformed entries silently, so
	// a typo in BYBIT_PROBE_AMOUNTS looks identical to a correct config. Drop
	// the currency from the anchored set rather than price it off the wrong tier.
	if kept := keepProbedCurrencies(cfg.BybitP2PCurrencies, cfg.BybitProbeAmounts); len(kept) != len(cfg.BybitP2PCurrencies) {
		cfg.BybitP2PCurrencies = kept
	}

	// P2P float-rate offset cap (basis points). 1000 = ±10%, matches Binance
	// P2P's typical platform-wide deviation cap.
	cfg.P2PFloatOffsetMaxBps = int(getEnvInt64("P2P_FLOAT_OFFSET_MAX_BPS", 1000))
	// Non-positive overrides fall back to the default: none of these is ever "off".
	cfg.P2PFixedRateBandBps = int(getEnvInt64("P2P_FIXED_RATE_BAND_BPS", DefaultP2PFixedRateBandBps))
	if cfg.P2PFixedRateBandBps <= 0 {
		cfg.P2PFixedRateBandBps = DefaultP2PFixedRateBandBps
	}
	cfg.P2PPaidUnreleasedAlertAfter = getEnvDuration("P2P_PAID_UNRELEASED_ALERT_AFTER", DefaultP2PPaidUnreleasedAlertAfter)
	cfg.P2PGasTopUpsPerDay = int(getEnvInt64("P2P_GAS_TOPUPS_PER_DAY", DefaultP2PGasTopUpsPerDay))
	if cfg.P2PGasTopUpsPerDay <= 0 {
		cfg.P2PGasTopUpsPerDay = DefaultP2PGasTopUpsPerDay
	}

	// Nomba — sandbox by default. Production should set NOMBA_BASE_URL to
	// https://api.nomba.com and provide live credentials.
	cfg.NombaBaseURL = getEnv("NOMBA_BASE_URL", "https://sandbox.nomba.com")
	cfg.NombaClientID = os.Getenv("NOMBA_CLIENT_ID")
	cfg.NombaClientSecret = os.Getenv("NOMBA_CLIENT_SECRET")
	cfg.NombaAccountID = os.Getenv("NOMBA_ACCOUNT_ID")
	cfg.NombaSenderName = getEnv("NOMBA_SENDER_NAME", "TSA Connect")

	// P2P merchant-reputation thresholds.
	cfg.P2PTrustedMinReleased = getEnvInt64("P2P_TRUSTED_MIN_RELEASED", 50)
	cfg.P2PTrustedMinCompletion = getEnvFloat64("P2P_TRUSTED_MIN_COMPLETION", 0.95)
	cfg.P2PTrustedMinAgeDays = int(getEnvInt64("P2P_TRUSTED_MIN_AGE_DAYS", 30))

	// PM automation (Nomba buy/sell auto-settle). All optional + dormant-by-
	// default: no PM_RELEASE_PRIVATE_KEY (and signer != "kms") ⇒ no signer ⇒ the
	// automation service + webhook + admin PM routes stay off; the route stubs
	// surface a clear "not configured" error.
	cfg.PMAdminEOAAddress = strings.TrimSpace(getEnv("PM_ADMIN_EOA_ADDRESS", ""))
	cfg.PMReleaseSigner = strings.ToLower(strings.TrimSpace(getEnv("PM_RELEASE_SIGNER", "local")))
	cfg.PMReleasePrivateKey = strings.TrimSpace(getEnv("PM_RELEASE_PRIVATE_KEY", ""))
	cfg.NombaWebhookSecret = strings.TrimSpace(getEnv("NOMBA_WEBHOOK_SECRET", ""))

	// Flutterwave — sandbox by default; production must set FLUTTERWAVE_BASE_URL
	// (the prod base is mid-migration on Flutterwave's side — verify at go-live).
	cfg.FlutterwaveBaseURL = getEnv("FLUTTERWAVE_BASE_URL", "https://developersandbox-api.flutterwave.com")
	cfg.FlutterwaveIdpURL = getEnv("FLUTTERWAVE_IDP_URL", "https://idp.flutterwave.com")
	cfg.FlutterwaveClientID = strings.TrimSpace(os.Getenv("FLUTTERWAVE_CLIENT_ID"))
	cfg.FlutterwaveClientSecret = strings.TrimSpace(os.Getenv("FLUTTERWAVE_CLIENT_SECRET"))
	cfg.FlutterwaveWebhookSecret = strings.TrimSpace(getEnv("FLUTTERWAVE_WEBHOOK_SECRET", ""))
	cfg.FlutterwaveMerchantID = strings.TrimSpace(getEnv("FLUTTERWAVE_MERCHANT_ID", ""))
	cfg.FlutterwaveCustomerID = strings.TrimSpace(getEnv("FLUTTERWAVE_CUSTOMER_ID", ""))
	cfg.FlutterwaveCompanyName = strings.TrimSpace(getEnv("FLUTTERWAVE_COMPANY_NAME", "TSA Connect"))
	cfg.FlutterwaveCompanyEmail = strings.TrimSpace(getEnv("FLUTTERWAVE_COMPANY_EMAIL", "payments@mcgpchain.com"))
	cfg.FlutterwaveCompanyPhone = strings.TrimSpace(getEnv("FLUTTERWAVE_COMPANY_PHONE", ""))

	// Central-account inbound attribution. Defaults to `va` — the mode that is
	// live today — so a deploy that forgets this variable changes nothing.
	// An unrecognised value is treated as `va` for the same reason: a typo must
	// not silently move every payer onto the company account and stop the app
	// showing the virtual accounts they have been paying into.
	cfg.InboundMode = normalizeInboundMode(getEnv("INBOUND_MODE", InboundModeVA))
	cfg.InboundPollInterval = getEnvDuration("INBOUND_POLL_INTERVAL", 30*time.Second)
	cfg.InboundStatementLookback = getEnvDuration("INBOUND_STATEMENT_LOOKBACK", 24*time.Hour)
	cfg.InboundCheckCooldown = getEnvDuration("INBOUND_CHECK_COOLDOWN", 10*time.Second)
	cfg.InboundCentralAccountNumber = os.Getenv("INBOUND_CENTRAL_ACCOUNT_NUMBER")
	cfg.InboundCentralBankName = os.Getenv("INBOUND_CENTRAL_BANK_NAME")
	cfg.InboundCentralAccountName = os.Getenv("INBOUND_CENTRAL_ACCOUNT_NAME")
	cfg.InboundAlertEmails = dedupeEmails(splitAndTrim(getEnv("INBOUND_ALERT_EMAILS", ""), ","))

	// Stablecoin ⇄ fiat ramp (brokered through Flutterwave; no float is held).
	cfg.StableSpreadBps = getEnvInt64("IT_STABLE_SPREAD_BPS", 155)
	cfg.StableRateLockTTL = time.Duration(getEnvInt64("IT_STABLE_RATE_LOCK_SECONDS", 900)) * time.Second
	cfg.StableOverDeliveryTolBps = getEnvInt64("IT_STABLE_OVER_DELIVERY_TOL_BPS", 200)
	cfg.StableMinSell = strings.TrimSpace(getEnv("IT_STABLE_MIN_SELL", "10"))
	cfg.FWStableDepositAddressPolygon = strings.TrimSpace(getEnv("FW_STABLE_DEPOSIT_ADDRESS_POLYGON", ""))

	// Deposit/withdrawal fee-quote engine (PM fee spec, batch 11). Two values
	// are PM-UNRESOLVED and intentionally config-first so flipping them is an
	// env change, not a deploy:
	//   FEE_PLATFORM_STABLE_USD — PM gave both $1 and $2; default 2.
	//   FEE_TAX_BASIS           — "fee" matches the PM's receipt (VAT charged
	//                             on the provider fee); "amount" matches their
	//                             prose. Default "fee" (receipt evidence).
	cfg.FeeQuote = FeeQuoteConfig{
		PlatformStableUSD:    getEnvInt64("FEE_PLATFORM_STABLE_USD", 2),
		PlatformNgnBps:       getEnvInt64("FEE_PLATFORM_NGN_BPS", 50),
		PlatformFxBps:        getEnvInt64("FEE_PLATFORM_FX_BPS", 200),
		ProviderFallbackBps:  getEnvInt64("FEE_PROVIDER_FALLBACK_BPS", 200),
		TaxBasis:             strings.ToLower(strings.TrimSpace(getEnv("FEE_TAX_BASIS", "fee"))),
		VATBps:               getEnvInt64("FEE_VAT_BPS", 750),
		PlatformTaxBps:       getEnvInt64("FEE_PLATFORM_TAX_BPS", 50),
		McgpSwapBps:          getEnvInt64("FEE_MCGP_SWAP_BPS", 200),
		LifiRouteBps:         getEnvInt64("FEE_LIFI_ROUTE_BPS", 300),
		StableDepositConvBps: getEnvInt64("FEE_STABLE_DEPOSIT_CONV_BPS", 200),
	}

	// Fee/TP overhaul (SP3). TP minting rate defaults to 1 TP = $1 of pool
	// value; tunable without code change.
	cfg.TPPerUSD = getEnvFloat64("FEE_TP_PER_USD", 100.0)
	// SP4 global-pool settlement cadence — default daily.
	cfg.GlobalPoolSettleInterval = getEnvDuration("FEE_GLOBAL_POOL_SETTLE_INTERVAL", 24*time.Hour)

	// SP7 claim-eligibility velocity heuristic. ON by default (advisory auto-
	// flags only gate the CLAIM, never accrual). Tunable + disable-able via env:
	//   REWARD_VELOCITY_ENABLED      — set false to turn the heuristic off entirely
	//   REWARD_VELOCITY_MAX_SIGNUPS  — direct-downline signups allowed per window
	//   REWARD_VELOCITY_WINDOW       — Go duration string (e.g. "24h", "1h")
	// Default: >20 direct referrals in 24h trips an advisory flag. Sensible for
	// an early-stage platform with few users — a genuine power-referrer above it
	// is simply reviewed + cleared by an admin.
	cfg.RewardVelocityEnabled = getEnvBool("REWARD_VELOCITY_ENABLED", true)
	cfg.RewardVelocityMaxSignups = int(getEnvInt64("REWARD_VELOCITY_MAX_SIGNUPS", 20))
	cfg.RewardVelocityWindow = getEnvDuration("REWARD_VELOCITY_WINDOW", 24*time.Hour)

	// v2 fee/TP engine kill-switch — DEFAULT OFF until the documented cutover.
	cfg.FeeModelV2Enabled = getEnvBool("FEE_MODEL_V2_ENABLED", false)

	cfg.NonEVMChains = buildNonEVMChains()

	// ── Mainnet configuration ──
	// Token addresses sourced from each chain's official documentation. Public
	// RPC URLs are used as defaults so the service boots without paid keys;
	// production deployments should override with Alchemy/Infura/QuickNode in
	// env to avoid public-RPC rate limits. Every *_RPC_URL also takes a
	// comma-separated failover list, primary first (blockchain.NewEVMClient):
	// Base and Polygon default to one, as their single public RPCs rate-limit
	// (Base, 429) or are gone (polygon-rpc.com: "API key disabled").
	mainnetChains := map[string]ChainConfig{
		"sonic":     {Name: "Sonic Network", RPCURL: getEnv("SONIC_MAINNET_RPC_URL", "https://rpc.soniclabs.com"), ChainID: getEnvInt64("SONIC_MAINNET_CHAIN_ID", 146), NativeCurrency: "S"},
		"bsc":       {Name: "BNB Smart Chain", RPCURL: getEnv("BSC_MAINNET_RPC_URL", "https://bsc-dataseed.binance.org"), ChainID: getEnvInt64("BSC_MAINNET_CHAIN_ID", 56), NativeCurrency: "BNB"},
		"ethereum":  {Name: "Ethereum", RPCURL: getEnv("ETHEREUM_MAINNET_RPC_URL", "https://eth.llamarpc.com"), ChainID: getEnvInt64("ETHEREUM_MAINNET_CHAIN_ID", 1), NativeCurrency: "ETH"},
		"polygon":   {Name: "Polygon", RPCURL: getEnv("POLYGON_MAINNET_RPC_URL", "https://polygon-bor-rpc.publicnode.com,https://rpc-mainnet.matic.quiknode.pro,https://polygon.drpc.org"), ChainID: getEnvInt64("POLYGON_MAINNET_CHAIN_ID", 137), NativeCurrency: "POL"},
		"arbitrum":  {Name: "Arbitrum One", RPCURL: getEnv("ARBITRUM_MAINNET_RPC_URL", "https://arb1.arbitrum.io/rpc"), ChainID: getEnvInt64("ARBITRUM_MAINNET_CHAIN_ID", 42161), NativeCurrency: "ETH"},
		"base":      {Name: "Base", RPCURL: getEnv("BASE_MAINNET_RPC_URL", "https://developer-access-mainnet.base.org,https://mainnet.base.org,https://base.gateway.tenderly.co"), ChainID: getEnvInt64("BASE_MAINNET_CHAIN_ID", 8453), NativeCurrency: "ETH"},
		"optimism":  {Name: "Optimism", RPCURL: getEnv("OPTIMISM_MAINNET_RPC_URL", "https://mainnet.optimism.io"), ChainID: getEnvInt64("OPTIMISM_MAINNET_CHAIN_ID", 10), NativeCurrency: "ETH"},
		"avalanche": {Name: "Avalanche C-Chain", RPCURL: getEnv("AVALANCHE_MAINNET_RPC_URL", "https://api.avax.network/ext/bc/C/rpc"), ChainID: getEnvInt64("AVALANCHE_MAINNET_CHAIN_ID", 43114), NativeCurrency: "AVAX"},
		"linea":     {Name: "Linea", RPCURL: getEnv("LINEA_MAINNET_RPC_URL", "https://rpc.linea.build"), ChainID: getEnvInt64("LINEA_MAINNET_CHAIN_ID", 59144), NativeCurrency: "ETH"},
		"robinhood": {Name: "Robinhood Chain", RPCURL: getEnv("ROBINHOOD_MAINNET_RPC_URL", "https://rpc.mainnet.chain.robinhood.com"), ChainID: getEnvInt64("ROBINHOOD_MAINNET_CHAIN_ID", 4663), NativeCurrency: "ETH"},
	}
	mainnetTokens := map[string]string{}
	if cfg.MCGPTokenAddress != "" {
		mainnetTokens["sonic:MCGP"] = cfg.MCGPTokenAddress
	}
	// Mainnet stablecoin addresses. Defaults are the canonical (most-used)
	// deployments per chain; override individually via env if a different
	// bridge/wrapper is preferred.
	for tokenKey, def := range map[string]string{
		"sonic:USDT":     getEnv("MAINNET_USDT_ADDRESS", "0x6047828dc181963ba44974801ff68e538da5eaf9"),
		"sonic:USDC":     getEnv("MAINNET_USDC_ADDRESS", "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"),
		"bsc:USDT":       getEnv("BSC_MAINNET_USDT_ADDRESS", "0x55d398326f99059fF775485246999027B3197955"),
		"bsc:USDC":       getEnv("BSC_MAINNET_USDC_ADDRESS", "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d"),
		"ethereum:USDT":  getEnv("ETHEREUM_MAINNET_USDT_ADDRESS", "0xdAC17F958D2ee523a2206206994597C13D831ec7"),
		"ethereum:USDC":  getEnv("ETHEREUM_MAINNET_USDC_ADDRESS", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
		"polygon:USDT":   getEnv("POLYGON_MAINNET_USDT_ADDRESS", "0xc2132D05D31c914a87C6611C10748AEb04B58e8F"),
		"polygon:USDC":   getEnv("POLYGON_MAINNET_USDC_ADDRESS", "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"),
		"arbitrum:USDT":  getEnv("ARBITRUM_MAINNET_USDT_ADDRESS", "0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9"),
		"arbitrum:USDC":  getEnv("ARBITRUM_MAINNET_USDC_ADDRESS", "0xaf88d065e77c8cC2239327C5EDb3A432268e5831"),
		"base:USDT":      getEnv("BASE_MAINNET_USDT_ADDRESS", "0xfde4C96c8593536E31F229EA8f37b2ADa2699bb2"),
		"base:USDC":      getEnv("BASE_MAINNET_USDC_ADDRESS", "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"),
		"optimism:USDT":  getEnv("OPTIMISM_MAINNET_USDT_ADDRESS", "0x94b008aA00579c1307B0EF2c499aD98a8ce58e58"),
		"optimism:USDC":  getEnv("OPTIMISM_MAINNET_USDC_ADDRESS", "0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85"),
		"avalanche:USDT": getEnv("AVALANCHE_MAINNET_USDT_ADDRESS", "0x9702230A8Ea53601f5cD2dc00fDBc13d4dF4A8c7"),
		"avalanche:USDC": getEnv("AVALANCHE_MAINNET_USDC_ADDRESS", "0xB97EF9Ef8734C71904D8002F8b6Bc66Dd9c48a6E"),
		"linea:USDT":     getEnv("LINEA_MAINNET_USDT_ADDRESS", "0xA219439258ca9da29E9Cc4cE5596924745e12B93"),
		"linea:USDC":     getEnv("LINEA_MAINNET_USDC_ADDRESS", "0x176211869cA2b568f2A7D4EE941E073a821EE1ff"),
		// Robinhood Chain's canonical stablecoin is USDG (Paxos Global Dollar);
		// bridged USDC/USDT arrive as USDG, so USDG is the one to wire here.
		"robinhood:USDG": getEnv("ROBINHOOD_MAINNET_USDG_ADDRESS", "0x5fc5360D0400a0Fd4f2af552ADD042D716F1d168"),
	} {
		if def != "" {
			mainnetTokens[tokenKey] = def
		}
	}

	// ── Testnet configuration ──
	// Testnet RPC defaults are public testnet endpoints; testnet token
	// addresses are intentionally left empty for chains where we don't have
	// a verified faucet/bridge stablecoin deployment to point at. Setting an
	// env var promotes a chain's testnet stablecoin in production.
	testnetChains := map[string]ChainConfig{
		"sonic":     {Name: "Sonic Testnet", RPCURL: getEnv("SONIC_TESTNET_RPC_URL", "https://rpc.testnet.soniclabs.com"), ChainID: getEnvInt64("SONIC_TESTNET_CHAIN_ID", 14601), NativeCurrency: "S"},
		"bsc":       {Name: "BSC Testnet", RPCURL: getEnv("BSC_TESTNET_RPC_URL", "https://data-seed-prebsc-1-s1.binance.org:8545"), ChainID: getEnvInt64("BSC_TESTNET_CHAIN_ID", 97), NativeCurrency: "tBNB"},
		"ethereum":  {Name: "Sepolia", RPCURL: getEnv("ETHEREUM_TESTNET_RPC_URL", "https://ethereum-sepolia-rpc.publicnode.com"), ChainID: getEnvInt64("ETHEREUM_TESTNET_CHAIN_ID", 11155111), NativeCurrency: "ETH"},
		"polygon":   {Name: "Polygon Amoy", RPCURL: getEnv("POLYGON_TESTNET_RPC_URL", "https://rpc-amoy.polygon.technology"), ChainID: getEnvInt64("POLYGON_TESTNET_CHAIN_ID", 80002), NativeCurrency: "POL"},
		"arbitrum":  {Name: "Arbitrum Sepolia", RPCURL: getEnv("ARBITRUM_TESTNET_RPC_URL", "https://sepolia-rollup.arbitrum.io/rpc"), ChainID: getEnvInt64("ARBITRUM_TESTNET_CHAIN_ID", 421614), NativeCurrency: "ETH"},
		"base":      {Name: "Base Sepolia", RPCURL: getEnv("BASE_TESTNET_RPC_URL", "https://sepolia.base.org"), ChainID: getEnvInt64("BASE_TESTNET_CHAIN_ID", 84532), NativeCurrency: "ETH"},
		"optimism":  {Name: "OP Sepolia", RPCURL: getEnv("OPTIMISM_TESTNET_RPC_URL", "https://sepolia.optimism.io"), ChainID: getEnvInt64("OPTIMISM_TESTNET_CHAIN_ID", 11155420), NativeCurrency: "ETH"},
		"avalanche": {Name: "Avalanche Fuji", RPCURL: getEnv("AVALANCHE_TESTNET_RPC_URL", "https://api.avax-test.network/ext/bc/C/rpc"), ChainID: getEnvInt64("AVALANCHE_TESTNET_CHAIN_ID", 43113), NativeCurrency: "AVAX"},
		"linea":     {Name: "Linea Sepolia", RPCURL: getEnv("LINEA_TESTNET_RPC_URL", "https://rpc.sepolia.linea.build"), ChainID: getEnvInt64("LINEA_TESTNET_CHAIN_ID", 59141), NativeCurrency: "ETH"},
		"robinhood": {Name: "Robinhood Testnet", RPCURL: getEnv("ROBINHOOD_TESTNET_RPC_URL", "https://rpc.testnet.chain.robinhood.com"), ChainID: getEnvInt64("ROBINHOOD_TESTNET_CHAIN_ID", 46630), NativeCurrency: "ETH"},
	}
	testnetTokens := map[string]string{}
	// Sonic testnet token defaults come from the testnet preset (the same
	// source the P2P stack uses when SONIC_NETWORK=testnet). This keeps the
	// wallet's token-discovery surface in sync with what the P2P escrow has
	// whitelisted — without it, the wallet looks up sonic:USDT against
	// mainnet addresses on the testnet RPC and shows nothing.
	testnetSonic := sonicPresets["testnet"]
	if addr := getEnv("TESTNET_MCGP_ADDRESS", testnetSonic.mcgpAddress); addr != "" {
		testnetTokens["sonic:MCGP"] = addr
	}
	if addr := getEnv("TESTNET_USDT_ADDRESS", testnetSonic.usdtAddress); addr != "" {
		testnetTokens["sonic:USDT"] = addr
	}
	if addr := getEnv("TESTNET_USDC_ADDRESS", testnetSonic.usdcAddress); addr != "" {
		testnetTokens["sonic:USDC"] = addr
	}
	if addr := getEnv("BSC_TESTNET_USDT_ADDRESS", getEnv("BSC_USDT_ADDRESS", "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7")); addr != "" {
		testnetTokens["bsc:USDT"] = addr
	}
	if addr := getEnv("BSC_TESTNET_USDC_ADDRESS", getEnv("BSC_USDC_ADDRESS", "")); addr != "" {
		testnetTokens["bsc:USDC"] = addr
	}
	// Optional testnet stablecoin overrides for the new chains. These are
	// off by default — set the env var only when the BE is pointed at a
	// specific testnet faucet token.
	for tokenKey, env := range map[string]string{
		"ethereum:USDT":  "ETHEREUM_TESTNET_USDT_ADDRESS",
		"ethereum:USDC":  "ETHEREUM_TESTNET_USDC_ADDRESS",
		"polygon:USDT":   "POLYGON_TESTNET_USDT_ADDRESS",
		"polygon:USDC":   "POLYGON_TESTNET_USDC_ADDRESS",
		"arbitrum:USDT":  "ARBITRUM_TESTNET_USDT_ADDRESS",
		"arbitrum:USDC":  "ARBITRUM_TESTNET_USDC_ADDRESS",
		"base:USDT":      "BASE_TESTNET_USDT_ADDRESS",
		"base:USDC":      "BASE_TESTNET_USDC_ADDRESS",
		"optimism:USDT":  "OPTIMISM_TESTNET_USDT_ADDRESS",
		"optimism:USDC":  "OPTIMISM_TESTNET_USDC_ADDRESS",
		"avalanche:USDT": "AVALANCHE_TESTNET_USDT_ADDRESS",
		"avalanche:USDC": "AVALANCHE_TESTNET_USDC_ADDRESS",
		"linea:USDT":     "LINEA_TESTNET_USDT_ADDRESS",
		"linea:USDC":     "LINEA_TESTNET_USDC_ADDRESS",
		// No verified Robinhood testnet USDG deployment is published; env-gated
		// so it can be promoted once a faucet/bridge address is known.
		"robinhood:USDG": "ROBINHOOD_TESTNET_USDG_ADDRESS",
	} {
		if addr := os.Getenv(env); addr != "" {
			testnetTokens[tokenKey] = addr
		}
	}

	cfg.Networks = map[string]NetworkConfig{
		"mainnet": {Chains: mainnetChains, TokenAddresses: mainnetTokens,
			// The route-2 canary must confirm Flutterwave sends native USDC, not USDC.e.
			FlutterwavePolygonUSDC: "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"},
		// Flutterwave has no verified testnet Polygon delivery, so route 2 stays off.
		"testnet": {Chains: testnetChains, TokenAddresses: testnetTokens},
	}

	// Legacy flat fields point to mainnet for backward compat
	cfg.Chains = mainnetChains
	cfg.TokenAddresses = mainnetTokens

	// ── Per-chain P2P + FeeManager registries (spec 2026-10-08 §5) ────────
	// Assembled last: every row takes its chain id and tokens from cfg.Networks.
	if err := retiredP2PEnv(); err != nil {
		log.Fatalf("[config] %v", err)
	}
	if err := assembleP2PChains(cfg, p2pPresets); err != nil {
		log.Fatalf("[config] %v", err)
	}
	assembleFeeManagerChains(cfg)
	if err := assembleLiquidityPool(cfg, liquidityPoolPresets); err != nil {
		log.Fatalf("[config] %v", err)
	}
	return cfg
}

// buildNonEVMChains configures non-EVM chains per network, keyed
// "<network>:<chainKey>" (e.g. "mainnet:tron"), mirroring the EVM Networks map.
// Each (network, chain) is dormant unless its RPC env is present.
func buildNonEVMChains() map[string]NonEVMChainConfig {
	out := map[string]NonEVMChainConfig{}
	apiKey := os.Getenv("TRON_API_KEY") // one TronGrid key works across networks

	addTron := func(network, rpc, usdt string) {
		if rpc == "" {
			return
		}
		tokens := map[string]string{}
		if usdt != "" {
			tokens["USDT"] = usdt
		}
		out[network+":tron"] = NonEVMChainConfig{
			ChainKey:       "tron",
			Family:         "tron",
			Network:        network,
			RPCURL:         rpc,
			APIKey:         apiKey,
			NativeSymbol:   "TRX",
			NativeDecimals: 6,
			Tokens:         tokens,
		}
	}

	// Preferred: explicit per-network config, so both can be live at once.
	addTron("mainnet", os.Getenv("TRON_MAINNET_RPC_URL"), os.Getenv("TRON_MAINNET_USDT_ADDRESS"))
	addTron("testnet", os.Getenv("TRON_TESTNET_RPC_URL"), os.Getenv("TRON_TESTNET_USDT_ADDRESS"))

	// Backward-compat: a single TRON_RPC_URL maps to the network named by
	// TRON_NETWORK (default mainnet), unless that network was already set above.
	if rpc := os.Getenv("TRON_RPC_URL"); rpc != "" {
		network := getEnv("TRON_NETWORK", "mainnet")
		if _, exists := out[network+":tron"]; !exists {
			addTron(network, rpc, os.Getenv("TRON_USDT_ADDRESS"))
		}
	}

	// BTC: RPCURL is an Esplora-compatible REST base (blockstream.info or
	// mempool.space). Same dormancy rule — unset env means the chain is off.
	addBTC := func(network, esploraBase string) {
		if esploraBase == "" {
			return
		}
		out[network+":btc"] = NonEVMChainConfig{
			ChainKey:       "btc",
			Family:         "btc",
			Network:        network,
			RPCURL:         esploraBase,
			NativeSymbol:   "BTC",
			NativeDecimals: 8,
		}
	}
	addBTC("mainnet", os.Getenv("BTC_MAINNET_ESPLORA_URL"))
	addBTC("testnet", os.Getenv("BTC_TESTNET_ESPLORA_URL"))

	// Solana: RPCURL is a JSON-RPC endpoint (public mainnet-beta/devnet, or a
	// keyed provider like Helius). Same dormancy rule — unset env = chain off.
	// Tokens map symbol → SPL mint; USDC/USDT are seeded when their mint env is
	// present, and users can still import any other mint at runtime.
	addSolana := func(network, rpc, usdcMint, usdtMint string) {
		if rpc == "" {
			return
		}
		tokens := map[string]string{}
		if usdcMint != "" {
			tokens["USDC"] = usdcMint
		}
		if usdtMint != "" {
			tokens["USDT"] = usdtMint
		}
		out[network+":solana"] = NonEVMChainConfig{
			ChainKey:       "solana",
			Family:         "solana",
			Network:        network,
			RPCURL:         rpc,
			NativeSymbol:   "SOL",
			NativeDecimals: 9,
			Tokens:         tokens,
		}
	}
	addSolana("mainnet",
		os.Getenv("SOLANA_MAINNET_RPC_URL"),
		os.Getenv("SOLANA_MAINNET_USDC_MINT"),
		os.Getenv("SOLANA_MAINNET_USDT_MINT"),
	)
	addSolana("testnet", // "testnet" binds to Solana devnet for the app's network toggle
		os.Getenv("SOLANA_DEVNET_RPC_URL"),
		os.Getenv("SOLANA_DEVNET_USDC_MINT"),
		os.Getenv("SOLANA_DEVNET_USDT_MINT"),
	)
	// Back-compat: a single SOLANA_RPC_URL maps to SOLANA_NETWORK (default
	// mainnet) unless that network was already configured above.
	if rpc := os.Getenv("SOLANA_RPC_URL"); rpc != "" {
		network := getEnv("SOLANA_NETWORK", "mainnet")
		if _, exists := out[network+":solana"]; !exists {
			addSolana(network, rpc, os.Getenv("SOLANA_USDC_MINT"), os.Getenv("SOLANA_USDT_MINT"))
		}
	}

	// Sui: RPCURL is a JSON-RPC fullnode endpoint (public mainnet/testnet or a
	// keyed provider). Same dormancy rule — unset env = chain off. Tokens map
	// symbol → coin type (e.g. "0x…::usdc::USDC"); USDC is seeded when its type
	// env is present, and users can import any other coin type at runtime.
	addSui := func(network, rpc, usdcType string) {
		if rpc == "" {
			return
		}
		tokens := map[string]string{}
		if usdcType != "" {
			tokens["USDC"] = usdcType
		}
		out[network+":sui"] = NonEVMChainConfig{
			ChainKey:       "sui",
			Family:         "sui",
			Network:        network,
			RPCURL:         rpc,
			NativeSymbol:   "SUI",
			NativeDecimals: 9,
			Tokens:         tokens,
		}
	}
	addSui("mainnet", os.Getenv("SUI_MAINNET_RPC_URL"), os.Getenv("SUI_MAINNET_USDC_TYPE"))
	addSui("testnet", os.Getenv("SUI_TESTNET_RPC_URL"), os.Getenv("SUI_TESTNET_USDC_TYPE"))
	// Back-compat: a single SUI_RPC_URL maps to SUI_NETWORK (default mainnet)
	// unless that network was already configured above.
	if rpc := os.Getenv("SUI_RPC_URL"); rpc != "" {
		network := getEnv("SUI_NETWORK", "mainnet")
		if _, exists := out[network+":sui"]; !exists {
			addSui(network, rpc, os.Getenv("SUI_USDC_TYPE"))
		}
	}
	return out
}

func getEnvFloat64(key string, fallback float64) float64 {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

// MaxCompanyTakeBps and DefaultCompanyTakeBps are RAMP_COMPANY_TAKE_BPS's one
// rule: 1..500 bps (Paycrest takes a sender fee of at most 5%), else 50.
const (
	MaxCompanyTakeBps     = 500
	DefaultCompanyTakeBps = 50
)

// ValidCompanyTakeBps is that rule, for any code that is handed a take.
func ValidCompanyTakeBps(bps int64) bool { return bps >= 1 && bps <= MaxCompanyTakeBps }

// companyTakeFromEnv resolves RAMP_COMPANY_TAKE_BPS once for every provider.
// Unset is the default; anything outside 1..500 (0, negative, too large, not
// a number) is the default too, loudly — a quote and an order must never
// read two different takes, nor Paycrest fall back to its dashboard fee.
func companyTakeFromEnv() int64 {
	raw, ok := os.LookupEnv("RAMP_COMPANY_TAKE_BPS")
	if !ok || strings.TrimSpace(raw) == "" {
		return DefaultCompanyTakeBps
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || !ValidCompanyTakeBps(v) {
		log.Printf("[config] ALERT ramp_company_take_invalid=1 RAMP_COMPANY_TAKE_BPS=%q is not a take in 1..%d bps: the default %d bps is in force for every provider",
			raw, MaxCompanyTakeBps, DefaultCompanyTakeBps)
		return DefaultCompanyTakeBps
	}
	return v
}

// removedPricingKeys are settings the 2026-10-07 pricing change retired. Left
// in an environment they do nothing, so an operator who edits one expecting a
// price to move is told at boot.
var removedPricingKeys = map[string]string{
	"PAYCREST_SENDER_FEE_PERCENT": "Paycrest orders send RAMP_COMPANY_TAKE_BPS; TSA Pay now reads TSA_PAY_FEE_PERCENT",
	"RAMP_ROUND_TRIP_BUDGET_BPS":  "conversions pass their route cost on at cost; TSA Pay now reads TSA_PAY_ROUND_TRIP_BUDGET_BPS",
	"RAMP_TREASURY_MARGIN_BPS":    "the company wallet prices at RAMP_COMPANY_TAKE_BPS",
	"RAMP_FLUTTERWAVE_SPREAD_BPS": "Flutterwave buys price at RAMP_COMPANY_TAKE_BPS",
}

func warnRemovedPricingKeys() {
	keys := make([]string, 0, len(removedPricingKeys))
	for k := range removedPricingKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, set := os.LookupEnv(k); set {
			log.Printf("[config] WARNING %s is set but no longer read and is ignored: %s. Remove it.", k, removedPricingKeys[k])
		}
	}
}

// MaxConvertedFeeBps bounds a converted fee cap from config: well above any
// fee we mean to charge, low enough that a typo ("2000" for "200") is refused
// rather than charged.
const MaxConvertedFeeBps = 500

// convertedFeeCapFromEnv reads a converted-fee cap. Unset keeps fallback; a
// value that is not a whole number in 1..MaxConvertedFeeBps (0 included) is 0:
// no converted buy or swapped sell on that chain, rather than a fallback more
// permissive than was asked.
func convertedFeeCapFromEnv(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 || v > MaxConvertedFeeBps {
		where := "on chains without their own cap"
		if chain := strings.TrimPrefix(key, "RAMP_CONVERTED_FEE_MAX_BPS_"); chain != key {
			where = "on " + strings.ToLower(chain)
		}
		log.Printf("[config] %s=%q is not a fee cap in 1..%d bps: converted buys and swapped sells %s are off", key, raw, MaxConvertedFeeBps, where)
		return 0
	}
	return v
}

// knownOTCDeployBlock is the creation block of a known OtcMarketplace
// deployment, or 0 (scan from genesis) for any other address.
func knownOTCDeployBlock(address string) uint64 {
	// Sonic mainnet desk; creation block found by bisecting eth_getCode on
	// 2026-09-28 (empty at 60909789, code at 60909790).
	if strings.EqualFold(strings.TrimSpace(address), "0xfEF5d571D47d21A9cd9c6aF423116557Bbb5BF8b") {
		return 60909790
	}
	return 0
}

func getEnvInt64(key string, fallback int64) int64 {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

// getEnvRenamed reads a renamed variable: the new name, else the old one (with
// a deprecation line so operators move it), else fallback. Blank counts as
// unset. The value is trimmed.
func getEnvRenamed(key, oldKey, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(oldKey)); v != "" {
		log.Printf("[config] DEPRECATED env %s: rename it to %s (still read for now)", oldKey, key)
		return v
	}
	return fallback
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

// DefaultLiFiBaseURL is LiFi's public API base. It is the single source of
// truth for every LiFi client the backend builds (AllSwap, ramp-conversion
// treasury, IT stable verifier) — none of them hardcode the literal
// themselves, they all resolve through ResolveLiFiBaseURL.
const DefaultLiFiBaseURL = "https://li.quest/v1"

// ResolveLiFiBaseURL normalises a raw LIFI_BASE_URL value: trims surrounding
// whitespace, strips a trailing slash, and falls back to DefaultLiFiBaseURL
// when the result is empty. LIFI_BASE_URL exists purely to let a local
// rehearsal point every LiFi client at a stand-in server — the real API
// cannot satisfy a status check for a transaction that only exists on a
// local chain fork — and must be left unset in production. A malformed value
// (blank, whitespace-only) falling through to the default rather than being
// used verbatim keeps a typo from silently sending real requests to a
// nonsense host.
func ResolveLiFiBaseURL(raw string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return DefaultLiFiBaseURL
	}
	return trimmed
}

// normalizeEnv trims whitespace, lower-cases, and canonicalises "prod" to
// "production" so ENV="Production", " production ", or "prod" are all
// detected identically to the exact string "production". Every other value
// (including "development", "staging", "") passes through unchanged.
func normalizeEnv(raw string) string {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "prod" {
		return "production"
	}
	return e
}

// normalizeOrigin turns a configured origin into the form a browser sends in
// its Origin header: scheme and host lower-cased, no trailing slash. The
// cookie-session origin check compares against it byte for byte.
func normalizeOrigin(raw string) string {
	origin := strings.TrimRight(strings.TrimSpace(raw), "/")
	scheme, host, ok := strings.Cut(origin, "://")
	if !ok || scheme == "" || host == "" {
		return origin
	}
	return strings.ToLower(scheme) + "://" + strings.ToLower(host)
}

// WebAuthEnabled reports whether the web app's sign-in routes are on.
func (c *Config) WebAuthEnabled() bool { return c != nil && c.WebAppOrigin != "" }

// IsProduction reports whether this process is running in production. It
// re-normalises c.Env so it is correct even for a Config built by hand (e.g.
// in a test) rather than via Load(). Every raw-signer-key guard and the gin
// release-mode switch must call this rather than comparing c.Env directly —
// see rawSignerKeyAllowed in cmd/server for the raw-key-specific guard built
// on top of it.
func (c *Config) IsProduction() bool {
	if c == nil {
		return false
	}
	return normalizeEnv(c.Env) == "production"
}

// getEnvDuration parses a Go duration env var (e.g. "24h", "15m", "1h30m"). An
// unset, unparseable, or non-positive value yields the fallback.
func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := time.ParseDuration(strings.TrimSpace(value)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

// getEnvBool parses a boolean env var. Accepts the strconv.ParseBool set
// (1/t/T/TRUE/true/0/f/F/FALSE/false …). An unset or unparseable value yields
// the fallback.
func getEnvBool(key string, fallback bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
}

// AllSwapFeeDeviationMaxOrDefault returns the configured max client-fee
// deviation (fraction), defaulting to 0.10 (10%) when unset/invalid.
func (c *Config) AllSwapFeeDeviationMaxOrDefault() float64 {
	if c == nil || c.AllSwapFeeDeviationMax <= 0 {
		return 0.10
	}
	return c.AllSwapFeeDeviationMax
}

// AllSwapNominalFeeBpsOrDefault returns the nominal integrator fee bps used as
// the verified-fee basis when LiFi is reachable but reports no usable fee figure,
// default 300 (3%).
func (c *Config) AllSwapNominalFeeBpsOrDefault() int {
	if c == nil || c.AllSwapNominalFeeBps <= 0 {
		return 300
	}
	return c.AllSwapNominalFeeBps
}

// TPPerUSDOrDefault returns the configured TP minting rate, falling back to 100
// (1 TP = $0.01) when unset or non-positive so the engine always has a sane rate.
func (c *Config) TPPerUSDOrDefault() float64 {
	if c == nil || c.TPPerUSD <= 0 {
		return 100.0
	}
	return c.TPPerUSD
}

// GlobalPoolSettleIntervalOrDefault returns the SP4 settlement cadence, falling
// back to 24h (daily) when unset or non-positive so the job always has a sane
// tick period.
func (c *Config) GlobalPoolSettleIntervalOrDefault() time.Duration {
	if c == nil || c.GlobalPoolSettleInterval <= 0 {
		return 24 * time.Hour
	}
	return c.GlobalPoolSettleInterval
}

// Inbound attribution modes (central-account design §9).
const (
	// InboundModeVA — per-user Flutterwave virtual accounts, attribution by
	// charge.completed webhook. Today's behaviour and the default.
	InboundModeVA = "va"
	// InboundModeCentral — one company account; attribution by polling the
	// wallet statement and matching sender name + amount.
	InboundModeCentral = "central"
)

// normalizeInboundMode maps an env value onto a known mode, defaulting to `va`.
// Unknown values do NOT fail the boot: the safe reading of a typo is "leave the
// live behaviour alone". Guessing `central` from a near-miss is the far worse
// failure — every payer redirected onto the company account on the strength of
// a typo, or, with that account unconfigured, a fleet that refuses to boot.
func normalizeInboundMode(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), InboundModeCentral) {
		return InboundModeCentral
	}
	return InboundModeVA
}

// InboundCentralEnabled reports whether inbound naira is attributed through the
// central account. Read it rather than comparing InboundMode by hand so the
// pay-into paths (IT buy, stablecoin buy, P2P) and the virtual-account guards
// cannot drift apart on which account a payer is being sent to.
func (c *Config) InboundCentralEnabled() bool {
	return c != nil && c.InboundMode == InboundModeCentral
}

// dedupeEmails drops repeats from an operator-maintained address list,
// comparing case-insensitively because a mailbox is the same mailbox however
// it was typed. The first spelling is kept so the configured order — which is
// usually the escalation order — survives.
func dedupeEmails(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, addr := range in {
		key := strings.ToLower(addr)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, addr)
	}
	return out
}

// AdminConsoleBaseURL returns one origin an operator can actually click.
//
// ADMIN_URL is a comma-separated CORS allow-list, not a single site address,
// and it is ordered dev-first because that is convenient for CORS. A hold alert
// linking an on-call operator to http://localhost:5173 is worse than no link at
// all, so an https origin wins over anything else; the first entry is the
// fallback for a single-origin deployment.
func (c *Config) AdminConsoleBaseURL() string {
	if c == nil {
		return ""
	}
	origins := splitAndTrim(c.AdminURL, ",")
	for _, o := range origins {
		if strings.HasPrefix(strings.ToLower(o), "https://") {
			return strings.TrimRight(o, "/")
		}
	}
	if len(origins) == 0 {
		return ""
	}
	return strings.TrimRight(origins[0], "/")
}

// InboundPollIntervalOrDefault returns the statement poll period, falling back
// to the 30s of decision 8 when unset or non-positive.
func (c *Config) InboundPollIntervalOrDefault() time.Duration {
	if c == nil || c.InboundPollInterval <= 0 {
		return 30 * time.Second
	}
	return c.InboundPollInterval
}

// ResolveFiatProvider names the active fiat gateway. Flutterwave is the sole
// provider: Nomba never granted prod API access and is being retired, so the
// resolver hard-returns "flutterwave" and can never select Nomba — even when FW
// creds are absent the gateway simply stays dormant (its routes 503) rather than
// silently falling back to a provider we can't use. The provider seam and its
// interfaces remain so a replacement can be slotted in if FW ever refuses prod.
//
// Central so the payout gateway, webhook wiring, and bank-directory selection
// can never disagree on the active provider.
func (c *Config) ResolveFiatProvider() string {
	return "flutterwave"
}

// parseProbeAmounts parses a "CUR:AMT,CUR:AMT" list into a map. Entries
// missing a colon or with empty key/value are skipped silently — better
// than failing boot over a typo, since the caller treats absence as
// "no amount filter for this currency".
// keepProbedCurrencies returns the subset of currencies that have a usable
// probe amount, logging (loudly) any it drops.
//
// The pairing between BYBIT_P2P_CURRENCIES and BYBIT_PROBE_AMOUNTS is easy to
// break and impossible to notice: a missing or typo'd probe makes
// fetchBybitSide skip the amount filter, silently reading a different tier of
// the order book than every other anchored currency. Failing the boot would be
// worse — one bad env var should not take the service down — so the currency
// simply stops being treated as anchored and falls back to OER, which is the
// same posture as any other unanchored currency.
func keepProbedCurrencies(currencies []string, probes map[string]string) []string {
	kept := make([]string, 0, len(currencies))
	for _, c := range currencies {
		code := strings.ToUpper(strings.TrimSpace(c))
		if code == "" {
			continue
		}
		if strings.TrimSpace(probes[code]) == "" {
			log.Printf("[config] ALERT bybit_probe_missing_total=1 %s is in BYBIT_P2P_CURRENCIES "+
				"but has no BYBIT_PROBE_AMOUNTS entry — dropping it from the anchored set "+
				"(it would otherwise be priced off the merchant tier, not the retail tier)", code)
			continue
		}
		kept = append(kept, code)
	}
	return kept
}

func parseProbeAmounts(s string) map[string]string {
	out := map[string]string{}
	for _, part := range splitAndTrim(s, ",") {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		cur := strings.ToUpper(strings.TrimSpace(kv[0]))
		amt := strings.TrimSpace(kv[1])
		if cur == "" || amt == "" {
			continue
		}
		out[cur] = amt
	}
	return out
}

func splitAndTrim(s, sep string) []string {
	var result []string
	for _, part := range strings.Split(s, sep) {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
