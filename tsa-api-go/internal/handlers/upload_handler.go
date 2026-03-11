package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudinary/cloudinary-go/v2"
	"github.com/cloudinary/cloudinary-go/v2/api/admin"
	"github.com/cloudinary/cloudinary-go/v2/api/uploader"
	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// UploadHandler handles file upload HTTP requests.
type UploadHandler struct {
	cloudinary *cloudinary.Cloudinary
}

// NewUploadHandler creates a new UploadHandler.
func NewUploadHandler(cfg *config.Config) *UploadHandler {
	cld, err := cloudinary.NewFromParams(
		cfg.CloudinaryCloudName,
		cfg.CloudinaryAPIKey,
		cfg.CloudinaryAPISecret,
	)
	if err != nil {
		// If cloudinary is not configured, handler will check for nil
		return &UploadHandler{}
	}
	return &UploadHandler{cloudinary: cld}
}

func (h *UploadHandler) ensureCloudinary(c *gin.Context) bool {
	if h.cloudinary == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "Cloudinary is not configured",
		})
		return false
	}
	return true
}

// UploadSingle uploads a single image file.
// POST /api/upload/single — auth, multipart 'image', optimize, upload to Cloudinary.
func (h *UploadHandler) UploadSingle(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	if !h.ensureCloudinary(c) {
		return
	}

	file, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Image file is required"})
		return
	}

	f, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to read file"})
		return
	}
	defer f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := h.cloudinary.Upload.Upload(ctx, f, uploader.UploadParams{
		Folder:         "tsa",
		Transformation: "q_auto,f_auto",
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to upload image"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Image uploaded successfully",
		"data": gin.H{
			"url":      result.SecureURL,
			"publicId": result.PublicID,
			"width":    result.Width,
			"height":   result.Height,
			"format":   result.Format,
			"bytes":    result.Bytes,
		},
	})
}

// UploadMultiple uploads multiple image files (max 10).
// POST /api/upload/multiple — auth, multipart 'images', max 10, optimize each.
func (h *UploadHandler) UploadMultiple(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	if !h.ensureCloudinary(c) {
		return
	}

	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid multipart form"})
		return
	}

	files := form.File["images"]
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "At least one image file is required"})
		return
	}
	if len(files) > 10 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Maximum 10 images allowed"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	results := make([]gin.H, 0, len(files))
	errors := make([]gin.H, 0)

	for i, file := range files {
		f, err := file.Open()
		if err != nil {
			errors = append(errors, gin.H{"index": i, "error": "Failed to read file"})
			continue
		}

		result, err := h.cloudinary.Upload.Upload(ctx, f, uploader.UploadParams{
			Folder:         "tsa",
			Transformation: "q_auto,f_auto",
		})
		f.Close()

		if err != nil {
			errors = append(errors, gin.H{"index": i, "error": "Failed to upload"})
			continue
		}

		results = append(results, gin.H{
			"url":      result.SecureURL,
			"publicId": result.PublicID,
			"width":    result.Width,
			"height":   result.Height,
			"format":   result.Format,
			"bytes":    result.Bytes,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": fmt.Sprintf("Uploaded %d of %d images", len(results), len(files)),
		"data": gin.H{
			"uploaded": results,
			"errors":   errors,
			"total":    len(files),
		},
	})
}

// UploadBase64 uploads a base64-encoded image.
// POST /api/upload/base64 — validate base64, upload to Cloudinary.
func (h *UploadHandler) UploadBase64(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	if !h.ensureCloudinary(c) {
		return
	}

	var body struct {
		Image string `json:"image" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Base64 image string is required"})
		return
	}

	// Validate base64 — strip data URI prefix if present
	imageData := body.Image
	if idx := strings.Index(imageData, ","); idx != -1 {
		imageData = imageData[idx+1:]
	}

	if _, err := base64.StdEncoding.DecodeString(imageData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid base64 image data"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Upload data URI directly; Cloudinary accepts data URIs
	uploadInput := body.Image
	if !strings.HasPrefix(uploadInput, "data:") {
		uploadInput = "data:image/png;base64," + imageData
	}

	result, err := h.cloudinary.Upload.Upload(ctx, uploadInput, uploader.UploadParams{
		Folder:         "tsa",
		Transformation: "q_auto,f_auto",
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to upload image"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Image uploaded successfully",
		"data": gin.H{
			"url":      result.SecureURL,
			"publicId": result.PublicID,
			"width":    result.Width,
			"height":   result.Height,
			"format":   result.Format,
		},
	})
}

// UploadFacial uploads facial verification images (base64 array, max 10).
// POST /api/upload/facial — base64 array, max 10, upload each.
func (h *UploadHandler) UploadFacial(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	if !h.ensureCloudinary(c) {
		return
	}

	var body struct {
		Images []string `json:"images" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Images array is required"})
		return
	}

	if len(body.Images) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "At least one image is required"})
		return
	}
	if len(body.Images) > 10 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Maximum 10 facial images allowed"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	results := make([]gin.H, 0, len(body.Images))
	uploadErrors := make([]gin.H, 0)

	for i, img := range body.Images {
		// Ensure data URI prefix
		uploadInput := img
		if !strings.HasPrefix(uploadInput, "data:") {
			imageData := img
			if idx := strings.Index(imageData, ","); idx != -1 {
				imageData = imageData[idx+1:]
			}
			uploadInput = "data:image/png;base64," + imageData
		}

		result, err := h.cloudinary.Upload.Upload(ctx, uploadInput, uploader.UploadParams{
			Folder:         "tsa/facial",
			Transformation: "q_auto,f_auto",
		})
		if err != nil {
			uploadErrors = append(uploadErrors, gin.H{"index": i, "error": "Failed to upload"})
			continue
		}

		results = append(results, gin.H{
			"url":      result.SecureURL,
			"publicId": result.PublicID,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": fmt.Sprintf("Uploaded %d of %d facial images", len(results), len(body.Images)),
		"data": gin.H{
			"uploaded": results,
			"errors":   uploadErrors,
			"total":    len(body.Images),
		},
	})
}

// DeleteImage deletes an image from Cloudinary.
// DELETE /api/upload/:publicId — auth, delete from Cloudinary.
func (h *UploadHandler) DeleteImage(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	if !h.ensureCloudinary(c) {
		return
	}

	publicID := c.Param("publicId")
	if publicID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Public ID is required"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := h.cloudinary.Upload.Destroy(ctx, uploader.DestroyParams{
		PublicID: publicID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to delete image"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Image deleted successfully",
		"data": gin.H{
			"publicId": publicID,
			"result":   result.Result,
		},
	})
}

// GetUploadStats returns Cloudinary usage statistics.
// GET /api/upload/stats — admin, Cloudinary usage.
func (h *UploadHandler) GetUploadStats(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	// Check admin role
	if user.Role != "admin" && user.Role != "super_admin" {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Admin access required"})
		return
	}

	if !h.ensureCloudinary(c) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	usage, err := h.cloudinary.Admin.Usage(ctx, admin.UsageParams{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch upload stats"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Upload stats retrieved successfully",
		"data": gin.H{
			"plan":          usage.Plan,
			"storage":       usage.Storage,
			"bandwidth":     usage.Bandwidth,
			"requests":      usage.Requests,
			"resources":     usage.Resources,
			"transformations": usage.Transformations,
		},
	})
}
