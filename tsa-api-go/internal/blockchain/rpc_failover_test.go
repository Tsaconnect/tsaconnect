package blockchain

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// rpcStub is one RPC endpoint: it answers with status (and body) and records
// every request body it saw.
type rpcStub struct {
	srv *httptest.Server

	mu     sync.Mutex
	status int
	body   string
	bodies []string
}

func newRPCStub(t *testing.T, status int, body string) *rpcStub {
	t.Helper()
	s := &rpcStub{status: status, body: body}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(b))
		status, body := s.status, s.body
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *rpcStub) set(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body = status, body
}

func (s *rpcStub) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

const (
	blockNumberOK  = `{"jsonrpc":"2.0","id":1,"result":"0x10"}`
	rangeTooLarge  = `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"range too large"}}`
	blockNumberReq = `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`
	sendRawReq     = `{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":["0x02f8"]}`
)

// testTransport is a failover transport over urls with a settable clock and a
// captured log.
type testTransport struct {
	*failoverTransport
	clock time.Time
	logs  []string
}

func newTestTransport(t *testing.T, urls ...string) *testTransport {
	t.Helper()
	ft, err := newFailoverTransport(8453, urls)
	if err != nil {
		t.Fatalf("newFailoverTransport: %v", err)
	}
	tt := &testTransport{failoverTransport: ft, clock: time.Unix(1_700_000_000, 0)}
	ft.now = func() time.Time { return tt.clock }
	ft.logf = func(format string, args ...any) { tt.logs = append(tt.logs, fmt.Sprintf(format, args...)) }
	return tt
}

func (tt *testTransport) post(t *testing.T, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, tt.endpoints[0].String(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestFailover_On429RetriesTheSameRequestOnTheNextEndpoint(t *testing.T) {
	primary := newRPCStub(t, http.StatusTooManyRequests, "Too Many Requests")
	fallback := newRPCStub(t, http.StatusOK, blockNumberOK)
	tt := newTestTransport(t, primary.srv.URL, fallback.srv.URL)

	status, body := tt.post(t, blockNumberReq)
	if status != http.StatusOK || body != blockNumberOK {
		t.Fatalf("answer = %d %q, want the fallback's", status, body)
	}
	if got := fallback.seen(); len(got) != 1 || got[0] != blockNumberReq {
		t.Fatalf("fallback saw %q, want the same request body replayed", got)
	}
	if tt.Generation() != 1 {
		t.Fatalf("generation = %d, want 1 after one switch", tt.Generation())
	}
}

func TestFailover_On5xxRetriesOnTheNextEndpoint(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			primary := newRPCStub(t, code, "upstream down")
			fallback := newRPCStub(t, http.StatusOK, blockNumberOK)
			tt := newTestTransport(t, primary.srv.URL, fallback.srv.URL)
			if status, body := tt.post(t, blockNumberReq); status != http.StatusOK || body != blockNumberOK {
				t.Fatalf("answer = %d %q, want the fallback's", status, body)
			}
		})
	}
}

func TestFailover_OnTransportErrorRetriesOnTheNextEndpoint(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	fallback := newRPCStub(t, http.StatusOK, blockNumberOK)
	tt := newTestTransport(t, deadURL, fallback.srv.URL)
	if status, body := tt.post(t, blockNumberReq); status != http.StatusOK || body != blockNumberOK {
		t.Fatalf("answer = %d %q, want the fallback's", status, body)
	}
	if len(fallback.seen()) != 1 {
		t.Fatalf("fallback saw %d requests", len(fallback.seen()))
	}
}

// A hung endpoint gets half the caller's remaining time, so the next one is
// still asked inside the caller's deadline.
func TestFailover_AHungEndpointLeavesTimeToAskTheNext(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); hung.Close() })
	fallback := newRPCStub(t, http.StatusOK, blockNumberOK)
	tt := newTestTransport(t, hung.URL, fallback.srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, hung.URL, strings.NewReader(blockNumberReq))
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); string(b) != blockNumberOK {
		t.Fatalf("answer = %q, want the fallback's", b)
	}
}

