package models

import "time"

// JobRunMarker is the persisted "when did this periodic audit last run".
//
// The scans that only ALERT (the unwindowed late-debit audits) are meant to
// run about once a day. Keeping that in memory meant every deploy or restart
// re-ran the whole audit, and two instances ran it twice; a row makes the
// schedule survive restarts and lets instances claim a run against each other.
type JobRunMarker struct {
	Name      string    `gorm:"primaryKey;size:64" json:"name"`
	LastRunAt time.Time `gorm:"not null" json:"lastRunAt"`
}

func (JobRunMarker) TableName() string { return "job_run_markers" }
