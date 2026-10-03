package utils

import (
	"github.com/gin-gonic/gin"
)

// SuccessResponse sends a standard JSON success response.
func SuccessResponse(c *gin.Context, status int, message string, data interface{}) {
	c.JSON(status, gin.H{
		"success": true,
		"message": message,
		"data":    data,
	})
}

// ErrorResponse sends a standard JSON error response.
func ErrorResponse(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"success": false,
		"message": message,
	})
}

// ErrorResponseWithCode sends a standard JSON error response plus a stable,
// snake_case machine code so a client can branch on WHY a request failed
// without matching human-readable message strings.
func ErrorResponseWithCode(c *gin.Context, status int, message string, code string) {
	c.JSON(status, gin.H{
		"success": false,
		"message": message,
		"code":    code,
	})
}

// ErrorResponseWithCodeData is ErrorResponseWithCode plus a data payload —
// for a refusal the client acts on with fresh server data (a ramp price
// that moved carries the new quote).
func ErrorResponseWithCodeData(c *gin.Context, status int, message, code string, data interface{}) {
	c.JSON(status, gin.H{
		"success": false,
		"message": message,
		"code":    code,
		"data":    data,
	})
}

// ValidationErrorResponse sends a validation error response with field-level errors.
func ValidationErrorResponse(c *gin.Context, errors interface{}) {
	c.JSON(422, gin.H{
		"success": false,
		"message": "Validation failed",
		"errors":  errors,
	})
}
