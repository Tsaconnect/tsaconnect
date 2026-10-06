package main

import (
	"log"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
)

// Converted venues (spec 2026-10-03 §15): the company wallet (route 1) or
// Flutterwave (route 2) delivers a USDC leg into the conversion treasury,
// which the unchanged orchestrator bridges. RAMP_TREASURY_CONVERTED and
// RAMP_FLUTTERWAVE_CONVERTED are read in main.go only and gate quoting and
// opening; nothing here is read by claim, send, handoff or the orchestrator.

// conversionTreasuryAddress is the conversion treasury's address, derived
// from its key alone — no RPC, whatever RAMP_CONVERSION_BUY says — so the
// company wallet can refuse a sale to it with no conversion record (§15.5 N1,
// Plan C6). The key is only read here, never used to sign.
func conversionTreasuryAddress(cfg *config.Config) string {
	raw := strings.TrimPrefix(strings.TrimSpace(cfg.RampTreasuryPrivateKey), "0x")
	if raw == "" {
		return ""
	}
	key, err := crypto.HexToECDSA(raw)
	if err != nil {
		return "" // buildRampConversion reports the bad key
	}
	return crypto.PubkeyToAddress(key.PublicKey).Hex()
}

// newCompanyWallet builds the company wallet knowing the conversion treasury.
func newCompanyWallet(cfg *config.Config, c services.CompanyWalletConfig) *services.CompanyWallet {
	c.ConversionTreasury = conversionTreasuryAddress(cfg)
	return services.NewCompanyWallet(c)
}

// convertedPricerFor is the converted pricer a provider gets, or a true nil
// (logged): only when the conversion treasury and its pricing were built.
func convertedPricerFor(cfg *config.Config, treasury *services.Treasury, quotes *services.PaycrestQuoteService, route string) ramp.ConvertedPricer {
	if !cfg.RampConversionBuyEnabled || treasury == nil || quotes == nil {
		log.Printf("[ramp-converted] %s dormant: RAMP_CONVERSION_BUY is off or the conversion treasury is not built", route)
		return nil
	}
	return quotes
}

// flutterwaveConvertedGate is route 2's boot gate (N5): a Polygon lane on the
// conversion treasury, and the USDC Flutterwave actually sends must be that
// lane's USDC — otherwise the treasury would be credited a token it does not
// bridge from.
func flutterwaveConvertedGate(cfg *config.Config, treasury *services.Treasury) bool {
	if treasury == nil {
		log.Printf("[ramp-converted] route 2 dormant: no conversion treasury")
		return false
	}
	lane := treasury.Lane("polygon")
	if lane == nil || lane.SponsorLane == nil {
		log.Printf("[ramp-converted] ALERT ramp_fw_converted_no_polygon_lane=1 — route 2 stays off")
		return false
	}
	want := strings.TrimSpace(cfg.RampFlutterwaveConvertedUSDC)
	have := lane.Tokens["USDC"]
	if want == "" || !strings.EqualFold(have, want) {
		log.Printf("[ramp-converted] ALERT ramp_fw_converted_token_mismatch=1 configured=%q lane=%q — route 2 stays off", want, have)
		return false
	}
	return true
}

// conversionProbes are the conversion lanes' chain clients, for the
// attach-hash depth check.
func conversionProbes(chains *services.BlockchainService, keys []string) map[string]services.TreasuryTxProbe {
	out := map[string]services.TreasuryTxProbe{}
	if chains == nil {
		return out
	}
	for _, chain := range keys {
		if client := chains.ClientForChain(chain); client != nil && client.Client != nil {
			out[chain] = client.Client
		}
	}
	return out
}

// wireConvertedBuys hands the IT service the conversion treasury converted
// opens and attach-hash need (Plan C5). Without a treasury nothing is set, so
// no typed nil reaches ConvertedBuys.Treasury; the handoff itself is decided
// by the record and never by this wiring.
func wireConvertedBuys(it *services.ITService, treasury *services.Treasury, chains *services.BlockchainService, cfg *config.Config) {
	if it == nil || treasury == nil {
		return
	}
	it.SetConvertedBuys(services.ConvertedBuys{
		TreasuryAddress: treasury.Address(), Treasury: treasury,
		Verifiers: rampTransferVerifiers(chains, evmChainKeys(cfg)),
		Probes:    conversionProbes(chains, treasury.ChainKeys()),
	})
}
