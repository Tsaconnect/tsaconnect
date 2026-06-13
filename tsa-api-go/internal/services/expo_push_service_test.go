package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/events"
)

func TestExpoPushService_SendPrunesDeadTokens(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		// Tickets in request order: tokA ok, tokB dead, tokC ok.
		_, _ = io.WriteString(w, `{"data":[{"status":"ok"},{"status":"error","message":"not registered","details":{"error":"DeviceNotRegistered"}},{"status":"ok"}]}`)
	}))
	defer srv.Close()

	svc := NewExpoPushService("")
	svc.url = srv.URL

	dead := svc.Send(
		[]string{"tokA", "tokB", "tokC"},
		"Trade paid",
		"@buyer marked paid",
		map[string]interface{}{"type": "p2p.trade.paid", "tradeId": "t1"},
	)

	if len(dead) != 1 || dead[0] != "tokB" {
		t.Fatalf("dead = %v, want [tokB]", dead)
	}

	var msgs []map[string]interface{}
	if err := json.Unmarshal(gotBody, &msgs); err != nil {
		t.Fatalf("request body not a JSON array: %v (%s)", err, gotBody)
	}
	if len(msgs) != 3 {
		t.Fatalf("sent %d messages, want 3", len(msgs))
	}
	if msgs[0]["to"] != "tokA" || msgs[0]["title"] != "Trade paid" {
		t.Errorf("msg0 = %v, want to=tokA title=Trade paid", msgs[0])
	}
	// The event type must ride in `data` so the app can deep-link on tap.
	if !strings.Contains(string(gotBody), "p2p.trade.paid") {
		t.Errorf("request body missing data.type: %s", gotBody)
	}
}

func TestExpoPushService_NoopOnEmptyOrNil(t *testing.T) {
	if got := NewExpoPushService("").Send(nil, "t", "b", nil); got != nil {
		t.Errorf("Send(nil tokens) = %v, want nil", got)
	}
	var nilSvc *ExpoPushService
	if got := nilSvc.Send([]string{"x"}, "t", "b", nil); got != nil {
		t.Errorf("nil-receiver Send = %v, want nil", got)
	}
}

func TestPushWorthy_Curation(t *testing.T) {
	push := []string{
		events.P2PTradePaid, events.P2PTradeReleased, "p2p.message.new",
		events.OrderPlaced, events.SecurityAccountLocked, events.P2PDepositConfirmed,
		events.MerchantApproved, events.TransactionReceived,
	}
	for _, e := range push {
		if !pushWorthy(e) {
			t.Errorf("pushWorthy(%q) = false, want true", e)
		}
	}
	skip := []string{
		events.P2PAdCreated, events.ProductListed, events.TransactionPending,
		events.VerificationSubmitted, events.MerchantSubmitted,
	}
	for _, e := range skip {
		if pushWorthy(e) {
			t.Errorf("pushWorthy(%q) = true, want false (self/noise)", e)
		}
	}
}
