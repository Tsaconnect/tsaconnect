package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// loadFieldCatalog reads the active field catalogue. Where field_defs cannot be
// read it falls back to the built-in seed, which is exactly what validation used
// before the table existed — a read failure never rejects a valid built-in key.
func loadFieldCatalog(db *gorm.DB) models.FieldCatalog {
	catalog, err := models.LoadFieldCatalog(db)
	if err != nil {
		log.Printf("[fields] field_defs unreadable, using the built-in catalogue: %v", err)
		return models.BuiltinFieldCatalog()
	}
	return catalog
}

// fieldDefUsage counts where a catalogue key is used.
type fieldDefUsage struct {
	Categories int `json:"categories"`
	Services   int `json:"services"`
}

func (u fieldDefUsage) referenced() bool { return u.Categories > 0 || u.Services > 0 }

// fieldDefReferences counts the categories (listing_fields / booking_fields)
// and service booking forms (non-custom FieldConfig entries) that use key. A
// LIKE on the column text narrows the rows; each is then decoded, so the count
// is exact. key is a validated slug ([a-z0-9_]); "_" may match loosely in the
// LIKE, which the decode corrects.
func fieldDefReferences(db *gorm.DB, key string) (fieldDefUsage, error) {
	var usage fieldDefUsage
	pattern := "%\"" + key + "\"%"

	var cats []models.Category
	if err := db.Select("id", "listing_fields", "booking_fields").
		Where("CAST(listing_fields AS TEXT) LIKE ? OR CAST(booking_fields AS TEXT) LIKE ?", pattern, pattern).
		Find(&cats).Error; err != nil {
		return usage, err
	}
	for i := range cats {
		listing, _ := cats[i].GetListingFields()
		booking, _ := cats[i].GetBookingFields()
		if refsContain(listing, key) || refsContain(booking, key) {
			usage.Categories++
		}
	}

	var profiles []models.ServiceProfile
	if err := db.Select("id", "field_config").
		Where("CAST(field_config AS TEXT) LIKE ?", pattern).Find(&profiles).Error; err != nil {
		return usage, err
	}
	for i := range profiles {
		if cfg := profiles[i].GetFieldConfig(); cfg != nil {
			for _, f := range cfg.Fields {
				if f.Key == key && !f.Custom {
					usage.Services++
					break
				}
			}
		}
	}
	return usage, nil
}

func refsContain(refs []models.CategoryFieldRef, key string) bool {
	for _, r := range refs {
		if r.Key == key {
			return true
		}
	}
	return false
}

// ListFieldDefs handles GET /api/products/category/field-defs — the ACTIVE
// catalogue, for the app and the console's field pickers. ?scope=listing|booking
// keeps fields usable on that form (scope "both" included).
func (h *Handlers) ListFieldDefs(c *gin.Context) {
	query := config.DB.Model(&models.FieldDef{}).Where("is_active = ?", true)
	if usage := c.Query("scope"); usage != "" {
		if usage != models.FieldUsageListing && usage != models.FieldUsageBooking {
			utils.ErrorResponse(c, http.StatusBadRequest, "scope must be listing or booking")
			return
		}
		query = query.Where("scope IN ?", []string{usage, string(models.FieldScopeBoth)})
	}
	var defs []models.FieldDef
	if err := query.Order("field_group ASC, label ASC").Find(&defs).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch fields")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Fields fetched successfully", defs)
}

// AdminListFieldDefs handles GET /api/admin/field-defs — every catalogue entry,
// inactive included, each with its usage counts.
func (h *Handlers) AdminListFieldDefs(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}
	var defs []models.FieldDef
	if err := config.DB.Order("field_group ASC, label ASC").Find(&defs).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch fields")
		return
	}
	type row struct {
		models.FieldDef
		Usage fieldDefUsage `json:"usage"`
	}
	out := make([]row, 0, len(defs))
	for _, d := range defs {
		usage, err := fieldDefReferences(config.DB, d.Key)
		if err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch field usage")
			return
		}
		out = append(out, row{FieldDef: d, Usage: usage})
	}
	utils.SuccessResponse(c, http.StatusOK, "Fields fetched successfully", out)
}

type createFieldDefRequest struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Group   string   `json:"group"`
	Options []string `json:"options"`
	Scope   string   `json:"scope"`
}

