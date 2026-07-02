package models

import (
	"time"

	"github.com/google/uuid"
)

// ServiceAvailability is one weekly open window for a service (a day-of-week plus
// a start/end time). Booking services use it to generate bookable slots; order
// services use it as operating hours. A service may have several rows per day.
type ServiceAvailability struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	ProductID uuid.UUID `gorm:"type:uuid;not null;index" json:"productId"`
	DayOfWeek int       `gorm:"not null" json:"dayOfWeek"`       // 0=Sun … 6=Sat (time.Weekday)
	StartTime string    `gorm:"size:5;not null" json:"startTime"` // "HH:MM" 24h, local to the service
	EndTime   string    `gorm:"size:5;not null" json:"endTime"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (ServiceAvailability) TableName() string {
	return "service_availabilities"
}
