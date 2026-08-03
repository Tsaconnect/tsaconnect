package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

func ckFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "clubkonnect", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// newCKServer serves fixtures for catalog paths and lets tests intercept
// transactional calls. Returns the client plus the recorded requests.
func newCKServer(t *testing.T, handler http.HandlerFunc) (*ClubkonnectClient, *[]*url.URL) {
	t.Helper()
	var seen []*url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := *r.URL
		seen = append(seen, &u)
		if handler != nil {
			handler(w, r)
			return
		}
		// Default: serve the catalog fixture matching the path.
		name := strings.TrimPrefix(r.URL.Path, "/")
		name = strings.TrimSuffix(name, ".asp") + ".json"
		b, err := os.ReadFile(filepath.Join("testdata", "clubkonnect", name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html") // as the real API does
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c := NewClubkonnectClient(&config.Config{
		ClubkonnectBaseURL:       srv.URL,
		ClubkonnectUserID:        "CK101",
		ClubkonnectAPIKey:        "SECRETKEY123",
		ClubkonnectDataplanField: "code",
	})
	return c, &seen
}

func TestDecideVend(t *testing.T) {
	cases := []struct {
		code     int
		want     VendDecision
		floatLow bool
	}{
		{200, VendSuccess, false},
		{201, VendPending, false}, // ORDER_COMPLETED but "Network Unresponsive" — NOT success
		{100, VendPending, false},
		{199, VendPending, false},
		{299, VendPending, false},
		{300, VendPending, false},
		{399, VendPending, false},
		{600, VendPending, false},
		{699, VendPending, false},
		{0, VendPending, false},   // unknown/absent → reconciler's problem
		{999, VendPending, false}, // undocumented → pending, never a refund guess
		{400, VendFailed, false},
		{418, VendFailed, false},
		{412, VendFailed, true},
		{415, VendFailed, true},
		{416, VendFailed, true},
		{417, VendFailed, true},
		{499, VendFailed, false},
		{500, VendFailed, false},
		{521, VendFailed, false},
		{599, VendFailed, false},
	}
	for _, c := range cases {
		got, fl := decideVend(c.code)
		if got != c.want || fl != c.floatLow {
			t.Errorf("decideVend(%d) = (%v,%v), want (%v,%v)", c.code, got, fl, c.want, c.floatLow)
		}
	}
}

func TestParseVendVariants(t *testing.T) {
	c := &ClubkonnectClient{apiKey: "k"}

	res, err := c.parseVend([]byte(`{"orderid":"4842869","statuscode":"100","status":"ORDER_RECEIVED"}`))
	if err != nil || res.OrderID != "4842869" || res.StatusCode != 100 || res.Status != "ORDER_RECEIVED" {
		t.Fatalf("string codes: %+v err=%v", res, err)
	}

	// Numeric statuscode and a prepaid-meter token.
	res, err = c.parseVend([]byte(`{"orderid":9,"statuscode":200,"status":"ORDER_COMPLETED","metertoken":"1234-5678"}`))
	if err != nil || res.StatusCode != 200 || res.Delivery != "1234-5678" {
		t.Fatalf("numeric codes/token: %+v err=%v", res, err)
	}

	// e-PIN delivery.
	res, err = c.parseVend([]byte(`{"orderid":"1","statuscode":"200","status":"ORDER_COMPLETED","pin":"WAEC-999"}`))
	if err != nil || res.Delivery != "WAEC-999" {
		t.Fatalf("pin: %+v err=%v", res, err)
	}

	if _, err = c.parseVend([]byte(`refund failed, contact customer care`)); err == nil {
		t.Fatal("plain text must not parse as a vend result")
	}
	if _, err = c.parseVend([]byte(`{"unrelated":"x"}`)); err == nil {
		t.Fatal("JSON without order fields must error")
	}
}

func TestVendSendsAuthAndParams(t *testing.T) {
	c, seen := newCKServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"orderid":"77","statuscode":"100","status":"ORDER_RECEIVED"}`))
	})
	res, err := c.VendAirtime(context.Background(), "01", "08030001111", 500, "req-1")
	if err != nil || res.OrderID != "77" {
		t.Fatalf("vend: %+v err=%v", res, err)
	}
	q := (*seen)[0].Query()
	for k, want := range map[string]string{
		"UserID": "CK101", "APIKey": "SECRETKEY123", "MobileNetwork": "01",
		"Amount": "500", "MobileNumber": "08030001111", "RequestID": "req-1",
	} {
		if q.Get(k) != want {
			t.Errorf("param %s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestVendDataPlanFieldSwitch(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"orderid":"1","statuscode":"100","status":"ORDER_RECEIVED"}`))
	}
	plan := BillPlan{Code: "2", ProductID: "500"}

	c, seen := newCKServer(t, handler)
	if _, err := c.VendData(context.Background(), "01", plan, "08030001111", "r1"); err != nil {
		t.Fatal(err)
	}
	if got := (*seen)[0].Query().Get("DataPlan"); got != "2" {
		t.Errorf("default field: DataPlan = %q, want PRODUCT_CODE %q", got, "2")
	}

	c2, seen2 := newCKServer(t, handler)
	c2.dataplanField = "id"
	if _, err := c2.VendData(context.Background(), "01", plan, "08030001111", "r1"); err != nil {
		t.Fatal(err)
	}
	if got := (*seen2)[0].Query().Get("DataPlan"); got != "500" {
		t.Errorf("id field: DataPlan = %q, want PRODUCT_ID %q", got, "500")
	}
}

