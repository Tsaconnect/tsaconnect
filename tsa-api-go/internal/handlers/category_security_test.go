package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// recordSQL captures every statement db runs, so a test can prove caller input
// never reached SQL as text.
func recordSQL(t *testing.T, db *gorm.DB) func() []string {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	record := func(tx *gorm.DB) {
		mu.Lock()
		seen = append(seen, tx.Statement.SQL.String())
		mu.Unlock()
	}
	if err := db.Callback().Query().After("gorm:query").Register("test:record_sql", record); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().After("gorm:row").Register("test:record_sql_row", record); err != nil {
		t.Fatal(err)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

var maliciousSortOrders = []string{
	"desc; DROP TABLE products",
	",(SELECT pg_sleep(1))",
	"ASC, (CASE WHEN 1=1 THEN name END)",
}

func assertNoInjectedSQL(t *testing.T, statements []string) {
	t.Helper()
	if len(statements) == 0 {
		t.Fatal("no SQL recorded; the query under test never ran")
	}
	for _, s := range statements {
		upper := strings.ToUpper(s)
		for _, bad := range []string{"DROP", "PG_SLEEP", "1=1", ";"} {
			if strings.Contains(upper, bad) {
				t.Fatalf("caller input reached SQL (%q): %s", bad, s)
			}
		}
	}
}

func TestProductOrderClause_AllowListsColumnAndDirection(t *testing.T) {
	cases := []struct{ by, order, want string }{
		{"createdAt", "asc", "created_at ASC"},
		{"price", "ASC", "price ASC"},
		{"rating.average", "desc", "rating->>'average' DESC NULLS LAST"},
		{"name", "", "name DESC"},
		{"id; DROP TABLE users", "asc", "created_at ASC"},
		{"price", "desc; DROP TABLE x", "price DESC"},
		{"price", ",(SELECT pg_sleep(1))", "price DESC"},
	}
	for _, tc := range cases {
		if got := productOrderClause(tc.by, tc.order); got != tc.want {
			t.Errorf("productOrderClause(%q, %q) = %q, want %q", tc.by, tc.order, got, tc.want)
		}
	}
}

func TestGetProductsByCategory_MaliciousSortOrderNeverReachesSQL(t *testing.T) {
	for _, order := range maliciousSortOrders {
		t.Run(order, func(t *testing.T) {
			db := setupProductModerationDB(t)
			owner := seedUser(t, db, models.RoleUser)
			seedProduct(t, db, owner.ID, models.ProductStatusActive)
			statements := recordSQL(t, db)

			c, w := modQueryCtx(t, "sortBy=price&sortOrder="+url.QueryEscape(order), nil)
			(&Handlers{}).GetProductsByCategory(c)

			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			assertNoInjectedSQL(t, statements())
		})
	}
}

func TestGetUserProducts_MaliciousSortNeverReachesSQL(t *testing.T) {
	for _, order := range maliciousSortOrders {
		t.Run(order, func(t *testing.T) {
			db := setupProductModerationDB(t)
			owner := seedUser(t, db, models.RoleMerchant)
			seedProduct(t, db, owner.ID, models.ProductStatusActive)
			statements := recordSQL(t, db)

			q := "sortBy=" + url.QueryEscape("name; DROP TABLE users") + "&sortOrder=" + url.QueryEscape(order)
			c, w := modQueryCtx(t, q, owner)
			(&Handlers{}).GetUserProducts(c)

			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			assertNoInjectedSQL(t, statements())
		})
	}
}

// setupCategoryAdminDB widens the moderation schema with what the category
// writes touch.
func setupCategoryAdminDB(t *testing.T) (*gorm.DB, *models.Category) {
	t.Helper()
	db := setupProductModerationDB(t)
	for _, s := range []string{
		`ALTER TABLE categories ADD COLUMN sort_order INTEGER DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS category_booking_fees (
			id TEXT PRIMARY KEY,
			category_id TEXT NOT NULL,
			tier TEXT NOT NULL,
			amount_usd REAL DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME,
			UNIQUE(category_id, tier)
		)`,
	} {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	id := uuid.New()
	if err := db.Exec(`INSERT INTO categories (id, title, sort_order, created_at, updated_at) VALUES (?, 'Rides', 0, ?, ?)`,
		id.String(), time.Now(), time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	return db, &models.Category{ID: id}
}

func categorySortOrder(t *testing.T, db *gorm.DB, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := db.Raw(`SELECT sort_order FROM categories WHERE id = ?`, id.String()).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func bookingFeeRows(t *testing.T, db *gorm.DB, id uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT COUNT(*) FROM category_booking_fees WHERE category_id = ?`, id.String()).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func reorderCategories(t *testing.T, user *models.User, id uuid.UUID) (int, string) {
	c, w := modCtx(t, http.MethodPatch, fmt.Sprintf(`{"orders":[{"id":%q,"order":7}]}`, id), "", user)
	(&Handlers{}).ReorderCategories(c)
	return w.Code, w.Body.String()
}

func upsertBookingFees(t *testing.T, user *models.User, id uuid.UUID) (int, string) {
	c, w := modCtx(t, http.MethodPut, `{"fees":{"residential":5}}`, "", user)
	c.Params = gin.Params{{Key: "categoryId", Value: id.String()}}
	(&Handlers{}).UpsertCategoryBookingFees(c)
	return w.Code, w.Body.String()
}

func TestReorderCategories_RefusesNonAdmins(t *testing.T) {
	for _, role := range []string{models.RoleMerchant, models.RoleSupport, models.RoleUser} {
		t.Run(role, func(t *testing.T) {
			db, cat := setupCategoryAdminDB(t)
			if code, body := reorderCategories(t, seedUser(t, db, role), cat.ID); code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", code, body)
			}
			if got := categorySortOrder(t, db, cat.ID); got != 0 {
				t.Fatalf("a %s reordered categories (sort_order %d)", role, got)
			}
		})
	}
}

func TestReorderCategories_AdminSucceeds(t *testing.T) {
	db, cat := setupCategoryAdminDB(t)
	if code, body := reorderCategories(t, seedUser(t, db, models.RoleAdmin), cat.ID); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := categorySortOrder(t, db, cat.ID); got != 7 {
		t.Fatalf("sort_order %d, want 7", got)
	}
}

func TestUpsertCategoryBookingFees_RefusesNonAdmins(t *testing.T) {
	for _, role := range []string{models.RoleMerchant, models.RoleSupport, models.RoleUser} {
		t.Run(role, func(t *testing.T) {
			db, cat := setupCategoryAdminDB(t)
			if code, body := upsertBookingFees(t, seedUser(t, db, role), cat.ID); code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", code, body)
			}
			if n := bookingFeeRows(t, db, cat.ID); n != 0 {
				t.Fatalf("a %s wrote %d booking fee rows", role, n)
			}
		})
	}
}

func TestUpsertCategoryBookingFees_AdminSucceeds(t *testing.T) {
	db, cat := setupCategoryAdminDB(t)
	if code, body := upsertBookingFees(t, seedUser(t, db, models.RoleAdmin), cat.ID); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if n := bookingFeeRows(t, db, cat.ID); n != int64(len(models.BookingFeeTiers)) {
		t.Fatalf("%d booking fee rows, want %d", n, len(models.BookingFeeTiers))
	}
}
