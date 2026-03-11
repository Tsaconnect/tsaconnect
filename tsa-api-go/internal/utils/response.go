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

// ValidationErrorResponse sends a validation error response with field-level errors.
func ValidationErrorResponse(c *gin.Context, errors interface{}) {
	c.JSON(422, gin.H{
		"success": false,
		"message": "Validation failed",
		"errors":  errors,
	})
}
