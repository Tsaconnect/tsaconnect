package services

import (
	"context"
	"log"
)

// CompanyWalletFloats is the company wallet's reader for jobs.GasFloatMonitor
// (spec §5.3): the native balance and floor come from Treasury.FloatBalances
// itself, and free stock per token from TreasuryFreeUnits (which reads through
// laneTokenBalance) — no second balance-reading loop.
type CompanyWalletFloats struct{ it *ITService }

func (s *ITService) CompanyWalletFloats() *CompanyWalletFloats { return &CompanyWalletFloats{it: s} }

func (f *CompanyWalletFloats) Enabled() bool { return f.it.HasCompanyWallet() }

func (f *CompanyWalletFloats) FloatBalances(ctx context.Context) ([]SponsorFloat, error) {
	w := f.it.wallet
	floats, err := w.Treasury().FloatBalances(ctx)
	var out []SponsorFloat
	for _, fl := range floats {
		tokens := w.Tokens(fl.Chain)
		if len(tokens) == 0 {
			continue // a lane the wallet offers nothing on
		}
		fl.Tokens = map[string]float64{}
		for _, tok := range tokens {
			free, rerr := f.it.TreasuryFreeUnits(ctx, fl.Chain, tok)
			if rerr != nil {
				// An unreadable balance is not an empty one: no email on an RPC blip.
				log.Printf("[ramp-company-wallet] free stock %s %s: %s", fl.Chain, tok, RedactAlertText(rerr.Error()))
				continue
			}
			fl.Tokens[tok] = StableFromBaseUnits(free)
		}
		out = append(out, fl)
	}
	return out, err // the monitor redacts it when it logs
}
