package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ReferralGraphService answers questions about the referral graph defined by
// users.referred_by: who sits above a user (uplines / ancestors) and who sits
// below (downlines / descendants).
//
// This service walks the upline chain for the fee/TP overhaul (SP2): it does the
// whole climb in ONE query using a recursive CTE, and it FLAGS inactive ancestors
// rather than stopping — SP3's route-and-continue policy (§3 of the design) needs
// the full path, including the inactive nodes whose generation share is routed to
// the global pool. SP3 and the TP/referrals UI consume it.
//
// Portability: every query uses `WITH RECURSIVE`, which is supported by BOTH
// PostgreSQL (production) and SQLite (the in-memory unit-test DB). Cycle safety
// is enforced WITHOUT any PG-only construct (no PostgreSQL `CYCLE` clause):
// instead each walk carries a delimited `path` text column and the recursive
// join excludes any node already on the path via a portable `LIKE` test, while
// a depth predicate caps the climb/descent at maxGen. A referral cycle
// (A→B→A) or a self-reference therefore terminates instead of looping forever.
type ReferralGraphService struct {
	db *gorm.DB
}

// NewReferralGraphService constructs the service over the given DB handle.
func NewReferralGraphService(db *gorm.DB) *ReferralGraphService {
	return &ReferralGraphService{db: db}
}

// Ancestor is one node on the upline path above a user.
type Ancestor struct {
	UserID     uuid.UUID `json:"userId"`
	Generation int       `json:"generation"` // 1 = direct referrer, 2 = their referrer, ...
	Active     bool      `json:"active"`     // false => inactive/suspended/deleted ancestor
}

// Descendant is one node on a downline generation below a user.
type Descendant struct {
	UserID     uuid.UUID `json:"userId"`
	Generation int       `json:"generation"`
	Name       string    `json:"name"`
	Username   string    `json:"username"`
	Active     bool      `json:"active"`
}

// ancestorRow is the scan target for the Uplines CTE. account_status is scanned
// as the raw string so the boolean Active flag is computed in Go (keeping the
// SQL free of enum-comparison portability concerns).
type ancestorRow struct {
	ID            string
	Generation    int
	AccountStatus string
}

// descendantRow is the scan target for the Downlines / DownlineCounts CTEs.
type descendantRow struct {
	ID            string
	Generation    int
	Name          string
	Username      string
	AccountStatus string
}

// genCountRow is the scan target for DownlineCounts.
type genCountRow struct {
	Generation int
	Cnt        int
}

// Uplines returns up to maxGen ancestors of userID by walking users.referred_by
// upward, in generation order (gen 1 first). Each result carries its Generation
// (1..maxGen) and an Active flag.
//
// Behavior contract (do not regress):
//   - A SINGLE recursive query is issued (not N sequential SELECTs).
//   - Inactive ancestors are RETURNED, flagged Active=false — the walk does NOT
//     stop at them (SP3 routes their share to the global pool and continues).
//   - CYCLE-SAFE + DEPTH-BOUNDED: a referral cycle or self-reference cannot
//     infinite-loop; the climb stops at maxGen and never revisits a node.
func (s *ReferralGraphService) Uplines(ctx context.Context, userID uuid.UUID, maxGen int) ([]Ancestor, error) {
	if maxGen <= 0 {
		return []Ancestor{}, nil
	}

	// Recursive CTE climbing referred_by from the start user.
	//   - base row: the start user at depth 0 (NOT returned; only its referrer
	//     and above are ancestors).
	//   - step: join the current node's referrer, incrementing depth, while
	//     (a) depth+1 <= maxGen and (b) the referrer is not already on the path
	//     (cycle guard). The path is a delimited list of visited ids; the LIKE
	//     test is portable across SQLite and PostgreSQL.
	const q = `
WITH RECURSIVE uplines(id, referred_by, depth, path) AS (
    SELECT u.id, u.referred_by, 0, '/' || u.id || '/'
    FROM users u
    WHERE u.id = ?
  UNION ALL
    SELECT p.id, p.referred_by, up.depth + 1, up.path || p.id || '/'
    FROM users p
    JOIN uplines up ON p.id = up.referred_by
    WHERE up.depth + 1 <= ?
      AND up.path NOT LIKE '%/' || p.id || '/%'
)
SELECT u.id AS id, up.depth AS generation, u.account_status AS account_status
FROM uplines up
JOIN users u ON u.id = up.id
WHERE up.depth >= 1
ORDER BY up.depth ASC`

	var rows []ancestorRow
	if err := s.db.WithContext(ctx).Raw(q, userID.String(), maxGen).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("uplines walk for %s: %w", userID, err)
	}

	out := make([]Ancestor, 0, len(rows))
	for _, r := range rows {
		uid, err := uuid.Parse(r.ID)
		if err != nil {
			return nil, fmt.Errorf("uplines: parse ancestor id %q: %w", r.ID, err)
		}
		out = append(out, Ancestor{
			UserID:     uid,
			Generation: r.Generation,
			Active:     r.AccountStatus == models.AccountStatusActive,
		})
	}
	return out, nil
}

