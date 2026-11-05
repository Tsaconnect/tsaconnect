package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The ORDER BY nearestFirst builds, without a database (the PostgreSQL-backed
// TestGetProductsByCategory_SortsNearestFirst runs it for real).
func TestNearestFirst_OrderClause(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	build := func(query string) (string, []interface{}) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/?"+query, nil)
		stmt := db.Model(&models.Product{}).Order(nearestFirst(c)).Find(&[]models.Product{}).Statement
		sql := stmt.SQL.String()
		return sql[strings.Index(sql, "ORDER BY"):], stmt.Vars
	}

	sql, vars := build("nearCountry=Nigeria&nearState=Enugu%20State&nearCity=Nsukka")
	want := "ORDER BY CASE WHEN city_key = ? AND state_key = ? THEN 0 WHEN state_key = ? THEN 1 WHEN country_key = ? THEN 2 ELSE 3 END, created_at DESC, id DESC"
	if sql != want {
		t.Fatalf("sql = %q\nwant  %q", sql, want)
	}
	if got := len(vars); got != 4 || vars[0] != "nsukka" || vars[1] != "enugu" || vars[2] != "enugu" || vars[3] != "nigeria" {
		t.Fatalf("vars = %v, want the location keys", vars)
	}

	// A city with no state is matched on its own.
	if sql, vars := build("nearCity=Nsukka"); !strings.HasPrefix(sql, "ORDER BY CASE WHEN city_key = ? THEN 0 ELSE 1 END") || len(vars) != 1 {
		t.Fatalf("city only: %q %v", sql, vars)
	}
	// No place: newest first.
	if sql, _ := build(""); sql != "ORDER BY `created_at` DESC,`id` DESC" {
		t.Fatalf("no place: %q", sql)
	}
}
