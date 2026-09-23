package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TelegramHandler — linking a site account to Telegram from the profile page.
type TelegramHandler struct {
	DB  *pgxpool.Pool
	Bot *TelegramBot // nil when TELEGRAM_BOT_TOKEN is not set
}

// Status — GET /api/telegram
func (h *TelegramHandler) Status(c *gin.Context) {
	if h.Bot == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false})
		return
	}
	userID, role := currentUser(c)

	var username *string
	var digest bool
	err := h.DB.QueryRow(c, `SELECT username, digest FROM telegram_links WHERE user_id = $1`, userID).Scan(&username, &digest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get telegram status"})
		return
	}
	linked := err == nil

	res := gin.H{"enabled": true, "bot": "@" + h.Bot.Username, "linked": linked}
	if linked {
		res["username"] = username
		if role == "owner" || role == "admin" {
			res["digest"] = digest
		}
	}
	c.JSON(http.StatusOK, res)
}

// CreateLink — POST /api/telegram/link
// Returns a one-time t.me link. Opening it sends "/start <code>" to the bot,
// and the bot links the Telegram user to this account. No password in Telegram.
func (h *TelegramHandler) CreateLink(c *gin.Context) {
	if h.Bot == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram bot is not configured: set TELEGRAM_BOT_TOKEN"})
		return
	}
	userID, _ := currentUser(c)

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create link"})
		return
	}
	code := hex.EncodeToString(buf) // 32 characters, allowed in t.me start parameters

	h.DB.Exec(c, `DELETE FROM telegram_link_codes WHERE expires_at < NOW() OR user_id = $1`, userID)
	_, err := h.DB.Exec(c,
		`INSERT INTO telegram_link_codes (code, user_id, expires_at) VALUES ($1, $2, NOW() + interval '15 minutes')`,
		code, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create link"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"url":        "https://t.me/" + h.Bot.Username + "?start=" + code,
		"expires_in": 15 * 60,
	})
}

// Unlink — DELETE /api/telegram/link
func (h *TelegramHandler) Unlink(c *gin.Context) {
	userID, _ := currentUser(c)
	if _, err := h.DB.Exec(c, `DELETE FROM telegram_links WHERE user_id = $1`, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unlink"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"linked": false})
}
