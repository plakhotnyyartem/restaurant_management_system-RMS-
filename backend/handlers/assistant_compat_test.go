package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// A fake OpenAI-compatible API: first asks for a tool, then answers with the
// tool's result. Checks the loop, the echoed model turn and the tool message.
func TestCompatAnswerToolLoop(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" || r.URL.Path != "/chat/completions" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body)

		if len(requests) == 1 {
			w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_summary","arguments":"{\"days\":7}"},
				"extra_content":{"google":{"thought_signature":"sig"}}}]}}]}`))
			return
		}
		msgs := body["messages"].([]any)
		tool := msgs[len(msgs)-1].(map[string]any)
		content, _ := json.Marshal("Выручка: " + tool["content"].(string))
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":` +
			string(content) + `}}]}`))
	}))
	defer srv.Close()
	t.Setenv("AI_API_KEY", "test-key")
	t.Setenv("AI_BASE_URL", srv.URL)

	summary, err := toolrunner.NewBetaToolFromJSONSchema("get_summary", "KPI",
		func(ctx context.Context, in daysInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			return jsonResult(map[string]int{"days": in.Days}, nil)
		})
	if err != nil {
		t.Fatal(err)
	}

	h := &AssistantHandler{}
	reply, err := h.compatAnswer(context.Background(),
		[]assistantMessage{{Role: "user", Content: "сколько заработали?"}}, []anthropic.BetaTool{summary})
	if err != nil {
		t.Fatal(err)
	}
	if reply != `Выручка: {"days":7}` {
		t.Fatalf("reply = %q", reply)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}

	// The tool schema reaches the API without JSON Schema meta keys.
	fn := requests[0]["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	params, _ := json.Marshal(fn["parameters"])
	if strings.Contains(string(params), "$schema") || !strings.Contains(string(params), `"days"`) {
		t.Fatalf("parameters = %s", params)
	}
	// The model's turn goes back unchanged, with Gemini's thought signature.
	second, _ := json.Marshal(requests[1]["messages"])
	if !strings.Contains(string(second), `"thought_signature":"sig"`) || !strings.Contains(string(second), `"tool_call_id":"call_1"`) {
		t.Fatalf("second request messages = %s", second)
	}
}

func TestAssistantErrorStatusCompat(t *testing.T) {
	code, _ := assistantErrorStatus(&compatError{StatusCode: 400, Body: `{"error":{"details":[{"reason":"API_KEY_INVALID"}]}}`})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("invalid key: code = %d", code)
	}
	if code, _ := assistantErrorStatus(&compatError{StatusCode: 429}); code != http.StatusTooManyRequests {
		t.Fatalf("rate limit: code = %d", code)
	}
}
