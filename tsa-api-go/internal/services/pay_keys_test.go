package services

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGeneratePayKey_ShapeAndHash(t *testing.T) {
	raw, prefix, hash, err := GeneratePayKey("live")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "tsapk_live_") || len(raw) != len("tsapk_live_")+43 {
		t.Fatalf("raw = %q", raw)
	}
	if prefix != raw[:len("tsapk_live_")+6] || hash != HashPayKey(raw) || len(hash) != 64 {
		t.Fatalf("prefix %q hash %q", prefix, hash)
	}
	if raw2, _, _, _ := GeneratePayKey("live"); raw2 == raw {
		t.Fatal("two keys collided")
	}
	if _, _, _, err := GeneratePayKey("prod"); err == nil {
		t.Fatal("an unknown mode minted a key")
	}
}

func TestPayKeyMode(t *testing.T) {
	for raw, want := range map[string]string{"tsapk_live_x": "live", "tsapk_test_x": "test"} {
		if got, ok := PayKeyMode(raw); !ok || got != want {
			t.Fatalf("%q -> %q %v", raw, got, ok)
		}
	}
	for _, raw := range []string{"", "tsapk_prod_x", "sk_live_x", "tsapk_live", "tsapk_live_", "tsapk_test_"} {
		if _, ok := PayKeyMode(raw); ok {
			t.Fatalf("%q accepted", raw)
		}
	}
}

func TestDerivePayWebhookSecret_PerMerchantModeAndVersion(t *testing.T) {
	master := []byte(strings.Repeat("k", 32))
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	a := DerivePayWebhookSecret(master, id, "live", 1)
	if a != DerivePayWebhookSecret(master, id, "live", 1) || !strings.HasPrefix(a, "whsec_") {
		t.Fatalf("secret = %q", a)
	}
	for name, other := range map[string]string{
		"test mode":      DerivePayWebhookSecret(master, id, "test", 1),
		"next version":   DerivePayWebhookSecret(master, id, "live", 2),
		"other merchant": DerivePayWebhookSecret(master, uuid.New(), "live", 1),
	} {
		if other == a {
			t.Fatalf("%s shares the live secret", name)
		}
	}
}

func TestSignPayWebhook_MatchesTheDocumentedScheme(t *testing.T) {
	body := []byte(`{"id":"e1"}`)
	got := SignPayWebhook("whsec_abc", 1_700_000_000, body)
	mac := hmac.New(sha256.New, []byte("whsec_abc"))
	mac.Write([]byte("1700000000." + string(body)))
	if want := "t=1700000000,v1=" + hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
