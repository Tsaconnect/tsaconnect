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
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// getCloudinary creates a Cloudinary client from the handler config.
func (h *Handlers) getCloudinary() *cloudinary.Cloudinary {
	cld, err := cloudinary.NewFromParams(
		h.Config.CloudinaryCloudName,
		h.Config.CloudinaryAPIKey,
		h.Config.CloudinaryAPISecret,
	)
	if err != nil {
		return nil
	}
	return cld
}

func (h *Handlers) ensureCloudinary(c *gin.Context) *cloudinary.Cloudinary {
	cld := h.getCloudinary()
	if cld == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "Cloudinary is not configured",
		})
		return nil
	}
	return cld
}

// UploadFile uploads a single image file.
// POST /api/upload/ — auth, multipart 'image', optimize, upload to Cloudinary.
func (h *Handlers) UploadFile(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	cld := h.ensureCloudinary(c)
	if cld == nil {
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

	result, err := cld.Upload.Upload(ctx, f, uploader.UploadParams{
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
func (h *Handlers) UploadMultiple(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	cld := h.ensureCloudinary(c)
	if cld == nil {
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
	uploadErrors := make([]gin.H, 0)

	for i, file := range files {
		f, err := file.Open()
		if err != nil {
			uploadErrors = append(uploadErrors, gin.H{"index": i, "error": "Failed to read file"})
			continue
		}

		result, err := cld.Upload.Upload(ctx, f, uploader.UploadParams{
			Folder:         "tsa",
			Transformation: "q_auto,f_auto",
		})
		f.Close()

		if err != nil {
			uploadErrors = append(uploadErrors, gin.H{"index": i, "error": "Failed to upload"})
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
			"errors":   uploadErrors,
			"total":    len(files),
		},
	})
}

// UploadBase64 uploads a base64-encoded image.
// POST /api/upload/base64 — validate base64, upload to Cloudinary.
func (h *Handlers) UploadBase64(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	cld := h.ensureCloudinary(c)
	if cld == nil {
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

	uploadInput := body.Image
	if !strings.HasPrefix(uploadInput, "data:") {
		uploadInput = "data:image/png;base64," + imageData
	}

	result, err := cld.Upload.Upload(ctx, uploadInput, uploader.UploadParams{
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
func (h *Handlers) UploadFacial(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	cld := h.ensureCloudinary(c)
	if cld == nil {
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
		uploadInput := img
		if !strings.HasPrefix(uploadInput, "data:") {
			imageData := img
			if idx := strings.Index(imageData, ","); idx != -1 {
				imageData = imageData[idx+1:]
			}
			uploadInput = "data:image/png;base64," + imageData
		}

		result, err := cld.Upload.Upload(ctx, uploadInput, uploader.UploadParams{
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
func (h *Handlers) DeleteImage(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	cld := h.ensureCloudinary(c)
	if cld == nil {
		return
	}

	publicID := c.Param("publicId")
	if publicID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Public ID is required"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := cld.Upload.Destroy(ctx, uploader.DestroyParams{
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
func (h *Handlers) GetUploadStats(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Admin access required"})
		return
	}

	cld := h.ensureCloudinary(c)
	if cld == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	usage, err := cld.Admin.Usage(ctx, admin.UsageParams{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch upload stats"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Upload stats retrieved successfully",
		"data": gin.H{
			"plan":            usage.Plan,
			"storage":         usage.Storage,
			"bandwidth":       usage.Bandwidth,
			"requests":        usage.Requests,
			"resources":       usage.Resources,
			"transformations": usage.Transformations,
		},
	})
}
