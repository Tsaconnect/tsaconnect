package services

import (
	"strings"
	"testing"
)

// Worker errors are whatever a provider client, an RPC library or the database
// driver chose to put in them, and they are about to go to a mailbox. Each case
// is a shape a real error in this repo takes.
func TestRedactAlertText(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		secrets []string
		keep    []string
	}{
		{
			name:    "transport error embeds the whole request URL",
			in:      `flutterwave wallet-statement: Get "https://api.flutterwave.cloud/f4bexperience/wallets/statement?from=2026-09-11&size=100": context deadline exceeded`,
			secrets: []string{"wallets/statement", "size=100"},
			keep:    []string{"https://api.flutterwave.cloud", "context deadline exceeded"},
		},
		{
			name:    "API key carried in the RPC path",
			in:      `Post "https://sonic-mainnet.g.alchemy.com/v2/AbCdEfGh12345": dial tcp: i/o timeout`,
			secrets: []string{"AbCdEfGh12345"},
			keep:    []string{"sonic-mainnet.g.alchemy.com", "i/o timeout"},
		},
		{
			name:    "credentials in URL userinfo",
			in:      `failed to connect to postgres://tsa:hunter2@db.internal:5432/tsa`,
			secrets: []string{"hunter2", "tsa:"},
			keep:    []string{"db.internal"},
		},
		{
			name:    "bearer token",
			in:      `upstream said: Authorization: Bearer abc.def-ghi_jkl was rejected`,
			secrets: []string{"abc.def-ghi_jkl"},
		},
		{
			name:    "bare JWT",
			in:      `token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig_nature expired`,
			secrets: []string{"eyJhbGciOiJIUzI1NiJ9", "sig_nature"},
			keep:    []string{"expired"},
		},
		{
			name:    "key=value credentials",
			in:      `refused: client_secret=shh123&api_key: k-999, password=pw`,
			secrets: []string{"shh123", "k-999", "=pw"},
		},
		{
			name:    "raw 32-byte hex, the shape of a private key",
			in:      `signer 0x4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318 rejected`,
			secrets: []string{"4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"},
			keep:    []string{"rejected"},
		},
		{
			name:    "database detail naming a user's email",
			in:      `ERROR: duplicate key value violates unique constraint "users_email_key" (SQLSTATE 23505) Key (email)=(ada@example.com) already exists`,
			secrets: []string{"ada@example.com"},
			keep:    []string{"users_email_key", "23505"},
		},
		{
			name:    "a bare relative path with a query",
			in:      `GET /vend?UserID=CK101&APIKey=XYZ failed`,
			secrets: []string{"CK101", "XYZ"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactAlertText(tc.in)
			for _, s := range tc.secrets {
				if strings.Contains(got, s) {
					t.Errorf("leaked %q\n in: %s\nout: %s", s, tc.in, got)
				}
			}
			for _, k := range tc.keep {
				if !strings.Contains(got, k) {
					t.Errorf("dropped diagnostic %q\n in: %s\nout: %s", k, tc.in, got)
				}
			}
		})
	}
}

// An HTML error page as the body of a failed request is kilobytes of noise in a
// log line and an email subject line's worth of use.
func TestRedactAlertTextBoundsLengthAndFlattensLines(t *testing.T) {
	got := RedactAlertText("first line\nsecond line\r\n" + strings.Repeat("x", 5000))
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("alert text must stay on one line: %q", got[:40])
	}
	if len(got) > alertTextMaxLen+len(alertTextTruncated) {
		t.Errorf("len = %d, want at most %d", len(got), alertTextMaxLen+len(alertTextTruncated))
	}
	if !strings.HasSuffix(got, alertTextTruncated) {
		t.Errorf("a truncated error must say so")
	}
}
