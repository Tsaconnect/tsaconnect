package services

import (
	"context"
	"errors"
	"strings"
)

// BankDirectory is the app-facing seam for the bank dropdown + NUBAN name
// enquiry (GET /p2p/banks, GET /p2p/banks/resolve). Both providers keep the
// exact response contract the app's ngBanks.ts depends on: banks as
// {code,name} (NIBSS codes) and resolve as {accountNumber,accountName}.
type BankDirectory interface {
	ListBanks(ctx context.Context) ([]NombaBank, error)
	Resolve(ctx context.Context, bankCode, accountNumber string) (NombaLookupResult, error)
}

// ─── Nomba adapter ─────────────────────────────────────────────────────────

type nombaBankDirectory struct{ c *NombaClient }

// NewNombaBankDirectory wraps a NombaClient; returns nil when the client is
// disabled so callers can gate on `!= nil` without interface-nil surprises.
func NewNombaBankDirectory(c *NombaClient) BankDirectory {
	if !c.IsEnabled() {
		return nil
	}
	return nombaBankDirectory{c: c}
}

func (d nombaBankDirectory) ListBanks(ctx context.Context) ([]NombaBank, error) {
	return d.c.ListBanks(ctx)
}

func (d nombaBankDirectory) Resolve(ctx context.Context, bankCode, accountNumber string) (NombaLookupResult, error) {
	return d.c.Lookup(ctx, bankCode, accountNumber)
}

// ─── Flutterwave adapter ───────────────────────────────────────────────────

type flutterwaveBankDirectory struct {
	c *FlutterwaveClient
	// country is the DEFAULT ISO-2 used when a caller names no country. The
	// per-country lookup goes through ListBanksIn, because a payout to a
	// Ghanaian account needs Ghanaian bank codes and a directory pinned at
	// construction can only ever serve one market.
	country string
}

// NewFlutterwaveBankDirectory wraps a FlutterwaveClient for the given country;
// returns nil when the client is disabled.
func NewFlutterwaveBankDirectory(c *FlutterwaveClient, country string) BankDirectory {
	if !c.IsEnabled() {
		return nil
	}
	if country == "" {
		country = "NG"
	}
	return flutterwaveBankDirectory{c: c, country: country}
}

func (d flutterwaveBankDirectory) ListBanks(ctx context.Context) ([]NombaBank, error) {
	return d.ListBanksIn(ctx, d.country)
}

// ListBanksIn returns the bank directory for a specific country, falling back
// to the configured default when country is empty.
func (d flutterwaveBankDirectory) ListBanksIn(ctx context.Context, country string) ([]NombaBank, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		country = d.country
	}
	banks, err := d.c.ListBanks(ctx, country)
	if err != nil {
		return nil, err
	}
	out := make([]NombaBank, 0, len(banks))
	for _, b := range banks {
		out = append(out, NombaBank{Code: b.Code, Name: b.Name})
	}
	return out, nil
}

func (d flutterwaveBankDirectory) Resolve(ctx context.Context, bankCode, accountNumber string) (NombaLookupResult, error) {
	return d.ResolveIn(ctx, "NGN", bankCode, accountNumber)
}

// ResolveIn verifies an account against a specific currency's rail. Account
// resolution is currency-scoped at the provider: a Ghanaian account number
// cannot be verified against the naira rail.
func (d flutterwaveBankDirectory) ResolveIn(ctx context.Context, currency, bankCode, accountNumber string) (NombaLookupResult, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "NGN"
	}
	name, err := d.c.ResolveAccount(ctx, currency, bankCode, accountNumber)
	if err != nil {
		// Map the FW sentinel onto the Nomba one the handler layer keys 404s on.
		if errors.Is(err, ErrFWAccountNotFound) {
			return NombaLookupResult{}, ErrNombaAccountNotFound
		}
		return NombaLookupResult{}, err
	}
	return NombaLookupResult{AccountNumber: accountNumber, AccountName: name}, nil
}
