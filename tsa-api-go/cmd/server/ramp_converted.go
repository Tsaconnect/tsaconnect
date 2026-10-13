package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
)

// Converted venues (spec 2026-10-03 §15): the company wallet (route 1) or
// Flutterwave (route 2) delivers a USDC leg into the conversion treasury,
// which the unchanged orchestrator bridges. Each route has no switch of its
// own: it follows its provider's switch plus RAMP_CONVERSION_BUY and the
// treasury's lanes, decided here at boot (companyWalletConverts,
// flutterwaveConverts). The decision gates quoting and opening only; nothing
// here is read by claim, send, handoff or the orchestrator.

// conversionTreasuryAddress is the conversion treasury's address, derived
// from its key alone — no RPC, whatever RAMP_CONVERSION_BUY says — so the
// company wallet can refuse a sale to it with no conversion record (§15.5 N1,
// Plan C6). The key is only read here, never used to sign.
func conversionTreasuryAddress(cfg *config.Config) string {
	return signerAddress(cfg.RampTreasuryPrivateKey) // "" when unset or bad; buildRampConversion reports a bad key
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

// companyWalletConverts is route 1's boot rule: the company-wallet provider
// is registered (RAMP_TREASURY_BUY and its key), RAMP_CONVERSION_BUY is on,
// and the conversion treasury has a lane on a chain the wallet sells its USDC
// leg on. The reason says why it is off ("" when on).
func companyWalletConverts(cfg *config.Config, registered bool, walletChains []string, treasury *services.Treasury) (bool, string) {
	switch {
	case !registered:
		return false, "company wallet not registered (RAMP_TREASURY_BUY + key)"
	case !cfg.RampConversionBuyEnabled:
		return false, "RAMP_CONVERSION_BUY off"
	case treasury == nil:
		return false, "no conversion treasury"
	}
	for _, chain := range walletChains {
		if lane := treasury.Lane(chain); lane != nil && lane.SponsorLane != nil {
			return true, ""
		}
	}
	return false, fmt.Sprintf("no conversion-treasury lane on the company wallet's chains %v", walletChains)
}

// flutterwaveConverts is route 2's boot rule: the Flutterwave provider is
// registered (RAMP_FLUTTERWAVE_BUY), RAMP_CONVERSION_BUY is on, the
// conversion treasury has a Polygon lane, and the USDC Flutterwave actually
// sends (the network bundle's FlutterwavePolygonUSDC) is that lane's USDC —
// otherwise the treasury would be credited a token it does not bridge from
// (N5). A mismatch is an ALERT, never fatal.
func flutterwaveConverts(cfg *config.Config, registered bool, treasury *services.Treasury) (bool, string) {
	switch {
	case !registered:
		return false, "Flutterwave provider not registered (RAMP_FLUTTERWAVE_BUY)"
	case !cfg.RampConversionBuyEnabled:
		return false, "RAMP_CONVERSION_BUY off"
	case treasury == nil:
		return false, "no conversion treasury"
	}
	lane := treasury.Lane("polygon")
	if lane == nil || lane.SponsorLane == nil {
		log.Printf("[ramp-converted] ALERT ramp_fw_converted_no_polygon_lane=1 — route 2 stays off")
		return false, "no Polygon conversion-treasury lane"
	}
	// Mainnet, as for the treasury's own lanes (buildRampConversion).
	want := strings.TrimSpace(cfg.Networks["mainnet"].FlutterwavePolygonUSDC)
	have := lane.Tokens["USDC"]
	if want == "" || !strings.EqualFold(have, want) {
		log.Printf("[ramp-converted] ALERT ramp_fw_converted_token_mismatch=1 flutterwave=%q lane=%q — route 2 stays off", want, have)
		return false, "Flutterwave's Polygon USDC is not the lane's USDC"
	}
	return true, ""
}

// offReason renders a route's off reason for the boot log ("" when on).
func offReason(why string) string {
	if why == "" {
		return ""
	}
	return " (" + why + ")"
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
			out[chain] = client.Probe()
		}
	}
	return out
}

// wireConvertedBuys hands the IT service the conversion treasury converted
// opens and attach-hash need (Plan C5). Without a treasury nothing is set, so
// no typed nil reaches ConvertedBuys.Treasury; the handoff itself is decided
// by the record and never by this wiring. feeCaps is the pricer whose
// RAMP_CONVERTED_FEE_MAX_BPS the opens re-check (nil: its default cap).
func wireConvertedBuys(it *services.ITService, treasury *services.Treasury, chains *services.BlockchainService, cfg *config.Config, feeCaps *services.PaycrestQuoteService) {
	if it == nil || treasury == nil {
		return
	}
	c := services.ConvertedBuys{
		TreasuryAddress: treasury.Address(), Treasury: treasury,
		Verifiers: rampTransferVerifiers(chains, evmChainKeys(cfg)),
		Probes:    conversionProbes(chains, treasury.ChainKeys()),
	}
	if feeCaps != nil { // never a typed nil in the interface
		c.FeeCaps = feeCaps
	}
	it.SetConvertedBuys(c)
}
