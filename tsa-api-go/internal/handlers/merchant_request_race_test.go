package handlers

import (
	"net/http"
	"sync"
	"testing"
	"time"

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
// approval runs (or is held back by the user-row lock) before the first one
// finishes. Neither approval touches the other's row.
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
			started := false
			if err := db.Callback().Create().Before("gorm:create").Register("test:approval_race", func(tx *gorm.DB) {
				pm, ok := tx.Statement.Dest.(*models.PaymentMerchant)
				if !ok || started || pm.IsLP() != firstIsLP {
					return
				}
				started = true
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

// Two admins approve the same LP request at once. The user-row lock runs them
// one after the other and the second finds the first's row through the
// kind-scoped lookup; the role index and ON CONFLICT DO NOTHING are only the
// backstop. Both succeed, and the user has exactly one LP row.
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