func TestErrorsNeverLeakAPIKey(t *testing.T) {
	c, _ := newCKServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Echo the full query back in a 500 — worst case for a leak.
		http.Error(w, "boom "+r.URL.RawQuery, http.StatusInternalServerError)
	})
	_, err := c.VendAirtime(context.Background(), "01", "080", 100, "r")
	if err == nil {
		t.Fatal("want error")
	}
	for _, secret := range []string{"SECRETKEY123", "CK101"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("credential %q leaked into error: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "params-redacted") && !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected redaction marker, got: %v", err)
	}
}

func TestTransportErrorsNeverLeakURL(t *testing.T) {
	// *url.Error embeds the ENTIRE request URL — including UserID and APIKey in
	// the query string. Point the client at a dead listener to force one.
	c := NewClubkonnectClient(&config.Config{
		ClubkonnectBaseURL: "http://127.0.0.1:1", // nothing listens here
		ClubkonnectUserID:  "CK101",
		ClubkonnectAPIKey:  "SECRETKEY123",
	})
	_, err := c.VendAirtime(context.Background(), "01", "08030001111", 100, "r")
	if err == nil {
		t.Fatal("want transport error")
	}
	for _, secret := range []string{"SECRETKEY123", "CK101", "APIKey="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%q leaked in transport error: %v", secret, err)
		}
	}
}

