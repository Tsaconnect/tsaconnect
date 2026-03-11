package config

import (
	"os"
)

type Config struct {
	MongoDBURI         string
	JWTSecret          string
	FrontendURL        string
	Port               string
	Env                string
	CloudinaryCloudName string
	CloudinaryAPIKey   string
	CloudinaryAPISecret string
}

func Load() *Config {
	return &Config{
		MongoDBURI:          getEnv("MONGODB_URI", "mongodb://localhost:27017/tsa"),
		JWTSecret:           getEnv("JWT_SECRET", ""),
		FrontendURL:         getEnv("FRONTEND_URL", "http://localhost:3000"),
		Port:                getEnv("PORT", "5000"),
		Env:                 getEnv("NODE_ENV", "development"),
		CloudinaryCloudName: getEnv("CLOUDINARY_CLOUD_NAME", ""),
		CloudinaryAPIKey:    getEnv("CLOUDINARY_API_KEY", ""),
		CloudinaryAPISecret: getEnv("CLOUDINARY_API_SECRET", ""),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
