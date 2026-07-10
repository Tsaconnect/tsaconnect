package chains

import "testing"

func TestBalanceZeroValueIsRenderable(t *testing.T) {
	b := Balance{Symbol: "TRX", Raw: "0", Decimals: 6}
	if b.Symbol != "TRX" || b.Decimals != 6 {
		t.Fatalf("unexpected balance: %+v", b)
	}
}

func TestSigningPayloadCarriesIndex(t *testing.T) {
	p := SigningPayload{Index: 2, HashHex: "deadbeef", Curve: CurveSecp256k1}
	if p.Index != 2 || p.Curve != CurveSecp256k1 {
		t.Fatalf("unexpected payload: %+v", p)
	}
}
