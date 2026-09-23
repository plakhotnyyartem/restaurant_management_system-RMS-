// Package telegram is a small client for the Telegram Bot API
// (https://core.telegram.org/bots/api): only the methods the bot needs.
// It uses plain net/http, and the API address can be replaced in tests.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const DefaultAPIURL = "https://api.telegram.org"

type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

func NewClient(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultAPIURL
	}
	return &Client{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		// Long polling keeps a request open for up to 30 s, so the timeout is longer.
		http: &http.Client{Timeout: 45 * time.Second},
	}
}

// ---------- API types (only the fields we use) ----------

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private | group | supergroup | channel
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

// APIError is an error answered by Telegram itself (wrong token, blocked bot …).
type APIError struct {
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram: %d %s", e.Code, e.Description)
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	url := c.baseURL + "/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// Never include the URL: it contains the bot token.
		return fmt.Errorf("telegram %s: request failed", method)
	}
	defer resp.Body.Close()

	var api apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&api); err != nil {
		return fmt.Errorf("telegram %s: bad response (HTTP %d)", method, resp.StatusCode)
	}
	if !api.OK {
		return &APIError{Code: api.ErrorCode, Description: api.Description}
	}
	if result != nil {
		return json.Unmarshal(api.Result, result)
	}
	return nil
}

// GetMe returns the bot itself (we need its @username for t.me links).
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var me User
	err := c.call(ctx, "getMe", map[string]any{}, &me)
	return me, err
}

// GetUpdates waits up to timeout seconds for new messages (long polling).
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

// SendMessage sends an HTML-formatted message.
func (c *Client) SendMessage(ctx context.Context, chatID int64, html string) error {
	return c.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}, nil)
}

// SendTyping shows "typing…" in the chat while a slow answer is being prepared.
func (c *Client) SendTyping(ctx context.Context, chatID int64) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, nil)
}

// SetCommands fills the command menu shown next to the input field.
func (c *Client) SetCommands(ctx context.Context, commands map[string]string, order []string) error {
	list := make([]map[string]string, 0, len(order))
	for _, name := range order {
		list = append(list, map[string]string{"command": name, "description": commands[name]})
	}
	return c.call(ctx, "setMyCommands", map[string]any{"commands": list}, nil)
}

// Escape makes text safe inside an HTML-formatted message.
func Escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
