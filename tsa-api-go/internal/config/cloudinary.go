package config

import (
	"fmt"
	"log"

	"github.com/cloudinary/cloudinary-go/v2"
)

var cloudinaryClient *cloudinary.Cloudinary

// InitCloudinary initializes the Cloudinary client from the given config.
func InitCloudinary(cfg *Config) error {
	if cfg.CloudinaryCloudName == "" || cfg.CloudinaryAPIKey == "" || cfg.CloudinaryAPISecret == "" {
		log.Println("Warning: Cloudinary credentials not fully configured")
		return nil
	}

	cld, err := cloudinary.NewFromParams(cfg.CloudinaryCloudName, cfg.CloudinaryAPIKey, cfg.CloudinaryAPISecret)
	if err != nil {
		return fmt.Errorf("failed to initialize Cloudinary: %w", err)
	}

	cloudinaryClient = cld
	log.Println("Cloudinary initialized successfully")
	return nil
}

// GetCloudinary returns the initialized Cloudinary client.
func GetCloudinary() *cloudinary.Cloudinary {
	return cloudinaryClient
}
