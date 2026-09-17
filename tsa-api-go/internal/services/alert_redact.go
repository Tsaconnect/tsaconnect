package services

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// urlQueryRE matches a URL query string so it can be stripped wholesale —
// transport errors (*url.Error) embed the ENTIRE request URL, credentials
// included, and allowlisting individual secrets is a losing game.
var urlQueryRE = regexp.MustCompile(`\?[^\s"]*`)

// RedactURLQueries replaces every query string in s with a marker.
func RedactURLQueries(s string) string {
	return urlQueryRE.ReplaceAllString(s, "?[params-redacted]")
}

const (
	// alertTextMaxLen bounds an error carried by an alert. A provider that
	// answers with an HTML error page would otherwise put kilobytes into a log
	// line and an email; the first few hundred bytes are what identifies it.
	alertTextMaxLen    = 500
	alertTextTruncated = "…[truncated]"
)

var (
	// absoluteURLRE finds URLs so they can be cut to scheme://host. The path is
	// not safe to keep: RPC providers put the API key there (/v2/<key>).
	absoluteURLRE = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	bearerRE      = regexp.MustCompile(`(?i)\bbearer\s+[^\s"',;]+`)
	jwtRE         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	// credentialRE matches a credential-named field in both `name=value` and
	// JSON `"name": "value"` form. Provider errors fall back to the raw JSON
	// body, where a quote sits between the name and the colon — without the
	// optional quotes here every JSON credential passed through untouched.
	// The name may carry a prefix or suffix (secret_key, private_key,
	// x-api-key) so compound field names are caught too.
	credentialRE = regexp.MustCompile(`(?i)\b([a-z0-9_-]*(?:secret|token|password|passwd|pwd|api[_-]?key|private[_-]?key)[a-z0-9_-]*)"?\s*[=:]\s*"?[^\s,;&"'}]+`)
	// flwSecretKeyRE catches a Flutterwave secret key by its own prefix,
	// wherever it appears and whatever field (if any) carries it.
	flwSecretKeyRE = regexp.MustCompile(`\bFLWSECK(?:_TEST)?-[A-Za-z0-9-]+`)
	// hex64RE is the shape of a raw private key. Tx hashes share it and are
	// lost too; an alert that might carry a key is not worth the hash.
	hex64RE = regexp.MustCompile(`\b(0x)?[0-9a-fA-F]{64}\b`)
	// pgDetailRE is Postgres' "Key (col)=(value)" constraint detail, whose
	// value is a user's row data.
	pgDetailRE = regexp.MustCompile(`\)=\([^)]*\)`)
	emailRE    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

// RedactAlertText makes arbitrary error text safe for an operator alert.
//
// A worker's error is whatever a provider client, an RPC library or the
// database driver chose to put in it, and an alert sends it to email — no
// access control, unknown forwarding, unbounded retention. So this removes
// what those errors are known to carry: request URLs (paths and queries hold
// API keys), URL userinfo, bearer tokens and JWTs, key=value credentials,
// private-key-shaped hex, and user data from database constraint details. It
// keeps the scheme and host, which say which dependency broke and are not
// secret.
//
// It is a best-effort filter over text nobody here controls, not a guarantee;
// the guarantee is not putting secrets in errors in the first place.
func RedactAlertText(s string) string {
	s = absoluteURLRE.ReplaceAllStringFunc(s, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "[url-redacted]"
		}
		return u.Scheme + "://" + u.Hostname() + "/[path-redacted]"
	})
	s = RedactURLQueries(s)
	s = bearerRE.ReplaceAllString(s, "Bearer [redacted]")
	s = jwtRE.ReplaceAllString(s, "[jwt-redacted]")
	s = flwSecretKeyRE.ReplaceAllString(s, "[flw-key-redacted]")
	s = credentialRE.ReplaceAllString(s, "$1=[redacted]")
	s = hex64RE.ReplaceAllString(s, "[hex-redacted]")
	s = pgDetailRE.ReplaceAllString(s, ")=([redacted])")
	s = emailRE.ReplaceAllString(s, "[email-redacted]")

	s = strings.Join(strings.Fields(s), " ")
	if len(s) > alertTextMaxLen {
		cut := alertTextMaxLen
		// Back off to a rune boundary so the cut never emits invalid UTF-8.
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + alertTextTruncated
	}
	return s
}
