package handlers

import (
	"encoding/json"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// writeAdminAudit appends one row to the shared admin trail (p2p_audit_logs): the acting admin,
// the action and its metadata. Never key material. A failed write is logged, not dropped: the
// action already happened, and the log line is then its only record.
func writeAdminAudit(c *gin.Context, db *gorm.DB, action string, meta map[string]any) {
	raw, _ := json.Marshal(meta)
	row := models.P2PAuditLog{ID: uuid.New(), Action: action, Metadata: datatypes.JSON(raw)}
	if u := getUserFromContext(c); u != nil {
		row.ActorID = &u.ID
	}
	if db == nil {
		return
	}
	if err := db.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		log.Printf("[audit] ALERT admin_audit_write_failed action=%s meta=%s err=%v", action, raw, err)
	}
}
