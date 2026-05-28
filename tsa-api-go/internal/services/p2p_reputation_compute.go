package services

import "time"

// onlineWindow is how recently a user must have been seen to count as "online".
const onlineWindow = 5 * time.Minute

// TrustedThresholds are the (config-driven) gates for the Trusted badge.
type TrustedThresholds struct {
	MinReleased   int64
	MinCompletion float64
	MinAgeDays    int
}

// SellerStats is the privacy-safe reputation view surfaced on ads / profiles.
type SellerStats struct {
	CompletionRate *float64 `json:"completionRate,omitempty"`
	AvgReleaseSecs *int     `json:"avgReleaseSecs,omitempty"`
	TotalOrders    int64    `json:"totalOrders"`
	PositivePct    *float64 `json:"positivePct,omitempty"`
	Online         bool     `json:"online"`
	Verified       bool     `json:"verified"`
	Trusted        bool     `json:"trusted"`
	IsNew          bool     `json:"isNew"`
}

// CompletionRate = released / (released + cancelled); nil when no terminal trades yet.
func CompletionRate(released, cancelled int64) *float64 {
	total := released + cancelled
	if total <= 0 {
		return nil
	}
	r := float64(released) / float64(total)
	return &r
}

// PositivePct = up / (up + down); nil when no ratings yet.
func PositivePct(up, down int64) *float64 {
	total := up + down
	if total <= 0 {
		return nil
	}
	p := float64(up) / float64(total)
	return &p
}

// IsOnline reports whether lastSeen is within the online window of now.
func IsOnline(lastSeen *time.Time, now time.Time) bool {
	if lastSeen == nil {
		return false
	}
	return now.Sub(*lastSeen) < onlineWindow
}

// EvaluateTrusted applies the Trusted gates. A manual admin grant always wins.
func EvaluateTrusted(verified bool, released int64, completion *float64, ageDays int, manual bool, cfg TrustedThresholds) bool {
	if manual {
		return true
	}
	if !verified {
		return false
	}
	if released < cfg.MinReleased {
		return false
	}
	if completion == nil || *completion < cfg.MinCompletion {
		return false
	}
	if ageDays < cfg.MinAgeDays {
		return false
	}
	return true
}
