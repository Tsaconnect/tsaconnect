package models

import "testing"

func TestITIntentAsset_DefaultsToMCGPForHistoricalRows(t *testing.T) {
	// Rows written before the asset column existed carry "". They are MCGP
	// intents and must read as such — exactly as ITIntentCurrency treats "".
	i := &ITIntent{}
	if got := ITIntentAsset(i); got != ITAssetMCGP {
		t.Fatalf("empty asset = %q, want %q", got, ITAssetMCGP)
	}
}

func TestITIntentAsset_RoundTripsExplicitValues(t *testing.T) {
	for _, want := range []string{ITAssetMCGP, ITAssetUSDC, ITAssetUSDT} {
		if got := ITIntentAsset(&ITIntent{Asset: want}); got != want {
			t.Fatalf("asset %q read back as %q", want, got)
		}
	}
}

func TestBeforeSave_MirrorsMcgpAmountIntoAssetAmountForMCGP(t *testing.T) {
	// AssetAmount is the general column; MCGP intents keep McgpAmount
	// authoritative and mirror it, the same dual-write idiom the kobo/minor
	// columns already use.
	i := &ITIntent{Asset: ITAssetMCGP, McgpAmount: "12345", NgnKobo: "100"}
	if err := i.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave: %v", err)
	}
	if i.AssetAmount != "12345" {
		t.Fatalf("AssetAmount = %q, want %q", i.AssetAmount, "12345")
	}
}

func TestBeforeSave_LeavesAssetAmountAloneForStablecoins(t *testing.T) {
	// A stablecoin intent has no McgpAmount; mirroring would zero its amount.
	i := &ITIntent{Asset: ITAssetUSDC, AssetAmount: "5000000", NgnKobo: "100"}
	if err := i.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave: %v", err)
	}
	if i.AssetAmount != "5000000" {
		t.Fatalf("AssetAmount = %q, want it untouched", i.AssetAmount)
	}
}
