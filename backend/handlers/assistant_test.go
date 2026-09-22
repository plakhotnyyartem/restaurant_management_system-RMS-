package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"restaurant-management/database"
)

// TestAssistantTools runs every assistant tool against the local database the
// same way the SDK tool runner does (Execute with raw JSON input) and checks
// that each one returns valid JSON. It is skipped when PostgreSQL is not running.
func TestAssistantTools(t *testing.T) {
	db, err := database.Connect()
	if err != nil {
		t.Skip("database is not available: ", err)
	}
	defer db.Close()

	analytics := &AnalyticsHandler{DB: db}
	h := &AssistantHandler{Analytics: analytics, Inventory: &InventoryHandler{DB: db, Analytics: analytics}}

	var used []string
	tools, err := h.tools(&used)
	if err != nil {
		t.Fatal(err)
	}

	inputs := map[string]string{
		"get_summary":          `{"days": 7}`,
		"get_menu_engineering": `{"days": 30}`,
		"get_forecast":         `{}`,
		"get_purchase_plan":    `{"budget": 300000, "safety": 10}`,
		"get_basket_pairs":     `{"days": 90, "limit": 3}`,
		"get_load_heatmap":     `{}`, // missing fields fall back to defaults
		"get_recommendations":  `{"days": 30}`,
	}
	if len(tools) != len(inputs) {
		t.Fatalf("got %d tools, want %d", len(tools), len(inputs))
	}

	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			if tool.Description() == "" || toolTitles[tool.Name()] == "" {
				t.Fatal("tool needs a description and a UI title")
			}
			input, ok := inputs[tool.Name()]
			if !ok {
				t.Fatalf("no test input for %s", tool.Name())
			}
			blocks, err := tool.Execute(context.Background(), json.RawMessage(input))
			if err != nil {
				t.Fatal(err)
			}
			if len(blocks) != 1 || blocks[0].OfText == nil {
				t.Fatalf("want one text block, got %+v", blocks)
			}
			text := blocks[0].OfText.Text
			if !json.Valid([]byte(text)) {
				t.Fatalf("result is not JSON: %.200s", text)
			}
			// A comma inside a jsonschema tag silently cuts the description.
			schema, _ := json.Marshal(tool.InputSchema())
			if strings.Contains(string(schema), `— бюджет"`) {
				t.Errorf("description was cut by a comma: %s", schema)
			}
			// Tool results go into the model's context: keep them compact.
			if len(text) > 40_000 {
				t.Errorf("result is %d bytes — too large for the context", len(text))
			}
		})
	}

	if len(used) != len(inputs) {
		t.Errorf("tracked %d tool calls, want %d", len(used), len(inputs))
	}
}

// TestAssistantNotConfigured: without an API key the endpoint must answer 503
// with a clear message instead of trying to call the API.
func TestAssistantNotConfigured(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	gin.SetMode(gin.TestMode)

	h := &AssistantHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/admin/assistant",
		strings.NewReader(`{"messages":[{"role":"user","content":"Привет"}]}`))
	h.Ask(c)

	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "ANTHROPIC_API_KEY") {
		t.Fatalf("got %d %s, want 503 mentioning ANTHROPIC_API_KEY", w.Code, w.Body.String())
	}
}

func TestAssistantValidation(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key-not-used")
	gin.SetMode(gin.TestMode)

	cases := map[string]string{
		"empty history":       `{"messages":[]}`,
		"unknown role":        `{"messages":[{"role":"system","content":"x"}]}`,
		"last is assistant":   `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`,
		"message is too long": `{"messages":[{"role":"user","content":"` + strings.Repeat("я", 4001) + `"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h := &AssistantHandler{}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/admin/assistant", strings.NewReader(body))
			h.Ask(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d %s, want 400", w.Code, w.Body.String())
			}
		})
	}
}
