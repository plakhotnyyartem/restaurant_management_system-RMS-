package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)


type AuthHandler struct {
	DB *pgxpool.Pool
}

type RegisterRequest struct {
	Name     string `json:"name" binding:"required"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req RegisterRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid request",
		})
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword(
		[]byte(req.Password),
		bcrypt.DefaultCost,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to hash password",
		})
		return
	}

	var userID int

	err = h.DB.QueryRow(
		c,
		`INSERT INTO users (name, email, password_hash)
		 VALUES ($1, $2, $3)
		 RETURNING id`,
		req.Name,
		req.Email,
		string(passwordHash),
	).Scan(&userID)

	if err != nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": "email already exists",
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "user registered successfully",
		"user": gin.H{
			"id":    userID,
			"name":  req.Name,
			"email": req.Email,
			"role":  "customer",
		},
	})
}


type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid request",
		})
		return
	}

	var (
		id           int
		name         string
		email        string
		passwordHash string
		role         string
	)

	err := h.DB.QueryRow(
		c,
		`SELECT id, name, email, password_hash, role
		 FROM users
		 WHERE email = $1`,
		req.Email,
	).Scan(&id, &name, &email, &passwordHash, &role)

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "invalid email or password",
		})
		return
	}

	err = bcrypt.CompareHashAndPassword(
		[]byte(passwordHash),
		[]byte(req.Password),
	)

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "invalid email or password",
		})
		return
	}

token, err := GenerateToken(id, role)
if err != nil {
	c.JSON(http.StatusInternalServerError, gin.H{
		"error": "failed to generate token",
	})
	return
}

c.JSON(http.StatusOK, gin.H{
	"message": "login successful",
	"token":   token,
	"user": gin.H{
		"id":    id,
		"name":  name,
		"email": email,
		"role":  role,
	},
})
}