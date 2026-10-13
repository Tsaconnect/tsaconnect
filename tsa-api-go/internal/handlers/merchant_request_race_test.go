package handlers

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

func pgUser(t *testing.T, db *gorm.DB, role string) *models.User {
	t.Helper()
	tag := uuid.New().String()[:8]
	u := &models.User{ID: uuid.New(), Name: "Ada Obi", Username: "u" + tag, Email: tag + "@example.com", Password: "x", Role: role,
		VerificationStatus: models.VerificationStatusVerified, VerifiedLegalName: "ADA OBI", AccountStatus: models.AccountStatusActive}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

func pgRequest(t *testing.T, db *gorm.DB, userID uuid.UUID, businessType string) string {
	t.Helper()
	mr := &models.MerchantRequest{ID: uuid.New(), UserID: userID, BusinessType: businessType, BusinessName: "ADA OBI",
		Address: "1 Marina", City: "Lagos", State: "Lagos", Country: "Nigeria", Phone: "+2348000000000",
		Status: models.MerchantRequestStatusPending, SettlementBankCode: "058", SettlementAccountNumber: "0123456789",
		SettlementAccountName: "ADA OBI", MakerWalletAddress: "0x52908400098527886E0F7030069857D2E4169EE7"}
	if err := db.Create(mr).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	return mr.ID.String()
}

// Owner 2026-10-13: a p2p_merchant and a liquidity_provider request for one
// user, approved by two admins at the same moment, both succeed: two rows, one
// per kind, role=merchant from the merchant approval only. The race is forced:
// while the first approval is writing its payment_merchants row, the second
// approval runs before the first one finishes. The (user_id, kind) index keeps
// the rows apart; this passes with or without the user-row lock, so it is not
// coverage for that lock (review M1). Neither approval touches the other's row.
func TestApproveMerchantRequest_ConcurrentMerchantAndLPApprovals_OnPostgres(t *testing.T) {
	for _, first := range []string{models.BusinessTypeP2PMerchant, models.BusinessTypeLiquidityProvider} {
		t.Run("first="+first, func(t *testing.T) {
			db := testutil.TempPostgres(t, "mr_race_", &models.User{}, &models.MerchantRequest{}, &models.PaymentMerchant{})
			if err := config.MigratePaymentMerchantRoleIndex(db); err != nil {
				t.Fatal(err)
			}
			h := lpHandler(t, db, true)
			applicant := pgUser(t, db, models.RoleUser)
			ids := map[string]string{
				models.BusinessTypeP2PMerchant:       pgRequest(t, db, applicant.ID, models.BusinessTypeP2PMerchant),
				models.BusinessTypeLiquidityProvider: pgRequest(t, db, applicant.ID, models.BusinessTypeLiquidityProvider),
			}
			second := models.BusinessTypeLiquidityProvider
			if first == second {
				second = models.BusinessTypeP2PMerchant
			}
			firstIsLP := first == models.BusinessTypeLiquidityProvider

			secondCode := 0
			secondDone := make(chan struct{})
			var started atomic.Bool
			if err := db.Callback().Create().Before("gorm:create").Register("test:approval_race", func(tx *gorm.DB) {
				pm, ok := tx.Statement.Dest.(*models.PaymentMerchant)
				if !ok || pm.IsLP() != firstIsLP || !started.CompareAndSwap(false, true) {
					return
				}
				go func() {
					defer close(secondDone)
					secondCode = approveAs(t, h, pgUser(t, db, models.RoleAdmin), ids[second])
				}()
				// Let the second approval finish first if nothing holds it back.
				select {
				case <-secondDone:
				case <-time.After(1500 * time.Millisecond):
				}
			}); err != nil {
				t.Fatal(err)
			}

			firstCode := approveAs(t, h, pgUser(t, db, models.RoleAdmin), ids[first])
			<-secondDone
			if firstCode != http.StatusOK || secondCode != http.StatusOK {
				t.Fatalf("codes first=%d second=%d, want both 200", firstCode, secondCode)
			}
			var rows []models.PaymentMerchant
			db.Where("user_id = ?", applicant.ID).Find(&rows)
			if len(rows) != 2 || rows[0].IsLP() == rows[1].IsLP() {
				t.Fatalf("rows = %+v, want one merchant and one LP", rows)
			}
			for _, r := range rows {
				if r.Status != models.PaymentMerchantStatusApproved {
					t.Fatalf("%s row status = %s", r.Kind, r.Status)
				}
			}
			var role string
			db.Model(&models.User{}).Where("id = ?", applicant.ID).Pluck("role", &role)
			if role != models.RoleMerchant {
				t.Fatalf("role = %s, want merchant (from the merchant approval only)", role)
			}
			var approved int64
			db.Model(&models.MerchantRequest{}).Where("user_id = ? AND status = ?", applicant.ID, models.MerchantRequestStatusApproved).Count(&approved)
			if approved != 2 {
				t.Fatalf("approved requests = %d, want 2", approved)
			}
		})
	}
}

// Two admins approve the same LP request at once. The (user_id, kind) index
// with ON CONFLICT DO NOTHING is the guarantee: the loser inserts nothing and
// reloads the winner's row. The user-row lock only serializes the two, so this
// passes with or without it (review M1). Both succeed, one LP row.
func TestApproveLPRequest_DoubleApproveSameRequestOneRow_OnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "mr_double_", &models.User{}, &models.MerchantRequest{}, &models.PaymentMerchant{})
	if err := config.MigratePaymentMerchantRoleIndex(db); err != nil {
		t.Fatal(err)
	}
	h := lpHandler(t, db, true)
	applicant := pgUser(t, db, models.RoleUser)
	id := pgRequest(t, db, applicant.ID, models.BusinessTypeLiquidityProvider)
	admins := []*models.User{pgUser(t, db, models.RoleAdmin), pgUser(t, db, models.RoleAdmin)}

	codes := make([]int, len(admins))
	var wg sync.WaitGroup
	for i, admin := range admins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = approveAs(t, h, admin, id)
		}()
	}
	wg.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("codes = %v, want both 200", codes)
	}
	var rows []models.PaymentMerchant
	db.Where("user_id = ?", applicant.ID).Find(&rows)
	if len(rows) != 1 || !rows[0].IsLP() {
		t.Fatalf("rows = %+v, want exactly one LP row", rows)
	}
}

