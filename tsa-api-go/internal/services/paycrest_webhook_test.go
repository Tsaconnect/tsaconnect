package services

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func paycrestSign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestPaycrestVerifier_AcceptsAValidSignature(t *testing.T) {
	const secret = "s3cr3t"
	body := []byte(`{"event":"payment_order.settled","data":{"id":"abc"}}`)
	v := NewPaycrestWebhookVerifier(secret)

	if !v.Verify(body, paycrestSign(body, secret)) {
		t.Fatal("a correctly signed body was rejected")
	}
	// Providers are inconsistent about hex case; both must verify.
	if !v.Verify(body, strings.ToUpper(paycrestSign(body, secret))) {
		t.Error("uppercase hex should verify")
	}
	if !v.Verify(body, " "+paycrestSign(body, secret)+" ") {
		t.Error("surrounding whitespace should not break verification")
	}
}

func TestPaycrestVerifier_RejectsTampering(t *testing.T) {
	const secret = "s3cr3t"
	body := []byte(`{"event":"payment_order.settled","data":{"id":"abc"}}`)
	sig := paycrestSign(body, secret)
	v := NewPaycrestWebhookVerifier(secret)

	cases := map[string]struct {
		body []byte
		sig  string
	}{
		"body changed by one byte": {[]byte(`{"event":"payment_order.settled","data":{"id":"abd"}}`), sig},
		"signed with another key":  {body, paycrestSign(body, "wrong")},
		"empty signature":          {body, ""},
		"not hex at all":           {body, "not-a-signature"},
		"truncated digest":         {body, sig[:len(sig)-2]},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if v.Verify(tc.body, tc.sig) {
				t.Fatal("verifier accepted an invalid signature")
			}
		})
	}
}

// Flutterwave's verifier is Base64 over the same algorithm. Mixing the two
// silently rejects every event, which is a very quiet way to lose money
// movement, so pin the encoding.
func TestPaycrestVerifier_RejectsBase64Encoding(t *testing.T) {
	const secret = "s3cr3t"
	body := []byte(`{"event":"payment_order.settled","data":{"id":"abc"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	b64 := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if NewPaycrestWebhookVerifier(secret).Verify(body, b64) {
		t.Fatal("Base64 accepted — this verifier must be hex only")
	}
}

// No secret must mean no acceptance, not blanket acceptance.
func TestPaycrestVerifier_FailsClosedWithoutASecret(t *testing.T) {
	if v := NewPaycrestWebhookVerifier("  "); v != nil {
		t.Fatal("a blank secret should yield no verifier")
	}
	var nilV *PaycrestWebhookVerifier
	if nilV.Verify([]byte(`{}`), "anything") {
		t.Fatal("a nil verifier must reject everything")
	}
}

func TestParsePaycrestWebhook_ExtractsIdentifiers(t *testing.T) {
	raw := []byte(`{
	  "event":"payment_order.settled",
	  "direction":"onramp",
	  "webhookVersion":"2",
	  "data":{"id":"3365f79c","reference":"PCB-abc-123","status":"settled"}
	}`)
	ev, err := ParsePaycrestWebhook(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Event != PaycrestEventSettled || ev.OrderID != "3365f79c" ||
		ev.Reference != "PCB-abc-123" || ev.Direction != "onramp" {
		t.Fatalf("parsed = %#v", ev)
	}
	// Paycrest sends no delivery id, so redelivering the SAME transition must
	// dedupe while a genuine later transition still lands.
	if ev.DedupeKey != "3365f79c:payment_order.settled" {
		t.Errorf("dedupe key = %q", ev.DedupeKey)
	}
}

func TestParsePaycrestWebhook_DedupeKeyDistinguishesTransitions(t *testing.T) {
	mk := func(event string) string {
		ev, err := ParsePaycrestWebhook([]byte(
			`{"event":"` + event + `","data":{"id":"same-order","reference":"r"}}`))
		if err != nil {
			t.Fatalf("parse %s: %v", event, err)
		}
		return ev.DedupeKey
	}
	if mk(PaycrestEventPending) == mk(PaycrestEventSettled) {
		t.Fatal("two different transitions on one order collided — the second would be dropped as a duplicate")
	}
}

func TestParsePaycrestWebhook_Rejects(t *testing.T) {
	cases := map[string]string{
		"not json":       `{`,
		"no event":       `{"data":{"id":"x"}}`,
		"no identifier":  `{"event":"payment_order.settled","data":{"status":"settled"}}`,
		"no data at all": `{"event":"payment_order.settled"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePaycrestWebhook([]byte(raw)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// An unconsumed type is well-formed and simply not ours. It must be
// distinguishable so the handler acknowledges instead of earning retries.
func TestParsePaycrestWebhook_UnknownEventIsItsOwnSentinel(t *testing.T) {
	_, err := ParsePaycrestWebhook([]byte(
		`{"event":"payment_order.something_new","data":{"id":"x","reference":"r"}}`))
	if !errors.Is(err, ErrPaycrestUnknownEvent) {
		t.Fatalf("want ErrPaycrestUnknownEvent, got %v", err)
	}
}

func TestParsePaycrestWebhook_AcceptsEveryDocumentedEvent(t *testing.T) {
	for _, e := range []string{
		PaycrestEventPending, PaycrestEventDeposited, PaycrestEventValidated,
		PaycrestEventSettled, PaycrestEventRefunded, PaycrestEventExpired,
	} {
		if _, err := ParsePaycrestWebhook([]byte(
			`{"event":"` + e + `","data":{"id":"x","reference":"r"}}`)); err != nil {
			t.Errorf("%s should be consumed: %v", e, err)
		}
	}
}

// Reference alone is enough: it exists on our row from creation, whereas their
// id only arrives with the response, so an event racing our persist still
// correlates.
func TestParsePaycrestWebhook_ReferenceAloneIsEnough(t *testing.T) {
	ev, err := ParsePaycrestWebhook([]byte(
		`{"event":"payment_order.expired","data":{"reference":"PCB-only"}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Reference != "PCB-only" || ev.OrderID != "" {
		t.Fatalf("parsed = %#v", ev)
	}
	if ev.DedupeKey != "PCB-only:payment_order.expired" {
		t.Errorf("dedupe key should fall back to the reference, got %q", ev.DedupeKey)
	}
}
