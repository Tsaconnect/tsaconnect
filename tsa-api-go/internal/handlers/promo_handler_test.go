package handlers

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func promoCtx(form url.Values) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c
}

func TestPromoFormTerms(t *testing.T) {
	cases := []struct {
		name    string
		form    url.Values
		present bool
		wantErr bool
		enabled bool
		counter *int
	}{
		{"absent leaves promo alone", url.Values{"name": {"x"}}, false, false, false, nil},
		{"disabled", url.Values{"promoEnabled": {"false"}}, true, false, false, nil},
		{"valid, no countdown", url.Values{"promoEnabled": {"true"}, "promoRequiredReferrals": {"5"}, "promoPrice": {"200"}}, true, false, true, nil},
		{"valid, countdown", url.Values{"promoEnabled": {"true"}, "promoRequiredReferrals": {"2"}, "promoPrice": {"200"}, "promoCountdownSeconds": {"120"}}, true, false, true, ptrInt(120)},
		{"no referrals", url.Values{"promoEnabled": {"true"}, "promoRequiredReferrals": {"0"}, "promoPrice": {"200"}}, true, true, true, nil},
		{"promo price not below price", url.Values{"promoEnabled": {"true"}, "promoRequiredReferrals": {"2"}, "promoPrice": {"2000"}}, true, true, true, nil},
		{"missing promo price", url.Values{"promoEnabled": {"true"}, "promoRequiredReferrals": {"2"}}, true, true, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			terms, present, msg := promoFormTerms(promoCtx(tc.form), 2000)
			if present != tc.present || (msg != "") != tc.wantErr {
				t.Fatalf("present=%v msg=%q", present, msg)
			}
			if !tc.wantErr && terms.Enabled != tc.enabled {
				t.Fatalf("enabled = %v", terms.Enabled)
			}
			if !tc.wantErr && (terms.CountdownSeconds == nil) != (tc.counter == nil) {
				t.Fatalf("countdown = %v", terms.CountdownSeconds)
			}
		})
	}
}

func ptrInt(n int) *int { return &n }