// CreateFieldDef handles POST /api/admin/field-defs — the PM's "Add custom
// field". The new field joins the catalogue as a non-system field any category
// can use.
func (h *Handlers) CreateFieldDef(c *gin.Context) {
	user, ok := requireAdmin(c)
	if !ok {
		return
	}
	var req createFieldDefRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	now := time.Now()
	def := models.FieldDef{
		ID:        uuid.New(),
		Key:       strings.TrimSpace(req.Key),
		Type:      req.Type,
		Label:     strings.TrimSpace(req.Label),
		Group:     orDefault(strings.TrimSpace(req.Group), models.FieldGroupDetails),
		Options:   datatypes.JSONSlice[string](req.Options),
		Scope:     models.FieldScope(req.Scope),
		IsSystem:  false,
		IsActive:  true,
		CreatedBy: &user.ID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := models.ValidateFieldDef(&def); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	var n int64
	if err := config.DB.Model(&models.FieldDef{}).Where("key = ?", def.Key).Count(&n).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create field")
		return
	}
	if n > 0 {
		utils.ErrorResponseWithCode(c, http.StatusConflict, "A field with this key already exists", "field_key_taken")
		return
	}
	if err := config.DB.Create(&def).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create field")
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Field created successfully", def)
}

// updateFieldDefRequest: every field optional; absent = unchanged. Key never
// changes; type only while the field is unused and not a system field.
type updateFieldDefRequest struct {
	Key      *string   `json:"key"`
	Type     *string   `json:"type"`
	Label    *string   `json:"label"`
	Group    *string   `json:"group"`
	Options  *[]string `json:"options"`
	Scope    *string   `json:"scope"`
	IsActive *bool     `json:"isActive"`
}

// UpdateFieldDef handles PUT /api/admin/field-defs/:id.
func (h *Handlers) UpdateFieldDef(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}
	def, ok := loadFieldDefParam(c)
	if !ok {
		return
	}
	var req updateFieldDefRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Key != nil && *req.Key != def.Key {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "A field's key cannot change", "field_key_immutable")
		return
	}
	usage, err := fieldDefReferences(config.DB, def.Key)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to read field usage")
		return
	}

	next := def
	if req.Type != nil && *req.Type != def.Type {
		if def.IsSystem || usage.referenced() {
			utils.ErrorResponseWithCode(c, http.StatusConflict,
				"A system field's or a field in use's type cannot change", "field_type_immutable")
			return
		}
		next.Type = *req.Type
	}
	if req.Label != nil {
		next.Label = strings.TrimSpace(*req.Label)
	}
	if req.Group != nil {
		next.Group = orDefault(strings.TrimSpace(*req.Group), models.FieldGroupDetails)
	}
	if req.Options != nil {
		next.Options = datatypes.JSONSlice[string](*req.Options)
		if len(next.Options) == 0 && len(def.Options) > 0 && usage.Categories > 0 {
			// A category that set no options of its own relies on these.
			utils.ErrorResponseWithCode(c, http.StatusConflict,
				"Categories use this field's options; it cannot be left without any", "field_in_use")
			return
		}
	}
	if req.Scope != nil && models.FieldScope(*req.Scope) != def.Scope {
		if usage.referenced() && models.FieldScope(*req.Scope) != models.FieldScopeBoth {
			utils.ErrorResponseWithCode(c, http.StatusConflict,
				"A field in use can only be widened to scope both", "field_in_use")
			return
		}
		next.Scope = models.FieldScope(*req.Scope)
	}
	if req.IsActive != nil {
		next.IsActive = *req.IsActive
	}
	if err := models.ValidateFieldDef(&next); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	options, _ := json.Marshal([]string(next.Options))
	if next.Options == nil {
		options = nil
	}
	updates := map[string]interface{}{
		"type":        next.Type,
		"label":       next.Label,
		"field_group": next.Group,
		"options":     datatypes.JSON(options),
		"scope":       next.Scope,
		"is_active":   next.IsActive,
		"updated_at":  time.Now(),
	}
	if err := config.DB.Model(&models.FieldDef{}).Where("id = ?", def.ID).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update field")
		return
	}
	var updated models.FieldDef
	config.DB.First(&updated, "id = ?", def.ID)
	utils.SuccessResponse(c, http.StatusOK, "Field updated successfully", updated)
}

// DeleteFieldDef handles DELETE /api/admin/field-defs/:id. Only an unused,
// non-system field is deleted; anything else answers 409 — deactivate it
// (PUT isActive=false) instead, which takes it off every form.
func (h *Handlers) DeleteFieldDef(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}
	def, ok := loadFieldDefParam(c)
	if !ok {
		return
	}
	if def.IsSystem {
		utils.ErrorResponseWithCode(c, http.StatusConflict, "System fields cannot be deleted; deactivate it instead", "field_in_use")
		return
	}
	usage, err := fieldDefReferences(config.DB, def.Key)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to read field usage")
		return
	}
	if usage.referenced() {
		utils.ErrorResponseWithCodeData(c, http.StatusConflict, "This field is in use; deactivate it instead", "field_in_use", usage)
		return
	}
	if err := config.DB.Delete(&models.FieldDef{}, "id = ?", def.ID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to delete field")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Field deleted successfully", nil)
}

func loadFieldDefParam(c *gin.Context) (models.FieldDef, bool) {
	var def models.FieldDef
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid field ID")
		return def, false
	}
	if err := config.DB.First(&def, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Field not found")
		return def, false
	}
	return def, true
}
