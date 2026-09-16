package services

import "testing"

// Indexer logos load straight into the app's image views, so only https URLs
// are passed through.
func TestHTTPSLogo(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"https is kept", "https://assets.ankr.com/charts/icon-only/eth.svg", "https://assets.ankr.com/charts/icon-only/eth.svg"},
		{"surrounding space is trimmed", "  https://cdn.moralis.io/usdt.png ", "https://cdn.moralis.io/usdt.png"},
		{"scheme is case-insensitive", "HTTPS://cdn.example/a.png", "HTTPS://cdn.example/a.png"},
		{"http is dropped", "http://cdn.example/a.png", ""},
		{"data URI is dropped", "data:image/png;base64,AAAA", ""},
		{"relative path is dropped", "/logos/a.png", ""},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := httpsLogo(tt.raw); got != tt.want {
				t.Errorf("httpsLogo(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
