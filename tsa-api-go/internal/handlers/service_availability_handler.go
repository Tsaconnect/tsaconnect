package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// GetServiceAvailability handles GET /api/services/:id/availability — public list
// of a service's weekly open windows.
func (h *Handlers) GetServiceAvailability(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	var rows []models.ServiceAvailability
	if err := config.DB.Where("product_id = ?", productID).
		Order("day_of_week asc, start_time asc").Find(&rows).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch availability")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Availability fetched successfully", gin.H{"windows": rows})
}

type availabilityWindow struct {
	DayOfWeek int    `json:"dayOfWeek"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

type replaceAvailabilityRequest struct {
	Windows []availabilityWindow `json:"windows"`
}

// ReplaceServiceAvailability handles PUT /api/services/:id/availability — the
// owning merchant replaces the full set of weekly windows.
func (h *Handlers) ReplaceServiceAvailability(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}
	if ownedServiceProduct(c, productID) == nil {
		return
	}
	var req replaceAvailabilityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid availability")
		return
	}
	// Validate every window before touching the DB.
	rows := make([]models.ServiceAvailability, 0, len(req.Windows))
	for _, w := range req.Windows {
		if w.DayOfWeek < 0 || w.DayOfWeek > 6 {
			utils.ErrorResponse(c, http.StatusBadRequest, "dayOfWeek must be 0..6")
			return
		}
		startMin, ok1 := parseHHMM(w.StartTime)
		endMin, ok2 := parseHHMM(w.EndTime)
		if !ok1 || !ok2 {
			utils.ErrorResponse(c, http.StatusBadRequest, "startTime/endTime must be HH:MM")
			return
		}
		if endMin <= startMin {
			utils.ErrorResponse(c, http.StatusBadRequest, "endTime must be after startTime")
			return
		}
		rows = append(rows, models.ServiceAvailability{
			ID:        uuid.New(),
			ProductID: productID,
			DayOfWeek: w.DayOfWeek,
			StartTime: w.StartTime,
			EndTime:   w.EndTime,
		})
	}

	tx := config.DB.Begin()
	if err := tx.Where("product_id = ?", productID).Delete(&models.ServiceAvailability{}).Error; err != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update availability")
		return
	}
	if len(rows) > 0 {
		if err := tx.Create(&rows).Error; err != nil {
			tx.Rollback()
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update availability")
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update availability")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Availability updated", gin.H{"windows": rows})
}

// openSlot is one computed bookable slot.
type openSlot struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Capacity  int       `json:"capacity"`  // 0 = unlimited
	Booked    int       `json:"booked"`    // party-size already reserved
	Remaining int       `json:"remaining"` // -1 = unlimited
}

// GetServiceSlots handles GET /api/services/:id/slots?from=YYYY-MM-DD&to=YYYY-MM-DD
// — computes the bookable slots for a booking service in a date range: the
// availability windows stepped by the slot duration, each annotated with how much
// capacity remains (bookings whose order is not cancelled/refunded count as taken).
//
// Times are treated as naive/UTC (service-local wall-clock) for Phase 1; a
// per-service timezone is a documented follow-up.
func (h *Handlers) GetServiceSlots(c *gin.Context) {
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid service ID")
		return
	}

	from, err := time.Parse("2006-01-02", c.Query("from"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "from must be YYYY-MM-DD")
		return
	}
	to, err := time.Parse("2006-01-02", c.Query("to"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "to must be YYYY-MM-DD")
		return
	}
	if to.Before(from) {
		utils.ErrorResponse(c, http.StatusBadRequest, "to must be on or after from")
		return
	}
	// Cap the range to 31 days to bound work.
	if to.Sub(from) > 31*24*time.Hour {
		to = from.Add(31 * 24 * time.Hour)
	}

	var profile models.ServiceProfile
	config.DB.Where("product_id = ?", productID).First(&profile)
	slotMin := profile.SlotDurationMinutes
	if slotMin <= 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "This service has no slot duration configured")
		return
	}
	capacity := profile.MaxCapacityPerSlot

	var windows []models.ServiceAvailability
	config.DB.Where("product_id = ?", productID).Find(&windows)
	byDay := map[int][]models.ServiceAvailability{}
	for _, w := range windows {
		byDay[w.DayOfWeek] = append(byDay[w.DayOfWeek], w)
	}

	// One grouped query for all booked counts in the range (avoids an N+1 of one
	// query per generated slot on this public endpoint). Keyed by slot start Unix.
	booked := bookedBySlot(productID, from, to.AddDate(0, 0, 1))

	slots := []openSlot{}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		dow := int(d.Weekday())
		for _, w := range byDay[dow] {
			startMin, ok1 := parseHHMM(w.StartTime)
			endMin, ok2 := parseHHMM(w.EndTime)
			if !ok1 || !ok2 {
				continue
			}
			for m := startMin; m+slotMin <= endMin; m += slotMin {
				start := time.Date(d.Year(), d.Month(), d.Day(), m/60, m%60, 0, 0, time.UTC)
				end := start.Add(time.Duration(slotMin) * time.Minute)
				b := booked[start.Unix()]
				remaining := -1
				if capacity > 0 {
					remaining = capacity - b
					if remaining < 0 {
						remaining = 0
					}
				}
				slots = append(slots, openSlot{
					Start: start, End: end, Capacity: capacity, Booked: b, Remaining: remaining,
				})
			}
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Slots fetched successfully", gin.H{
		"slots":               slots,
		"slotDurationMinutes": slotMin,
		"maxCapacityPerSlot":  capacity,
	})
}

// bookedBySlot returns party-size booked per slot for a product in [from, to),
// counting only bookings whose order is NOT cancelled/refunded (a cancel/refund
// frees the slot automatically — design §4/F4). Keyed by slot-start Unix seconds.
func bookedBySlot(productID uuid.UUID, from, to time.Time) map[int64]int {
	type row struct {
		SlotStart time.Time
		Total     int
	}
	var rows []row
	config.DB.
		Model(&models.ServiceBooking{}).
		Joins("JOIN orders ON orders.id = service_bookings.order_id").
		Where("service_bookings.product_id = ? AND service_bookings.slot_start >= ? AND service_bookings.slot_start < ?", productID, from, to).
		Where("orders.status NOT IN ?", []string{models.OrderStatusCancelled, models.OrderStatusRefunded}).
		Group("service_bookings.slot_start").
		Select("service_bookings.slot_start AS slot_start, COALESCE(SUM(service_bookings.party_size),0) AS total").
		Scan(&rows)
	m := make(map[int64]int, len(rows))
	for _, r := range rows {
		m[r.SlotStart.UTC().Unix()] = r.Total
	}
	return m
}

// formatSlot is a tiny helper for slot error messages.
func formatSlot(t time.Time) string {
	return t.Format("2006-01-02 15:04")
}
