package main

import (
	"context"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// companyWalletProbeTimeout bounds one chain's receipt probe at boot.
const companyWalletProbeTimeout = 10 * time.Second

// buildCompanyWallet wires the company wallet (spec §4.1), or returns nil —
// dormant — with a line saying why. A key shared with any other hot signer is
// fatal (S1, Decision D50); everything else degrades to dormant. It never reads
// RAMP_TREASURY_BUY: that flag only decides whether the provider is registered
// (main.go, router block), so orders already open keep settling and delivering
// when it is off (review I1).
func buildCompanyWallet(cfg *config.Config, chains *services.BlockchainService) *services.CompanyWallet {
	if cfg.RampTreasuryWalletPrivateKey == "" {
		log.Printf("[ramp-company-wallet] dormant: RAMP_TREASURY_WALLET_PRIVATE_KEY not set")
		return nil
	}
	if !rawSignerKeyAllowed(cfg) {
		log.Printf("[ramp-company-wallet] dormant: raw RAMP_TREASURY_WALLET_PRIVATE_KEY refused in production (set ALLOW_RAW_SIGNER_KEYS=true, or use KMS)")
		return nil
	}
	alertRawSignerKeyInProduction(cfg, "RAMP_TREASURY_WALLET_PRIVATE_KEY")
	key, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.RampTreasuryWalletPrivateKey, "0x"))
	if err != nil {
		log.Printf("[ramp-company-wallet] dormant: invalid RAMP_TREASURY_WALLET_PRIVATE_KEY (not a 32-byte hex key)")
		return nil
	}
	// The one protection that holds in prod today: ENV=development there makes
	// rawSignerKeyAllowed allow every raw key (spec §7 caveat).
	if err := assertDistinctKey("RAMP_TREASURY_WALLET_PRIVATE_KEY", key, map[string]string{
		"RELAYER_PRIVATE_KEY":       cfg.RelayerPrivateKey,
		"IT_OPERATOR_PRIVATE_KEY":   cfg.ITOperatorPrivateKey,
		"PM_RELEASE_PRIVATE_KEY":    cfg.PMReleasePrivateKey,
		"RAMP_SPONSOR_PRIVATE_KEY":  cfg.RampSponsorPrivateKey,
		"RAMP_TREASURY_PRIVATE_KEY": cfg.RampTreasuryPrivateKey,
	}); err != nil {
		fatalf("[config] REFUSING TO START: %v", err)
		return nil
	}
	if chains == nil {
		log.Printf("[ramp-company-wallet] dormant: no chain registry available")
		return nil
	}
	// The same parser pricing uses, so the boot check and the live limit agree.
	maxUnits, err := services.ParseStableLimit(cfg.RampTreasuryMaxUSD, false)
	if err != nil {
		// The lane carries no caps (C2); this is the one per-order maximum, enforced
		// in pricing, and a typo must not silently remove or zero it.
		log.Printf("[ramp-company-wallet] ALERT ramp_company_wallet_cap_invalid_total=1 var=RAMP_TREASURY_MAX_USD value=%q — dormant", cfg.RampTreasuryMaxUSD)
		return nil
	}
	lanes, _ := buildTreasuryLanes(cfg, chains, key, treasuryLaneSpec{
		chains: cfg.RampTreasuryWalletChains, guardPrefix: "ramp_company_wallet_",
		logTag: "[ramp-company-wallet]", metric: "ramp_company_wallet", needRouters: false,
	})
	decimals := map[string]map[string]int{}
	verifiers := map[string]services.ERC20SourcedTransferVerifier{}
	probes := map[string]services.TreasuryTxProbe{}
	receiptRPCs := map[string]services.RawRPCCaller{}
	receiptsOK := map[string]bool{}
	for chain, lane := range lanes {
		client := chains.ClientForChain(chain)
		if client == nil || client.Client == nil {
			delete(lanes, chain)
			continue
		}
		d, dropped := services.CompanyWalletTokenDecimals(lane)
		for _, sym := range dropped {
			log.Printf("[ramp-company-wallet] ALERT ramp_company_wallet_token_dropped_total=1 chain=%s token=%s reason=decimals unreadable", chain, sym)
		}
		if len(d) == 0 {
			delete(lanes, chain)
			continue
		}
		decimals[chain] = d

		// 2.3 #8: a lane whose RPC cannot read receipts cannot prove a new
		// delivery, so it is not quoted — but it is kept, so an order already
		// open still settles and sends, and the re-probe job reopens it (I1).
		receiptRPCs[chain] = client.Client.Client()
		pctx, cancel := context.WithTimeout(context.Background(), companyWalletProbeTimeout)
		perr := services.ProbeReceipts(pctx, receiptRPCs[chain])
		cancel()
		receiptsOK[chain] = perr == nil
		if perr != nil {
			alertReceiptsUnreadable(chain, rpcHost(cfg, chain), perr)
		}
		verifiers[chain] = services.NewLogTransferVerifier(client.Client)
		probes[chain] = client.Client
	}
	if len(lanes) == 0 {
		log.Printf("[ramp-company-wallet] dormant: no usable chain in RAMP_TREASURY_WALLET_CHAINS=%v", cfg.RampTreasuryWalletChains)
		return nil
	}
	// No value caps on the lane (review C2): the maximum is enforced where
	// orders are priced, and free stock is the real per-token limit (§4.1).
	treasury := services.NewTreasury(lanes, services.CompanyWalletCaps(), nil)
	if treasury.Address() == "" {
		log.Printf("[ramp-company-wallet] dormant: lanes disagree on their signer")
		return nil
	}
	w := services.NewCompanyWallet(services.CompanyWalletConfig{
		Treasury: treasury, Decimals: decimals, Verifiers: verifiers, Probes: probes,
		ReceiptRPCs: receiptRPCs, ReceiptsOK: receiptsOK,
	})
	if w == nil {
		log.Printf("[ramp-company-wallet] dormant: no token offered on any lane")
		return nil
	}
	// Where the operator sends stock and gas (spec §4.1 boot line).
	log.Printf("[ramp-company-wallet] enabled address=%s chains=%v quotable=%v tokens=%v max_order=$%s",
		w.Address(), w.Chains(), w.QuotableChains(), decimals, services.FormatStableUnits(maxUnits.String()))
	return w
}

// rpcHost names a chain's RPC by host only — never the full URL, which can
// carry a key.
func rpcHost(cfg *config.Config, chain string) string {
	u, err := url.Parse(cfg.Networks["mainnet"].Chains[chain].RPCURL)
	if err != nil || u.Host == "" {
		return "unknown"
	}
	return u.Host
}

// alertReceiptsUnreadable logs the boot probe failure. A go-ethereum transport
// error is a *url.Error carrying the full RPC URL (API key in the path), so the
// text goes through the shared redactor.
func alertReceiptsUnreadable(chain, host string, err error) {
	log.Printf("[ramp-company-wallet] ALERT ramp_company_wallet_receipts_unreadable=1 chain=%s rpc_host=%s err=%v — not quoted until a re-probe passes",
		chain, host, services.RedactAlertText(err.Error()))
}

// wireTreasuryMarketRate builds the company wallet's rate source and hands it
// to the IT service for settlement. A nil Paycrest source falls back to the
// Flutterwave mid checked against the FX feed.
func wireTreasuryMarketRate(it *services.ITService, pc *services.PaycrestQuoteService, tolBps int64) *services.TreasuryMarketRate {
	rates := services.NewTreasuryMarketRate(pc, it, tolBps)
	if it != nil {
		it.SetTreasuryMarketRate(rates)
	}
	return rates
}
