package config

import "testing"

// The cookie-session origin check compares the browser's Origin header byte for
// byte, so the configured origin must already be in the browser's form.
func TestLoad_NormalizesWebAppOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		" HTTPS://App.TSAConnectWorld.com/ ": "https://app.tsaconnectworld.com",
		"https://app.tsaconnectworld.com//":  "https://app.tsaconnectworld.com",
		"http://LOCALHOST:5173":              "http://localhost:5173",
		"":                                   "",
	} {
		t.Setenv("WEB_APP_ORIGIN", raw)
		cfg := Load()
		if cfg.WebAppOrigin != want {
			t.Errorf("WEB_APP_ORIGIN %q loaded as %q, want %q", raw, cfg.WebAppOrigin, want)
		}
		if cfg.WebAuthEnabled() != (want != "") {
			t.Errorf("WEB_APP_ORIGIN %q: WebAuthEnabled = %v", raw, cfg.WebAuthEnabled())
		}
	}
}
