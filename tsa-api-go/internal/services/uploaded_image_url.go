package services

import (
	"net/url"
	"strings"
)

// uploadedImageHost is the host the upload endpoints actually return —
// Cloudinary's SecureURL (see handlers/upload_handler.go). Proofs, chat images
// and dispute evidence render in other users' apps and in the admin console as
// an <img> inside an <a>, so an unscreened URL is a javascript: href in the
// DOM and a request from every viewer's device to a host the poster picked.
// Pinning the host is the only check that prevents the second one.
const uploadedImageHost = "res.cloudinary.com"

// isUploadedImageURL reports whether raw is an https URL on the upload host.
// Hostname() is exact (and drops any :port), so a lookalike such as
// res.cloudinary.com.evil.tld fails where a prefix or suffix test would pass.
func isUploadedImageURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.Hostname() == uploadedImageHost
}
