package middleware

import (
	"log"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// Recover turns a handler panic into a 500 with the standard error envelope.
// It logs only what locates the fault — method, route, the signed-in user's id,
// the panic value and the stack — and never the request itself: gin's own
// Recovery (gin.Default, gin.CustomRecovery) dumps every header, the web
// session Cookie included, into the log.
//
// http.ErrAbortHandler is passed on, not recovered: a handler panics with it to
// cut a response it has already started (a CSV export failing part-way), and
// only net/http can abort the connection so the client sees the body end early
// instead of a clean, complete-looking end. net/http logs no stack for it.
func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			user := "-"
			if u := userFromContext(c); u != nil {
				user = u.ID.String()
			}
			log.Printf("[panic] %s %s user=%s: %v\n%s", c.Request.Method, requestRoute(c), user, v, debug.Stack())
			if c.Writer.Written() {
				c.Abort()
				return
			}
			utils.ErrorResponse(c, http.StatusInternalServerError, "Internal server error")
			c.Abort()
		}()
		c.Next()
	}
}

// requestRoute is the matched route pattern (":id", not the id), or the path
// when no route matched — never the query string, which can carry a token.
func requestRoute(c *gin.Context) string {
	if route := c.FullPath(); route != "" {
		return route
	}
	return c.Request.URL.Path
}
