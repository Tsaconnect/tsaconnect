package middleware

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/cloudinary/cloudinary-go/v2"
	"github.com/cloudinary/cloudinary-go/v2/api/uploader"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// UploadResult holds the result of a Cloudinary upload.
type UploadResult struct {
	URL      string `json:"url"`
	PublicID string `json:"publicId"`
	Format   string `json:"format"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// DeleteFromCloudinary deletes a resource from Cloudinary by its public ID.
func DeleteFromCloudinary(cfg *config.Config, publicID string) error {
	cld, err := cloudinary.NewFromParams(
		cfg.CloudinaryCloudName,
		cfg.CloudinaryAPIKey,
		cfg.CloudinaryAPISecret,
	)
	if err != nil {
		return fmt.Errorf("failed to initialize Cloudinary: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err = cld.Upload.Destroy(ctx, uploader.DestroyParams{PublicID: publicID})
	if err != nil {
		return fmt.Errorf("failed to delete from Cloudinary: %w", err)
	}

	return nil
}

// UploadToCloudinary uploads file data to Cloudinary and returns the upload result.
func UploadToCloudinary(cfg *config.Config, fileData []byte, folder string) (*UploadResult, error) {
	cld, err := cloudinary.NewFromParams(
		cfg.CloudinaryCloudName,
		cfg.CloudinaryAPIKey,
		cfg.CloudinaryAPISecret,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Cloudinary: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	uploadParams := uploader.UploadParams{
		Folder: folder,
	}

	result, err := cld.Upload.Upload(ctx, bytes.NewReader(fileData), uploadParams)
	if err != nil {
		return nil, fmt.Errorf("failed to upload to Cloudinary: %w", err)
	}

	return &UploadResult{
		URL:      result.SecureURL,
		PublicID: result.PublicID,
		Format:   result.Format,
		Width:    result.Width,
		Height:   result.Height,
	}, nil
}