func TestWalletBalance(t *testing.T) {
	c, _ := newCKServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"balance":"257,660.16"}`))
	})
	got, err := c.WalletBalance(context.Background())
	if err != nil || got != 257660 {
		t.Fatalf("balance = %d err=%v, want 257660", got, err)
	}
}

func TestCancelOrderPlainText(t *testing.T) {
	c, _ := newCKServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`refund failed, contact customer care`))
	})
	err := c.CancelOrder(context.Background(), "123")
	if err == nil || !strings.Contains(err.Error(), "refund failed") {
		t.Fatalf("want plain-text cancel failure surfaced, got %v", err)
	}
}

func TestDisabledClient(t *testing.T) {
	c := NewClubkonnectClient(&config.Config{})
	if c.IsEnabled() {
		t.Fatal("unconfigured client must be disabled")
	}
	if _, err := c.VendAirtime(context.Background(), "01", "080", 100, "r"); err != ErrClubkonnectDisabled {
		t.Fatalf("want ErrClubkonnectDisabled, got %v", err)
	}
}

// ─── catalog parsing against live-captured fixtures ─────────────────────────

func TestAirtimeNetworksFixture(t *testing.T) {
	c, seen := newCKServer(t, nil)
	nets, err := c.AirtimeNetworks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]BillProvider{}
	for _, n := range nets {
		byCode[n.Code] = n
	}
	mtn, ok := byCode["01"]
	if !ok || mtn.Name != "MTN" || mtn.MinAmount != 50 || mtn.MaxAmount != 200000 {
		t.Fatalf("MTN entry wrong: %+v (all: %+v)", mtn, nets)
	}
	if _, ok := byCode["03"]; !ok {
		t.Fatalf("9mobile (m_9mobile key) not normalized: %+v", nets)
	}
	// Catalog calls carry no credentials.
	if q := (*seen)[0].Query(); q.Get("APIKey") != "" || q.Get("UserID") != "" {
		t.Error("catalog call must be unauthenticated")
	}
}

func TestDataPlansFixture(t *testing.T) {
	c, _ := newCKServer(t, nil)
	plans, err := c.DataPlans(context.Background(), "01")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) == 0 {
		t.Fatal("no MTN plans parsed")
	}
	first := plans[0]
	if first.Code == "" || first.ProductID == "" || first.CostNGN <= 0 {
		t.Fatalf("plan missing vend keys or cost: %+v", first)
	}
	if first.AmountNGN != 0 {
		t.Fatalf("client must not retail-price data plans (that is BillService's job): %+v", first)
	}
	nets, err := c.DataNetworks(context.Background())
	if err != nil || len(nets) < 4 {
		t.Fatalf("data networks: %+v err=%v", nets, err)
	}
}

func TestDiscosFixture(t *testing.T) {
	c, _ := newCKServer(t, nil)
	discos, err := c.Discos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range discos {
		if d.Code == "01" && strings.Contains(d.Name, "EKEDC") {
			found = true
			if d.MinAmount != 1000 || d.MaxAmount != 200000 {
				t.Fatalf("EKEDC bounds wrong: %+v", d)
			}
		}
	}
	if !found {
		t.Fatalf("EKEDC not parsed: %+v", discos)
	}
}

func TestCableCatalogFixture(t *testing.T) {
	c, _ := newCKServer(t, nil)
	providers, err := c.CableProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range providers {
		if strings.EqualFold(p.Name, "showmax") {
			t.Fatal("Showmax must be curated out (defective catalog)")
		}
	}
	pkgs, err := c.CablePackages(context.Background(), "dstv")
	if err != nil || len(pkgs) == 0 {
		t.Fatalf("dstv packages: %v err=%v", pkgs, err)
	}
	var padi *BillPlan
	for i := range pkgs {
		if pkgs[i].Code == "dstv-padi" {
			padi = &pkgs[i]
		}
	}
	if padi == nil || padi.AmountNGN != 4400 || padi.CostNGN != 4356 {
		t.Fatalf("dstv-padi face/cost wrong: %+v", padi)
	}
}

func TestBettingCompaniesFixture(t *testing.T) {
	c, _ := newCKServer(t, nil)
	companies, err := c.BettingCompanies(context.Background())
	if err != nil || len(companies) == 0 {
		t.Fatalf("betting: %v err=%v", companies, err)
	}
	found := false
	for _, b := range companies {
		if b.Code == "product-nairabet" {
			found = true
			if b.MinAmount != 100 || b.MaxAmount != 50000 {
				t.Fatalf("nairabet bounds wrong: %+v", b)
			}
		}
	}
	if !found {
		t.Fatalf("nairabet not parsed: %+v", companies)
	}
}

func TestEpinPlansFixture(t *testing.T) {
	c, _ := newCKServer(t, nil)
	waec, err := c.EpinPlans(context.Background(), "waec")
	if err != nil || len(waec) == 0 {
		t.Fatalf("waec plans: %v err=%v", waec, err)
	}
	if waec[0].AmountNGN <= 0 || waec[0].Code == "" {
		t.Fatalf("waec plan malformed: %+v", waec[0])
	}
	// JAMB's live catalog is currently empty — must parse to zero plans, not error.
	jamb, err := c.EpinPlans(context.Background(), "jamb")
	if err != nil || len(jamb) != 0 {
		t.Fatalf("jamb: want empty, got %v err=%v", jamb, err)
	}
}

func TestVerifyElectricityNameExtraction(t *testing.T) {
	c, _ := newCKServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":{"Customer_Name":"ADA OBI"}}`))
	})
	name, err := c.VerifyElectricity(context.Background(), "01", "1111")
	if err != nil || name != "ADA OBI" {
		t.Fatalf("name = %q err=%v", name, err)
	}

	c2, _ := newCKServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"customer_name":"INVALID_METERNO"}`))
	})
	if _, err := c2.VerifyElectricity(context.Background(), "01", "9999"); err == nil {
		t.Fatal("INVALID marker must be treated as not-found")
	}
}
