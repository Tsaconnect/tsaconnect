package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The one place a handler decides whether the caller acts with staff powers.
// A web (cookie) session never does (spec §4): the admin console signs in with
// a bearer token, and a cookie that a same-site page can ride must not unlock
// anything beyond the user's own account. Bearer requests are unaffected.

// isAdminUser reports whether u holds an admin role. It says nothing about the
// request: use it about a user being acted on, and hasAdminPrivilege about the
// caller.
func isAdminUser(u *models.User) bool {
	return u != nil && (u.Role == models.RoleAdmin || u.Role == models.RoleSuperAdmin)
}

// hasAdminPrivilege reports whether the caller u may use admin powers on this
// request.
func hasAdminPrivilege(c *gin.Context, u *models.User) bool {
	return !middleware.IsWebSession(c) && isAdminUser(u)
}

// hasSuperAdminPrivilege reports whether the caller u may use super-admin
// powers on this request.
func hasSuperAdminPrivilege(c *gin.Context, u *models.User) bool {
	return !middleware.IsWebSession(c) && u != nil && u.Role == models.RoleSuperAdmin
}

// actingUser is the caller as a service that checks roles itself should see
// it: on a web session, a copy with no staff role.
func actingUser(c *gin.Context, u *models.User) *models.User {
	if u == nil || !middleware.IsWebSession(c) || !isAdminUser(u) {
		return u
	}
	stripped := *u
	stripped.Role = models.RoleUser
	return &stripped
}

// hasStaffPrivilege reports whether the caller u is staff (admin, super_admin
// or support) on this request. AdminAuth also admits marketplace merchants,
// who are not staff.
func hasStaffPrivilege(c *gin.Context, u *models.User) bool {
	return !middleware.IsWebSession(c) && u != nil &&
		(isAdminUser(u) || u.Role == models.RoleSupport)
}
