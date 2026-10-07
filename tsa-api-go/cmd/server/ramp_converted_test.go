package main

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// laneSigner is a lane's signer for address checks only; it never signs.
type laneSigner struct{ addr string }

func (s laneSigner) Address() string { return s.addr }
func (laneSigner) SignAndBroadcast(context.Context, []byte) (string, error) {
	panic("laneSigner never signs")
}

// testTreasury is a treasury with one lane per chain, each offering USDC at
// the given address.
func testTreasury(usdc map[string]string) *services.Treasury {
	lanes := map[string]*services.TreasuryLane{}
	for chain, addr := range usdc {
		lanes[chain] = &services.TreasuryLane{SponsorLane: &services.SponsorLane{
			ChainKey: chain, Signer: laneSigner{"0x00000000000000000000000000000000000000A1"},
			Tokens: map[string]string{"USDC": addr},
		}}
	}
	return services.NewTreasury(lanes, services.CompanyWalletCaps(), nil)
}

func keyAddress(t *testing.T, hexKey string) string {
	t.Helper()
	k, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		t.Fatal(err)
	}
	return crypto.PubkeyToAddress(k.PublicKey).Hex()
}

func TestConversionTreasuryAddress_DerivedFromTheKeyAlone(t *testing.T) {
	want := keyAddress(t, testKeyA)
	for _, raw := range []string{testKeyA, "0x" + testKeyA, "  0x" + testKeyA + " "} {
		// RAMP_CONVERSION_BUY off: the address is still known (Plan C6).
		cfg := &config.Config{RampTreasuryPrivateKey: raw, RampConversionBuyEnabled: false}
		if got := conversionTreasuryAddress(cfg); got != want {
			t.Fatalf("%q: address = %s, want %s", raw, got, want)
		}
	}
	if conversionTreasuryAddress(&config.Config{}) != "" || conversionTreasuryAddress(&config.Config{RampTreasuryPrivateKey: "zz"}) != "" {
		t.Fatal("no key or a bad key derives nothing")
	}
}

// The company wallet is built knowing the conversion treasury, so a sale to
// it without a record is refused (Task 3's guard) whatever the switches say.
func TestNewCompanyWallet_KnowsTheConversionTreasury(t *testing.T) {
	treasury := testTreasury(map[string]string{"base": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"})
	cw := services.CompanyWalletConfig{Treasury: treasury, Decimals: map[string]map[string]int{"base": {"USDC": 6}}}

	cfg := &config.Config{RampTreasuryPrivateKey: testKeyB}
	w := newCompanyWallet(cfg, cw)
	if w == nil || !w.IsConversionTreasury(keyAddress(t, testKeyB)) {
		t.Fatal("the wallet does not know the conversion treasury")
	}
	if w.IsConversionTreasury(keyAddress(t, testKeyA)) {
		t.Fatal("another address taken for the conversion treasury")
	}
	if w := newCompanyWallet(&config.Config{}, cw); w == nil || w.IsConversionTreasury(keyAddress(t, testKeyB)) {
		t.Fatal("no conversion key: no conversion treasury")
	}
}

func TestConvertedPricerFor_NilUnlessConversionIsBuilt(t *testing.T) {
	treasury := testTreasury(map[string]string{"base": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"})
	quotes := &services.PaycrestQuoteService{}
	if p := convertedPricerFor(&config.Config{RampConversionBuyEnabled: false}, treasury, quotes, "route1"); p != nil {
		t.Fatal("conversion off: no pricer")
	}
	on := &config.Config{RampConversionBuyEnabled: true}
	if p := convertedPricerFor(on, nil, quotes, "route1"); p != nil {
		t.Fatal("no conversion treasury: no pricer")
	}
	// A typed-nil quote service must come back as a true nil interface.
	var none *services.PaycrestQuoteService
	if p := convertedPricerFor(on, treasury, none, "route1"); p != nil {
		t.Fatal("no quote service: no pricer")
	}
	if p := convertedPricerFor(on, treasury, quotes, "route1"); p == nil {
		t.Fatal("conversion built: the quote service prices converted legs")
	}
}

// Route 1 follows the company wallet's switch: on only when the provider is
// registered, RAMP_CONVERSION_BUY is on and a conversion-treasury lane sits on
// one of the wallet's chains.
func TestCompanyWalletConverts(t *testing.T) {
	baseLane := testTreasury(map[string]string{"base": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"})
	on := &config.Config{RampConversionBuyEnabled: true}
	for _, tc := range []struct {
		name       string
		cfg        *config.Config
		registered bool
		chains     []string
		treasury   *services.Treasury
		want       bool
	}{
		{"all prerequisites", on, true, []string{"base"}, baseLane, true},
		{"lane on one of several chains", on, true, []string{"arbitrum", "base"}, baseLane, true},
		{"provider not registered", on, false, []string{"base"}, baseLane, false},
		{"RAMP_CONVERSION_BUY off", &config.Config{}, true, []string{"base"}, baseLane, false},
		{"no conversion treasury", on, true, []string{"base"}, nil, false},
		{"no lane on the wallet's chain", on, true, []string{"bsc"}, baseLane, false},
	} {
		got, why := companyWalletConverts(tc.cfg, tc.registered, tc.chains, tc.treasury)
		if got != tc.want || (got != (why == "")) {
			t.Fatalf("%s: route 1 = %v (%q), want %v", tc.name, got, why, tc.want)
		}
	}
}

// Route 2 follows Flutterwave's switch, and stays off — never fatal — unless
// the network bundle's Flutterwave Polygon USDC is the Polygon lane's USDC.
func TestFlutterwaveConverts(t *testing.T) {
	const polygonUSDC = "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"
	polygon := testTreasury(map[string]string{"polygon": polygonUSDC})
	withFW := func(usdc string, conversion bool) *config.Config {
		return &config.Config{RampConversionBuyEnabled: conversion,
			Networks: map[string]config.NetworkConfig{"mainnet": {FlutterwavePolygonUSDC: usdc}}}
	}
	for _, tc := range []struct {
		name       string
		cfg        *config.Config
		registered bool
		treasury   *services.Treasury
		want       bool
	}{
		{"match", withFW(polygonUSDC, true), true, polygon, true},
		{"match, other case", withFW(" 0x3C499C542CEF5E3811E1192CE70D8CC03D5C3359 ", true), true, polygon, true},
		{"provider not registered", withFW(polygonUSDC, true), false, polygon, false},
		{"RAMP_CONVERSION_BUY off", withFW(polygonUSDC, false), true, polygon, false},
		{"no conversion treasury", withFW(polygonUSDC, true), true, nil, false},
		{"no polygon lane", withFW(polygonUSDC, true), true, testTreasury(map[string]string{"base": polygonUSDC}), false},
		{"no Flutterwave USDC on the network", withFW("", true), true, polygon, false},
		{"Flutterwave sends USDC.e, lane holds native USDC", withFW("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174", true), true, polygon, false},
	} {
		got, why := flutterwaveConverts(tc.cfg, tc.registered, tc.treasury)
		if got != tc.want || (got != (why == "")) {
			t.Fatalf("%s: route 2 = %v (%q), want %v", tc.name, got, why, tc.want)
		}
	}
}
