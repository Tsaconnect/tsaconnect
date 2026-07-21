package services

import (
	"context"
	"errors"
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
	c       *FlutterwaveClient
	country string // ISO-2, "NG" today
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
	banks, err := d.c.ListBanks(ctx, d.country)
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
	name, err := d.c.ResolveAccount(ctx, "NGN", bankCode, accountNumber)
	if err != nil {
		// Map the FW sentinel onto the Nomba one the handler layer keys 404s on.
		if errors.Is(err, ErrFWAccountNotFound) {
			return NombaLookupResult{}, ErrNombaAccountNotFound
		}
		return NombaLookupResult{}, err
	}
	return NombaLookupResult{AccountNumber: accountNumber, AccountName: name}, nil
}
