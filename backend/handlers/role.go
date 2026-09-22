package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userRole, exists := c.Get("role")

		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "role not found",
			})
			c.Abort()
			return
		}

		role, ok := userRole.(string)

		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "invalid role",
			})
			c.Abort()
			return
		}

		for _, allowedRole := range roles {
			if role == allowedRole {
				c.Next()
				return
			}
		}

		c.JSON(http.StatusForbidden, gin.H{
			"error": "access denied",
		})
		c.Abort()
	}
}