// DownlineCounts returns the number of descendants per generation (1..maxGen)
// below userID, for the TP/referrals UI. Generations with zero descendants are
// omitted from the map.
//
// CYCLE-SAFE + DEPTH-BOUNDED via the same path/LIKE guard as Uplines.
func (s *ReferralGraphService) DownlineCounts(ctx context.Context, userID uuid.UUID, maxGen int) (map[int]int, error) {
	if maxGen <= 0 {
		return map[int]int{}, nil
	}

	// Soft-deleted descendants are excluded (deleted_at IS NULL on the recursive
	// child join), matching the existing referrals UI which filters them out
	// (tp_handler.go:177, user_handler.go:500). Deletion sets BOTH
	// account_status='deleted' and deleted_at (user_handler.go:461-462). The base
	// row (the start user) also carries the guard for completeness; it is never
	// counted regardless (depth >= 1). NOTE: this differs from Uplines, where
	// deleted ANCESTORS must remain (flagged Active=false) so SP3 routes their
	// generation share to the global pool.
	const q = `
WITH RECURSIVE downlines(id, depth, path) AS (
    SELECT u.id, 0, '/' || u.id || '/'
    FROM users u
    WHERE u.id = ? AND u.deleted_at IS NULL
  UNION ALL
    SELECT c.id, dn.depth + 1, dn.path || c.id || '/'
    FROM users c
    JOIN downlines dn ON c.referred_by = dn.id
    WHERE dn.depth + 1 <= ?
      AND c.deleted_at IS NULL
      AND dn.path NOT LIKE '%/' || c.id || '/%'
)
SELECT depth AS generation, COUNT(*) AS cnt
FROM downlines
WHERE depth >= 1
GROUP BY depth
ORDER BY depth ASC`

	var rows []genCountRow
	if err := s.db.WithContext(ctx).Raw(q, userID.String(), maxGen).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("downline counts for %s: %w", userID, err)
	}

	out := make(map[int]int, len(rows))
	for _, r := range rows {
		out[r.Generation] = r.Cnt
	}
	return out, nil
}

// Downlines returns a paginated slice of descendants at a specific generation
// below userID (for the referrals screen). Results are ordered by username for
// stable pagination. gen must be >= 1.
//
// CYCLE-SAFE + DEPTH-BOUNDED: the descent never climbs past gen and never
// revisits a node, so cycles cannot loop.
func (s *ReferralGraphService) Downlines(ctx context.Context, userID uuid.UUID, gen, limit, offset int) ([]Descendant, error) {
	if gen <= 0 {
		return []Descendant{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	// Descend only as deep as `gen` (the path guard plus the depth bound keep
	// the walk finite), then select just the rows AT that generation.
	// Soft-deleted descendants are excluded (deleted_at IS NULL on the recursive
	// child join), matching the existing referrals UI (tp_handler.go:177,
	// user_handler.go:500). See DownlineCounts for the full rationale; Uplines
	// intentionally retains deleted ancestors (flagged inactive).
	const q = `
WITH RECURSIVE downlines(id, depth, path) AS (
    SELECT u.id, 0, '/' || u.id || '/'
    FROM users u
    WHERE u.id = ? AND u.deleted_at IS NULL
  UNION ALL
    SELECT c.id, dn.depth + 1, dn.path || c.id || '/'
    FROM users c
    JOIN downlines dn ON c.referred_by = dn.id
    WHERE dn.depth + 1 <= ?
      AND c.deleted_at IS NULL
      AND dn.path NOT LIKE '%/' || c.id || '/%'
)
SELECT u.id AS id, dn.depth AS generation, u.name AS name, u.username AS username, u.account_status AS account_status
FROM downlines dn
JOIN users u ON u.id = dn.id
WHERE dn.depth = ?
ORDER BY u.username ASC
LIMIT ? OFFSET ?`

	var rows []descendantRow
	if err := s.db.WithContext(ctx).Raw(q, userID.String(), gen, gen, limit, offset).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("downlines gen %d for %s: %w", gen, userID, err)
	}

	out := make([]Descendant, 0, len(rows))
	for _, r := range rows {
		uid, err := uuid.Parse(r.ID)
		if err != nil {
			return nil, fmt.Errorf("downlines: parse descendant id %q: %w", r.ID, err)
		}
		out = append(out, Descendant{
			UserID:     uid,
			Generation: r.Generation,
			Name:       r.Name,
			Username:   r.Username,
			Active:     r.AccountStatus == models.AccountStatusActive,
		})
	}
	return out, nil
}
