package models

import (
	"time"

	"github.com/google/uuid"
)

// P2PMerchantStats is the materialized reputation read-model, one row per user.
// Lifetime counters + thumbs + last_seen are updated incrementally off trade/rating
// events; the 30-day avg times are recomputed by a nightly reconcile job. All values
// are advisory — they never gate money movement.
type P2PMerchantStats struct {
	UserID            uuid.UUID  `gorm:"type:uuid;primaryKey" json:"userId"`
	ReleasedCount     int64      `gorm:"not null;default:0" json:"releasedCount"`                         // lifetime completed (either role)
	CancelledCount    int64      `gorm:"not null;default:0" json:"cancelledCount"`                        // lifetime self-attributable cancels
	AvgReleaseSecs30d *int       `gorm:"column:avg_release_secs_30d" json:"avgReleaseSecs30d,omitempty"` // seller responsiveness (job-computed)
	AvgPaySecs30d     *int       `gorm:"column:avg_pay_secs_30d" json:"avgPaySecs30d,omitempty"`         // buyer pay latency (job-computed)
	ThumbsUp          int64      `gorm:"not null;default:0" json:"thumbsUp"`
	ThumbsDown        int64      `gorm:"not null;default:0" json:"thumbsDown"`
	LastSeenAt        *time.Time `json:"lastSeenAt,omitempty"`
	Verified          bool       `gorm:"not null;default:false" json:"verified"`
	Trusted           bool       `gorm:"not null;default:false" json:"trusted"`
	TrustedManual     bool       `gorm:"not null;default:false" json:"trustedManual"` // admin override
	UpdatedAt         time.Time  `json:"updatedAt"`
}
