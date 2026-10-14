package routes

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// AdminAuth also admits merchant and support. An override marks someone
// verified and records the legal name their bank transfers are credited by, so
// the route must refuse both before the handler runs.
func TestKYCOverrideRouteIsAdminOnly(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	path := "/api/kyc/admin/override/" + uuid.New().String()

	for _, role := range []string{models.RoleSupport, models.RoleMerchant} {
		tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, role).ID)
		if w := payRoutesDo(r, http.MethodPost, path, tok, map[string]string{"legalName": "ANY"}); w.Code != http.StatusForbidden {
			t.Fatalf("%s: want 403, got %d %s", role, w.Code, w.Body.String())
		}
	}
	for _, role := range []string{models.RoleAdmin, models.RoleSuperAdmin} {
		tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, role).ID)
		// Past the gate, the unknown user id is the handler's 404.
		if w := payRoutesDo(r, http.MethodPost, path, tok, map[string]string{}); w.Code != http.StatusNotFound {
			t.Fatalf("%s: want the handler's 404, got %d %s", role, w.Code, w.Body.String())
		}
	}
}
