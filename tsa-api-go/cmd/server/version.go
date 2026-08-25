package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// buildCommit is the git SHA this binary was built from, injected at build time:
//
//	go build -ldflags "-X main.buildCommit=$(git rev-parse --short HEAD)"
//
// The deploy script asserts this value over /health to confirm the release it just
// activated is the one actually answering. Without it, the health check passes for
// any process listening on the port — including the previous release that never
// restarted.
var buildCommit = "unknown"

// healthHandler builds the /health handler. ping is injected so the failure path is
// testable without a database.
func healthHandler(ping func() error, commit string) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := http.StatusOK
		dbStatus := "connected"
		if err := ping(); err != nil {
			// 503, not 200. This endpoint gates every deploy: returning OK with a
			// dead database means a bad DATABASE_URL or a Postgres still starting
			// after reboot passes the gate, and the rollback never fires.
			status = http.StatusServiceUnavailable
			dbStatus = "disconnected"
		}
		c.JSON(status, gin.H{
			"status":   map[bool]string{true: "ok", false: "degraded"}[status == http.StatusOK],
			"message":  "TSA API is running",
			"database": dbStatus,
			"commit":   commit,
		})
	}
}
