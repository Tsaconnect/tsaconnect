package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func seedIntent(t *testing.T, s *ITService, dir, status string) models.ITIntent {
	t.Helper()
	it := models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), Direction: dir, Status: status,
		McgpAmount: "1", NgnKobo: "100", RateKobo: "1",
	}
	if err := s.db.Create(&it).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	return it
}

func TestListIntentsAdmin_FiltersAndPagination(t *testing.T) {
	s := newITTestService(t)
	ctx := context.Background()

	seedIntent(t, s, models.ITDirectionBuy, models.ITStatusCredited)
	seedIntent(t, s, models.ITDirectionSell, models.ITStatusDebited)
	seedIntent(t, s, models.ITDirectionSell, models.ITStatusDebited)
	seedIntent(t, s, models.ITDirectionSell, models.ITStatusSettled)

	// No filter → everything, total reflects the full set.
	all, total, err := s.ListIntentsAdmin(ctx, "", "", 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 4 || len(all) != 4 {
		t.Fatalf("no filter: total=%d len=%d, want 4/4", total, len(all))
	}

	// Direction filter.
	sells, total, err := s.ListIntentsAdmin(ctx, models.ITDirectionSell, "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(sells) != 3 {
		t.Fatalf("sell filter: total=%d len=%d, want 3/3", total, len(sells))
	}

	// Direction + status: the stuck-payout shape the console exists to surface.
	debited, total, err := s.ListIntentsAdmin(ctx, models.ITDirectionSell, models.ITStatusDebited, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(debited) != 2 {
		t.Fatalf("debited sells: total=%d len=%d, want 2/2", total, len(debited))
	}

	// Pagination: limit caps the page, total stays the unpaginated count.
	page, total, err := s.ListIntentsAdmin(ctx, "", "", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(page) != 1 {
		t.Fatalf("paged: total=%d len=%d, want total 4, page 1", total, len(page))
	}

	// Offset past the end yields an empty page but the true total.
	empty, total, err := s.ListIntentsAdmin(ctx, "", "", 50, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(empty) != 0 {
		t.Fatalf("offset past end: total=%d len=%d, want total 4, page 0", total, len(empty))
	}
}
