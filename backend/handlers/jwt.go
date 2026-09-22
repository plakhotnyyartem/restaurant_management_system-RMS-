package handlers

import (
	"time"

	"github.com/golang-jwt/jwt/v5"

	"restaurant-management/config"
)

// jwtSecret signs and verifies tokens. It comes from the JWT_SECRET environment
// variable (never from the code) and is set once at startup by InitJWT.
var jwtSecret []byte

// InitJWT sets the signing secret. A short secret can be brute-forced,
// so anything under 32 characters is rejected.
func InitJWT(secret string) error {
	if len(secret) < 32 {
		return config.ErrWeakSecret
	}
	jwtSecret = []byte(secret)
	return nil
}

func GenerateToken(userID int, role string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"role":    role,
		"exp":     time.Now().Add(24 * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString(jwtSecret)
}