// A JSON-RPC error inside a 200 is the node's real answer ("range too large"
// is handled by the log scanner): it is returned as is, never retried.
func TestFailover_NeverOnAJSONRPCErrorInA200(t *testing.T) {
	primary := newRPCStub(t, http.StatusOK, rangeTooLarge)
	fallback := newRPCStub(t, http.StatusOK, blockNumberOK)
	tt := newTestTransport(t, primary.srv.URL, fallback.srv.URL)
	if status, body := tt.post(t, blockNumberReq); status != http.StatusOK || body != rangeTooLarge {
		t.Fatalf("answer = %d %q, want the primary's own error", status, body)
	}
	if len(fallback.seen()) != 0 || tt.Generation() != 0 {
		t.Fatalf("fell over on a JSON-RPC error: fallback saw %d, generation %d", len(fallback.seen()), tt.Generation())
	}
}

func TestFailover_NeverOnA4xxOtherThan429(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			primary := newRPCStub(t, code, "no")
			fallback := newRPCStub(t, http.StatusOK, blockNumberOK)
			tt := newTestTransport(t, primary.srv.URL, fallback.srv.URL)
			if status, _ := tt.post(t, blockNumberReq); status != code {
				t.Fatalf("status = %d, want the primary's %d", status, code)
			}
			if len(fallback.seen()) != 0 {
				t.Fatalf("fell over on %d", code)
			}
		})
	}
}

func TestFailover_StaysOnTheFallbackForTheCooldownThenTriesThePrimary(t *testing.T) {
	primary := newRPCStub(t, http.StatusTooManyRequests, "slow down")
	fallback := newRPCStub(t, http.StatusOK, blockNumberOK)
	tt := newTestTransport(t, primary.srv.URL, fallback.srv.URL)

	tt.post(t, blockNumberReq) // primary 429 → fallback
	primary.set(http.StatusOK, blockNumberOK)
	tt.clock = tt.clock.Add(failoverCooldown - time.Second)
	tt.post(t, blockNumberReq)
	if n := len(primary.seen()); n != 1 {
		t.Fatalf("primary asked %d times inside the cooldown, want only the first", n)
	}
	if n := len(fallback.seen()); n != 2 {
		t.Fatalf("fallback asked %d times, want sticky", n)
	}
	gen := tt.Generation()

	tt.clock = tt.clock.Add(2 * time.Second) // cooldown over
	tt.post(t, blockNumberReq)
	if n := len(primary.seen()); n != 2 {
		t.Fatalf("primary asked %d times after the cooldown, want it tried again", n)
	}
	if tt.Generation() != gen+1 {
		t.Fatalf("generation = %d, want %d: going back to the primary is an endpoint change", tt.Generation(), gen+1)
	}
	tt.post(t, blockNumberReq)
	if n := len(fallback.seen()); n != 2 {
		t.Fatalf("fallback asked %d times once the primary answered again", n)
	}
}

// Every switch bumps the generation, including one away from a fallback that
// fails in turn; a request that stays put never does.
func TestFailover_GenerationCountsEveryEndpointChange(t *testing.T) {
	a := newRPCStub(t, http.StatusOK, blockNumberOK)
	b := newRPCStub(t, http.StatusOK, blockNumberOK)
	c := newRPCStub(t, http.StatusOK, blockNumberOK)
	tt := newTestTransport(t, a.srv.URL, b.srv.URL, c.srv.URL)
	tt.post(t, blockNumberReq)
	if tt.Generation() != 0 {
		t.Fatalf("generation moved without a switch: %d", tt.Generation())
	}
	a.set(http.StatusTooManyRequests, "")
	tt.post(t, blockNumberReq) // a → b
	b.set(http.StatusServiceUnavailable, "")
	tt.post(t, blockNumberReq) // b → c
	if tt.Generation() != 2 || len(c.seen()) != 1 {
		t.Fatalf("generation = %d, c saw %d; want 2 switches ending on c", tt.Generation(), len(c.seen()))
	}
}

func TestFailover_EveryEndpointFailingReturnsTheFailureAndStaysPut(t *testing.T) {
	a := newRPCStub(t, http.StatusTooManyRequests, "a busy")
	b := newRPCStub(t, http.StatusTooManyRequests, "b busy")
	tt := newTestTransport(t, a.srv.URL, b.srv.URL)
	if status, _ := tt.post(t, blockNumberReq); status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the 429", status)
	}
	if tt.Generation() != 0 {
		t.Fatalf("generation = %d: nothing answered, so nothing switched", tt.Generation())
	}
}

