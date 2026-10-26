package handlers

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func merchantRequestTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, ddl := range []string{
		`CREATE TABLE merchant_requests (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, business_type TEXT NOT NULL, status TEXT NOT NULL)`,
		`CREATE TABLE payment_merchants (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	return db
}

func addMerchantRequest(t *testing.T, db *gorm.DB, user uuid.UUID, businessType, status string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO merchant_requests (id, user_id, business_type, status) VALUES (?, ?, ?, ?)`,
		uuid.NewString(), user.String(), businessType, status).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func addPaymentMerchant(t *testing.T, db *gorm.DB, user uuid.UUID, kind, status string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO payment_merchants (id, user_id, kind, status) VALUES (?, ?, ?, ?)`,
		uuid.NewString(), user.String(), kind, status).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestHasApprovedMerchantRequest(t *testing.T) {
	db := merchantRequestTestDB(t)
	approved, pending, lp, none := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	pmMerchant, pmLP, pmPending := uuid.New(), uuid.New(), uuid.New()
	addPaymentMerchant(t, db, pmMerchant, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive)
	// The reported account: approved application aside, an active LP row only.
	addPaymentMerchant(t, db, pmLP, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive)
	addPaymentMerchant(t, db, pmPending, models.PaymentMerchantKindMerchant, "pending")
	addMerchantRequest(t, db, approved, "payment_acceptance", models.MerchantRequestStatusApproved)
	addMerchantRequest(t, db, pending, "payment_acceptance", "pending")
	addMerchantRequest(t, db, lp, models.BusinessTypeLiquidityProvider, models.MerchantRequestStatusApproved)

	for name, tc := range map[string]struct {
		user uuid.UUID
		want bool
	}{
		"approved application":                  {approved, true},
		"pending application":                   {pending, false},
		"approved liquidity-provider (retired)": {lp, false},
		"no application":                        {none, false},
	} {
		got, err := hasApprovedMerchantRequest(db, tc.user)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v err %v, want %v", name, got, err, tc.want)
		}
	}
}

// User has its own MarshalJSON: the flag must still reach the response, and the
// user's own fields must survive.
func TestProfileWithMerchantFlag_KeepsUserFieldsAndAddsFlag(t *testing.T) {
	u := models.User{ID: uuid.New(), Username: "obaino4christ", Role: models.RoleSuperAdmin}
	got, err := profileWithMerchantFlag(u, true)
	if err != nil {
		t.Fatal(err)
	}
	if got["merchantApproved"] != true {
		t.Errorf("merchantApproved = %v, want true", got["merchantApproved"])
	}
	if got["username"] != "obaino4christ" || got["role"] != string(models.RoleSuperAdmin) {
		t.Errorf("user fields lost: username=%v role=%v", got["username"], got["role"])
	}
}
