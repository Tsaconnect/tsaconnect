package blockchain

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// failoverCooldown is how long a client stays on a fallback endpoint before
	// it tries the primary again.
	failoverCooldown = 60 * time.Second
	// failoverAttemptCap bounds one attempt of a request that has no deadline
	// of its own, so a hung endpoint cannot keep the next from being asked.
	failoverAttemptCap = 20 * time.Second
)

// SplitRPCURLs reads an *_RPC_URL value: one URL, or a comma-separated
// failover list (primary first). Blanks are dropped.
func SplitRPCURLs(raw string) []string {
	var out []string
	for _, u := range strings.Split(raw, ",") {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// RPCHosts names an *_RPC_URL value by host only, comma-separated — never the
// full URL, whose path or query can carry an API key.
func RPCHosts(raw string) string {
	urls := SplitRPCURLs(raw)
	hosts := make([]string, 0, len(urls))
	for _, raw := range urls {
		hosts = append(hosts, hostOf(raw))
	}
	return strings.Join(hosts, ",")
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "unknown"
	}
	return u.Host
}

// failoverTransport sends each JSON-RPC request to one of several endpoints of
// the same chain. It is sticky: every request goes to the active endpoint.
// When that endpoint answers 429 or 5xx, or the request fails in transport,
// the SAME request (body replayed) is tried on the next endpoint, and the one
// that answers becomes active for failoverCooldown before the primary is tried
// again. A JSON-RPC error inside a 200, and any other 4xx, is the node's real
// answer and is returned as is.
//
// Every change of the active endpoint bumps Generation. Two answers from
// different nodes can disagree (a fallback may lag the primary by blocks), so
// a decision built from several reads compares the generation before and
// after and decides nothing when it moved.
type failoverTransport struct {
	chainID   int64
	endpoints []*url.URL
	base      http.RoundTripper
	now       func() time.Time
	logf      func(format string, args ...any)

	mu     sync.Mutex
	active int
	until  time.Time // when the active fallback's cooldown ends
	gen    uint64
}

func newFailoverTransport(chainID int64, rawURLs []string) (*failoverTransport, error) {
	if len(rawURLs) < 2 {
		return nil, fmt.Errorf("a failover list needs at least two RPC URLs")
	}
	t := &failoverTransport{chainID: chainID, base: http.DefaultTransport, now: time.Now, logf: log.Printf}
	for _, raw := range rawURLs {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("RPC URL list for chain %d: every entry must be an http(s) URL (host %s is not)", chainID, hostOf(raw))
		}
		t.endpoints = append(t.endpoints, u)
	}
	return t, nil
}

// Generation counts the endpoint changes so far.
func (t *failoverTransport) Generation() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gen
}

// start returns the endpoint a new request goes to, moving back to the
// primary once a fallback's cooldown is over.
func (t *failoverTransport) start() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active != 0 && !t.now().Before(t.until) {
		t.logf("[rpc] chain=%d endpoint %s -> %s (cooldown over, trying the primary again)",
			t.chainID, t.endpoints[t.active].Host, t.endpoints[0].Host)
		t.active = 0
		t.gen++
	}
	return t.active
}

// answered records that endpoint i answered a request first sent to from.
func (t *failoverTransport) answered(from, i int, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == i {
		return
	}
	t.logf("[rpc] chain=%d endpoint %s -> %s (%s)", t.chainID, t.endpoints[from].Host, t.endpoints[i].Host, reason)
	t.active = i
	t.until = t.now().Add(failoverCooldown)
	t.gen++
}

func (t *failoverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	// A broadcast that timed out or met a 5xx may have reached the node; sending
	// it elsewhere could turn the caller's "outcome unknown" into a wrong
	// "rejected" from a second node. Only a 429 — refused unprocessed — moves on.
	broadcast := bytes.Contains(body, []byte(`"eth_send`))

	first := t.start()
	n := len(t.endpoints)
	var (
		resp   *http.Response
		err    error
		reason string
	)
	for k := 0; k < n; k++ {
		i := (first + k) % n
		last := k == n-1
		resp, err = t.attempt(req, t.endpoints[i], body, last)
		switch {
		case err == nil && resp.StatusCode == http.StatusTooManyRequests:
			reason = "HTTP 429"
		case err == nil && resp.StatusCode >= 500 && !broadcast:
			reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
		case err != nil && !broadcast && req.Context().Err() == nil:
			reason = "transport error"
		default:
			if err == nil && k > 0 {
				t.answered(first, i, reason)
			}
			return resp, err
		}
		if last {
			return resp, err
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}
	}
	return resp, err // unreachable: the last attempt always returns
}

// attempt sends body to endpoint. Unless it is the last endpoint left, it gets
// half the caller's remaining time (or failoverAttemptCap without a deadline),
// so a hung endpoint leaves time to ask the next.
func (t *failoverTransport) attempt(req *http.Request, endpoint *url.URL, body []byte, last bool) (*http.Response, error) {
	ctx := req.Context()
	cancel := context.CancelFunc(func() {})
	if !last {
		budget := failoverAttemptCap
		if dl, ok := ctx.Deadline(); ok {
			budget = time.Until(dl) / 2
		}
		ctx, cancel = context.WithTimeout(ctx, budget)
	}
	out := req.Clone(ctx)
	u := *endpoint
	out.URL, out.Host = &u, ""
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	resp, err := t.base.RoundTrip(out)
	if err != nil {
		cancel()
		return nil, err
	}
	// The attempt's context must outlive the body the caller has yet to read.
	resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
