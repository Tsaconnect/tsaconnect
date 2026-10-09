package handlers

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

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

// Review I2 (O-B3, decision 11): a p2p_merchant and a liquidity_provider
// request for one user, approved by two admins at the same moment, never leave
// an LP with role=merchant. The race is forced: while the first approval is
// writing its payment_merchants row, the second approval runs (or is held
// back by the user-row lock) before the first one finishes.
func TestApproveMerchantRequest_ConcurrentMerchantAndLPApprovals_OnPostgres(t *testing.T) {
	for _, first := range []string{models.BusinessTypeP2PMerchant, models.BusinessTypeLiquidityProvider} {
		t.Run("first="+first, func(t *testing.T) {
			db := testutil.TempPostgres(t, "mr_race_", &models.User{}, &models.MerchantRequest{}, &models.PaymentMerchant{})
			if err := db.Exec(`CREATE UNIQUE INDEX uq_payment_merchants_user ON payment_merchants (user_id)`).Error; err != nil {
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

			var role string
			db.Model(&models.User{}).Where("id = ?", applicant.ID).Pluck("role", &role)
			var rows []models.PaymentMerchant
			db.Where("user_id = ?", applicant.ID).Find(&rows)
			if len(rows) != 1 {
				t.Fatalf("payment_merchants rows = %d, want 1", len(rows))
			}
			if rows[0].IsLP() && role == models.RoleMerchant {
				t.Fatalf("an LP ended with role=merchant (first=%d second=%d)", firstCode, secondCode)
			}
			codes := map[int]int{firstCode: 1}
			codes[secondCode]++
			if codes[http.StatusOK] != 1 || codes[http.StatusConflict] != 1 {
				t.Fatalf("codes first=%d second=%d, want one 200 and one 409", firstCode, secondCode)
			}
			var statuses []string
			db.Model(&models.MerchantRequest{}).Where("user_id = ? AND status = ?", applicant.ID, models.MerchantRequestStatusApproved).Pluck("business_type", &statuses)
			if len(statuses) != 1 || statuses[0] != map[bool]string{true: models.BusinessTypeLiquidityProvider, false: models.BusinessTypeP2PMerchant}[rows[0].IsLP()] {
				t.Fatalf("approved requests = %v for a %s row: the losing approval was half-applied", statuses, rows[0].Kind)
			}
		})
	}
}
