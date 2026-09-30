package middleware

import (
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// The web app is served by Netlify, which proxies /api to this API. Without
// help the API would see Netlify's address as the client, and every per-IP
// limit would lump all web visitors into one budget that anyone could spend.
//
// Netlify signs each proxied request: x-nf-sign is an HS256 JWS (iss "netlify",
// short exp) made with the site's API_SIGNATURE_TOKEN, and the visitor's address
// travels in X-Nf-Client-Connection-Ip. The address is trusted ONLY when the
// signature verifies — anyone else can set that header.
const (
	NetlifySignHeader     = "X-Nf-Sign"
	NetlifyClientIPHeader = "X-Nf-Client-Connection-Ip"
)

// NetlifyProxiedKey is set (true) on the context of a request whose Netlify
// signature verified — it came in through the web app's origin.
const NetlifyProxiedKey = "netlifyProxied"

// netlifyUntrustedLogEvery bounds the "client address not trusted" log line: a
// broken signature fails every web request, and one line a minute is enough to
// be seen.
const netlifyUntrustedLogEvery = time.Minute

// NetlifyClientIP makes a request that arrived through the web app's signed
// Netlify proxy count, for c.ClientIP() and everything built on it (rate
// limits, logs), as coming from the visitor Netlify names. A missing or
// invalid signature leaves the request untouched, and is logged (at most once
// a minute) when the request names a visitor — otherwise a secret that no
// longer matches Netlify's would silently put every web visitor in one rate
// limit bucket. A verified signature also sets NetlifyProxiedKey. An empty
// secret disables it.
func NetlifyClientIP(secret string) gin.HandlerFunc {
	return netlifyClientIP(secret, time.Now)
}

func netlifyClientIP(secret string, now func() time.Time) gin.HandlerFunc {
	var lastLogged atomic.Int64 // unix nanos of the last untrusted-address line; 0 = never
	return func(c *gin.Context) {
		if secret != "" {
			at := now()
			ip, signed, why := netlifyVisitor(c.Request, secret, at)
			if signed {
				c.Set(NetlifyProxiedKey, true)
			}
			switch {
			case ip != nil:
				// Not a trusted proxy address, so gin takes it as the client as-is.
				c.Request.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			case len(c.Request.Header.Values(NetlifyClientIPHeader)) > 0 && logDue(&lastLogged, at, netlifyUntrustedLogEvery):
				log.Printf("[web] netlify_client_ip_untrusted=1 reason=%q: the visitor address was ignored and this request counts as the proxy's (logged at most once a minute)", why)
			}
		}
		c.Next()
	}
}

// netlifyVisitor is the visitor a correctly signed request names, or nil and
// why not; signed says whether the signature itself verified.
func netlifyVisitor(r *http.Request, secret string, now time.Time) (ip net.IP, signed bool, why string) {
	sig := r.Header.Get(NetlifySignHeader)
	if sig == "" {
		return nil, false, "signature missing"
	}
	_, err := jwt.Parse(sig, func(*jwt.Token) (interface{}, error) { return []byte(secret), nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("netlify"),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil {
		return nil, false, "signature invalid: " + err.Error()
	}
	// Exactly one: the signature does not cover the header, so a second copy is
	// someone else's addition, and which one a reader takes is not ours to guess.
	values := r.Header.Values(NetlifyClientIPHeader)
	if len(values) != 1 {
		return nil, true, "not exactly one visitor address"
	}
	if ip = net.ParseIP(strings.TrimSpace(values[0])); ip == nil {
		return nil, true, "visitor address unparseable"
	}
	return ip, true, ""
}

// logDue reports whether a line last logged at *last may be logged again at
// now, claiming the slot if so — lock-free, and exactly one of racing callers wins.
func logDue(last *atomic.Int64, now time.Time, every time.Duration) bool {
	prev := last.Load()
	if prev != 0 && now.Sub(time.Unix(0, prev)) < every {
		return false
	}
	return last.CompareAndSwap(prev, now.UnixNano())
}