// A broadcast that may have reached a node is never replayed elsewhere on a
// timeout or 5xx: the caller's "uncertain" path must decide. A 429 was refused
// before it was processed, so it moves on.
func TestFailover_ABroadcastMovesOnOnlyAfterA429(t *testing.T) {
	primary := newRPCStub(t, http.StatusBadGateway, "gateway")
	fallback := newRPCStub(t, http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":"0xabc"}`)
	tt := newTestTransport(t, primary.srv.URL, fallback.srv.URL)
	if status, _ := tt.post(t, sendRawReq); status != http.StatusBadGateway || len(fallback.seen()) != 0 {
		t.Fatalf("a 502 broadcast was replayed: status %d, fallback saw %d", status, len(fallback.seen()))
	}
	primary.set(http.StatusTooManyRequests, "")
	if status, _ := tt.post(t, sendRawReq); status != http.StatusOK || len(fallback.seen()) != 1 {
		t.Fatalf("a 429 broadcast was not moved on: status %d, fallback saw %d", status, len(fallback.seen()))
	}
}

// URLs carry API keys in their paths: a switch names hosts only.
func TestFailover_LogsTheSwitchByHostOnly(t *testing.T) {
	primary := newRPCStub(t, http.StatusTooManyRequests, "")
	fallback := newRPCStub(t, http.StatusOK, blockNumberOK)
	tt := newTestTransport(t, primary.srv.URL+"/v2/sekret-key-1", fallback.srv.URL+"/v2/sekret-key-2?apikey=sekret3")
	tt.post(t, blockNumberReq)
	if len(tt.logs) != 1 {
		t.Fatalf("logs = %q, want one switch line", tt.logs)
	}
	line := tt.logs[0]
	if strings.Contains(line, "sekret") || strings.Contains(line, "/v2") {
		t.Fatalf("switch log leaks the URL: %q", line)
	}
	if !strings.Contains(line, tt.endpoints[0].Host) || !strings.Contains(line, tt.endpoints[1].Host) || !strings.Contains(line, "429") {
		t.Fatalf("switch log = %q, want both hosts and the reason", line)
	}
}

func TestSplitRPCURLs(t *testing.T) {
	got := SplitRPCURLs(" https://a.example/rpc , ,https://b.example ")
	if len(got) != 2 || got[0] != "https://a.example/rpc" || got[1] != "https://b.example" {
		t.Fatalf("SplitRPCURLs = %q", got)
	}
	if hosts := RPCHosts("https://a.example/v2/key,https://b.example:8545/x?k=1"); hosts != "a.example,b.example:8545" {
		t.Fatalf("RPCHosts = %q", hosts)
	}
}

// End to end: an ethclient dialled over a list reads through a rate-limited
// primary, and its generation is readable from the client.
func TestNewEVMClient_AListFailsOverOnARateLimit(t *testing.T) {
	primary := newRPCStub(t, http.StatusTooManyRequests, "Too Many Requests")
	fallback := newRPCStub(t, http.StatusOK, blockNumberOK)
	c, err := NewEVMClient(primary.srv.URL+","+fallback.srv.URL, 8453)
	if err != nil {
		t.Fatal(err)
	}
	n, err := c.Client.BlockNumber(context.Background())
	if err != nil || n != 16 {
		t.Fatalf("BlockNumber = %d %v, want 16 from the fallback", n, err)
	}
	if c.EndpointGeneration() != 1 || c.Probe().EndpointGeneration() != 1 {
		t.Fatalf("generation = %d, want 1", c.EndpointGeneration())
	}
}

func TestNewEVMClient_OneURLHasNoFailover(t *testing.T) {
	stub := newRPCStub(t, http.StatusOK, blockNumberOK)
	c, err := NewEVMClient(stub.srv.URL, 8453)
	if err != nil {
		t.Fatal(err)
	}
	if c.failover != nil || c.EndpointGeneration() != 0 {
		t.Fatal("a single URL must keep today's direct dial")
	}
}

func TestNewEVMClient_AListMustBeHTTP(t *testing.T) {
	if _, err := NewEVMClient("wss://a.example/ws,https://b.example", 8453); err == nil {
		t.Fatal("a websocket URL in a list was accepted")
	}
}
