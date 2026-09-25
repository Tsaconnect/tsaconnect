package middleware

import (
	"net"
	"net/http"
	"strings"
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

// NetlifyClientIP makes a request that arrived through the web app's signed
// Netlify proxy count, for c.ClientIP() and everything built on it (rate
// limits, logs), as coming from the visitor Netlify names. A missing or
// invalid signature leaves the request untouched. An empty secret disables it.
func NetlifyClientIP(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if secret != "" {
			if ip, ok := netlifyVisitor(c.Request, secret, time.Now()); ok {
				// Not a trusted proxy address, so gin takes it as the client as-is.
				c.Request.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			}
		}
		c.Next()
	}
}

func netlifyVisitor(r *http.Request, secret string, now time.Time) (net.IP, bool) {
	sig := r.Header.Get(NetlifySignHeader)
	if sig == "" {
		return nil, false
	}
	_, err := jwt.Parse(sig, func(*jwt.Token) (interface{}, error) { return []byte(secret), nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("netlify"),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil {
		return nil, false
	}
	ip := net.ParseIP(strings.TrimSpace(r.Header.Get(NetlifyClientIPHeader)))
	if ip == nil {
		return nil, false
	}
	return ip, true
}