// Review M2: between the deploy and the one-off, on Postgres, a second-role
// approval is a 409 second_role_not_enabled and nothing is applied. ON
// CONFLICT DO NOTHING absorbs the old (user_id) index's violation without
// aborting the transaction, so the handler sees the refusal and rolls back.
func TestApproveLPRequest_BeforeOneOffIs409_OnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "mr_preoneoff_", &models.User{}, &models.MerchantRequest{}, &models.PaymentMerchant{})
	if err := config.MigratePaymentMerchantRoleIndex(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX uq_payment_merchants_user ON payment_merchants (user_id)`).Error; err != nil {
		t.Fatal(err)
	}
	h := lpHandler(t, db, true)
	applicant := pgUser(t, db, models.RoleMerchant)
	merchant := &models.PaymentMerchant{ID: uuid.New(), UserID: applicant.ID, Name: "Ada Foods", Status: models.PaymentMerchantStatusActive}
	if err := db.Create(merchant).Error; err != nil {
		t.Fatal(err)
	}
	id := pgRequest(t, db, applicant.ID, models.BusinessTypeLiquidityProvider)

	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+id+"/approve", map[string]any{"note": "ok"}, pgUser(t, db, models.RoleAdmin))
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.ApproveMerchantRequest(c)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusConflict || body["code"] != "second_role_not_enabled" {
		t.Fatalf("approve = %d %v, want 409 second_role_not_enabled", w.Code, body)
	}
	var mr models.MerchantRequest
	db.First(&mr, "id = ?", id)
	if mr.Status != models.MerchantRequestStatusPending || mr.ReviewedBy != nil {
		t.Fatalf("request = %s reviewed_by=%v, want still pending and unreviewed", mr.Status, mr.ReviewedBy)
	}
	var rows []models.PaymentMerchant
	db.Where("user_id = ?", applicant.ID).Find(&rows)
	if len(rows) != 1 || rows[0].ID != merchant.ID || rows[0].IsLP() || rows[0].Status != models.PaymentMerchantStatusActive {
		t.Fatalf("rows = %+v, want only the untouched merchant row", rows)
	}
	var role string
	db.Model(&models.User{}).Where("id = ?", applicant.ID).Pluck("role", &role)
	if role != models.RoleMerchant {
		t.Fatalf("role = %s, want merchant unchanged", role)
	}
}

// Review M9: the second of two concurrent approvals of one request loaded it
// while still pending. It must not overwrite the first admin's review: the
// request update only moves a pending request, and a race lost to another
// approval answers 200 with the request as the winner left it.
func TestApproveLPRequest_ConcurrentSecondApprovalKeepsFirstReview_OnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "mr_review_", &models.User{}, &models.MerchantRequest{}, &models.PaymentMerchant{})
	if err := config.MigratePaymentMerchantRoleIndex(db); err != nil {
		t.Fatal(err)
	}
	h := lpHandler(t, db, true)
	applicant := pgUser(t, db, models.RoleUser)
	id := pgRequest(t, db, applicant.ID, models.BusinessTypeLiquidityProvider)
	first, second := pgUser(t, db, models.RoleAdmin), pgUser(t, db, models.RoleAdmin)

	secondCode := 0
	secondDone := make(chan struct{})
	var started atomic.Bool
	if err := db.Callback().Create().Before("gorm:create").Register("test:review_race", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*models.PaymentMerchant); !ok || !started.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer close(secondDone)
			secondCode = approveAs(t, h, second, id) // loads the request while it is still pending
		}()
		select {
		case <-secondDone:
		case <-time.After(1500 * time.Millisecond):
		}
	}); err != nil {
		t.Fatal(err)
	}

	firstCode := approveAs(t, h, first, id)
	<-secondDone
	if firstCode != http.StatusOK || secondCode != http.StatusOK {
		t.Fatalf("codes first=%d second=%d, want both 200", firstCode, secondCode)
	}
	var mr models.MerchantRequest
	db.First(&mr, "id = ?", id)
	if mr.Status != models.MerchantRequestStatusApproved || mr.ReviewedBy == nil || *mr.ReviewedBy != first.ID {
		t.Fatalf("request = %s reviewed_by=%v, want approved by the first admin %s", mr.Status, mr.ReviewedBy, first.ID)
	}
	var n int64
	db.Model(&models.PaymentMerchant{}).Where("user_id = ?", applicant.ID).Count(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want one LP row", n)
	}
}

// A rejection that commits while an approval is in flight leaves the approval
// with nothing to move: it must answer 409, never "approved".
func TestApproveLPRequest_LostToAConcurrentRejection_Is409_OnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "mr_reject_race_", &models.User{}, &models.MerchantRequest{}, &models.PaymentMerchant{})
	if err := config.MigratePaymentMerchantRoleIndex(db); err != nil {
		t.Fatal(err)
	}
	h := lpHandler(t, db, true)
	applicant := pgUser(t, db, models.RoleUser)
	id := pgRequest(t, db, applicant.ID, models.BusinessTypeLiquidityProvider)
	admin := pgUser(t, db, models.RoleAdmin)

	var started atomic.Bool
	if err := db.Callback().Create().Before("gorm:create").Register("test:reject_race", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*models.PaymentMerchant); !ok || !started.CompareAndSwap(false, true) {
			return
		}
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&models.MerchantRequest{}).Where("id = ?", id).
			Update("status", models.MerchantRequestStatusRejected).Error; err != nil {
			t.Errorf("reject: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}

	if code := approveAs(t, h, admin, id); code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", code)
	}
	var n int64
	db.Model(&models.PaymentMerchant{}).Where("user_id = ?", applicant.ID).Count(&n)
	if n != 0 {
		t.Fatalf("rows = %d, want the approval rolled back", n)
	}
}
