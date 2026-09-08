package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// ── stubs ────────────────────────────────────────────────────────────────
//
// These mirror stubInstitutions/stubBankDir in
// internal/services/paycrest_directory_test.go, but that file's stubs are
// unexported to package services — this package needs its own.

type stubPaycrestInstitutions struct {
	rows []services.PaycrestInstitution
}

func (s *stubPaycrestInstitutions) Institutions(context.Context, string) ([]services.PaycrestInstitution, error) {
	return s.rows, nil
}

type stubPaycrestBankDir struct {
	rows []services.NombaBank
}

func (s *stubPaycrestBankDir) ListBanks(context.Context) ([]services.NombaBank, error) {
	return s.rows, nil
}

func (s *stubPaycrestBankDir) Resolve(context.Context, string, string) (services.NombaLookupResult, error) {
	return services.NombaLookupResult{}, nil
}

func newPaycrestBanksHandler(pcRows []services.PaycrestInstitution, localRows []services.NombaBank) *PaycrestHandler {
	pc := &stubPaycrestInstitutions{rows: pcRows}
	local := &stubPaycrestBankDir{rows: localRows}
	dir := services.NewPaycrestDirectory(pc, local)
	return NewPaycrestHandler(nil, nil, dir)
}

type banksResponse struct {
	Success bool `json:"success"`
	Data    struct {
		BankCodes []string        `json:"bankCodes"`
		Banks     []bankJSON      `json:"banks"`
		Unmatched json.RawMessage `json:"unmatched"`
	} `json:"data"`
}

type bankJSON struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func doBanksRequest(t *testing.T, h *PaycrestHandler, rawQuery string) banksResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	target := "/api/paycrest/banks"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)

	h.Banks(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp banksResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	if resp.Data.Unmatched == nil {
		t.Fatalf("unmatched field missing from response entirely: %s", w.Body.String())
	}
	return resp
}

// NGN must still surface the matcher's unmatched Paycrest banks — this is the
// only visibility into whether OPay/PalmPay/Moniepoint are being matched by
// the NIBSS matcher, so a regression here silently kills that diagnostic.
func TestPaycrestHandler_Banks_NGN_ReportsUnmatched(t *testing.T) {
	h := newPaycrestBanksHandler(
		[]services.PaycrestInstitution{
			{Name: "Access Bank", Code: "ABNGNGLA", Type: "bank"},
			{Name: "OPay", Code: "OPAYNGPC", Type: "bank"}, // no local counterpart -> unmatched
		},
		[]services.NombaBank{{Name: "Access Bank", Code: "044"}},
	)

	resp := doBanksRequest(t, h, "currency=NGN")

	var unmatched []bankJSON
	if err := json.Unmarshal(resp.Data.Unmatched, &unmatched); err != nil {
		t.Fatalf("decode unmatched: %v", err)
	}
	if len(unmatched) != 1 || unmatched[0].Code != "OPAYNGPC" {
		t.Fatalf("want OPay reported unmatched for NGN, got %#v", unmatched)
	}
	// The NGN path must still return NIBSS codes, not Paycrest's own.
	if len(resp.Data.BankCodes) != 1 || resp.Data.BankCodes[0] != "044" {
		t.Fatalf("want NIBSS code 044 for NGN, got %#v", resp.Data.BankCodes)
	}
}

// A currency with no local list to reconcile against must report unmatched
// as an empty array, never null — the app reads those differently, and null
// would misleadingly suggest the field was skipped rather than intentionally
// empty.
func TestPaycrestHandler_Banks_NonNGN_UnmatchedIsEmptyArrayNotNull(t *testing.T) {
	h := newPaycrestBanksHandler(
		[]services.PaycrestInstitution{
			{Name: "Equity Bank Kenya", Code: "EQBLKENA", Type: "bank"},
		},
		[]services.NombaBank{{Name: "Access Bank", Code: "044"}}, // irrelevant for KES
	)

	resp := doBanksRequest(t, h, "currency=KES")

	if got := string(resp.Data.Unmatched); got != "[]" {
		t.Fatalf("unmatched = %s, want the literal empty array [] (not null)", got)
	}
	// Non-NGN passes Paycrest's own code through unchanged.
	if len(resp.Data.BankCodes) != 1 || resp.Data.BankCodes[0] != "EQBLKENA" {
		t.Fatalf("want Paycrest's own code EQBLKENA for KES, got %#v", resp.Data.BankCodes)
	}
}

// An empty (but present) currency query param must behave exactly like NGN,
// including reporting unmatched banks — not fall into the non-NGN branch and
// silently drop the diagnostic.
func TestPaycrestHandler_Banks_EmptyCurrencyBehavesAsNGN(t *testing.T) {
	h := newPaycrestBanksHandler(
		[]services.PaycrestInstitution{
			{Name: "Access Bank", Code: "ABNGNGLA", Type: "bank"},
			{Name: "OPay", Code: "OPAYNGPC", Type: "bank"},
		},
		[]services.NombaBank{{Name: "Access Bank", Code: "044"}},
	)

	resp := doBanksRequest(t, h, "currency=")

	var unmatched []bankJSON
	if err := json.Unmarshal(resp.Data.Unmatched, &unmatched); err != nil {
		t.Fatalf("decode unmatched: %v", err)
	}
	if len(unmatched) != 1 || unmatched[0].Code != "OPAYNGPC" {
		t.Fatalf("empty currency should behave as NGN and report OPay unmatched, got %#v", unmatched)
	}
	if len(resp.Data.BankCodes) != 1 || resp.Data.BankCodes[0] != "044" {
		t.Fatalf("empty currency should behave as NGN (NIBSS codes), got %#v", resp.Data.BankCodes)
	}
}
