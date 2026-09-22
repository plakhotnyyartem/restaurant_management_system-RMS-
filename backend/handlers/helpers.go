package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// currentUser returns the id and role that AuthMiddleware put into the context.
// JWT numbers are decoded as float64, so the id is converted back to int.
func currentUser(c *gin.Context) (int, string) {
	var id int
	if value, ok := c.Get("user_id"); ok {
		if f, ok := value.(float64); ok {
			id = int(f)
		}
	}
	role, _ := c.Get("role")
	roleStr, _ := role.(string)
	return id, roleStr
}

// parseID reads the :id path parameter and answers 400 itself when it is invalid.
func parseID(c *gin.Context, what string) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid " + what + " id",
		})
		return 0, false
	}
	return id, true
}

// intQuery reads an integer query parameter, clamped to [min, max].
func intQuery(c *gin.Context, name string, def, min, max int) int {
	value, err := strconv.Atoi(c.Query(name))
	if err != nil {
		return def
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
