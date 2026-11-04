package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// ---------- Free alternative: any OpenAI-compatible chat API ----------
//
// Without ANTHROPIC_API_KEY the assistant can use any service that speaks the
// OpenAI /chat/completions format with tool calls. By default it is Google
// Gemini, which has a free tier (key: aistudio.google.com → Get API key).
// Groq, OpenRouter and others work too — set AI_BASE_URL and AI_MODEL
// (several models separated by commas: the next one is a fallback).
//
// The tools are the very same anthropic.BetaTool values: they only need a
// name, a JSON schema and Execute(), so no analytics code is duplicated.

const (
	compatDefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	// Google's aliases for the newest Flash models. The free tier is often
	// "experiencing high demand" (503): then the next model in the list is tried.
	compatDefaultModels = "gemini-flash-latest,gemini-flash-lite-latest"
	compatMaxIterations = 8
	compatFallbackAfter = 25 * time.Second
	compatBusyPause     = 5 * time.Minute
)

var compatHTTP = &http.Client{Timeout: 2 * time.Minute}

func compatKey() string { return os.Getenv("AI_API_KEY") }

func compatSetting(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// compatError is a non-2xx answer of the compatible API.
type compatError struct {
	StatusCode int
	Body       string
}

func (e *compatError) Error() string {
	return fmt.Sprintf("ai api: HTTP %d: %s", e.StatusCode, e.Body)
}

type compatToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type compatResponse struct {
	Choices []struct {
		FinishReason string          `json:"finish_reason"`
		Message      json.RawMessage `json:"message"`
	} `json:"choices"`
}

// compatSchema turns a tool's input schema into OpenAI "parameters".
// Gemini rejects JSON Schema meta keys, so only the plain parts are kept;
// nil means the tool takes no arguments.
func compatSchema(t anthropic.BetaTool) (map[string]any, error) {
	data, err := json.Marshal(t.InputSchema())
	if err != nil {
		return nil, err
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	props, _ := s["properties"].(map[string]any)
	if len(props) == 0 {
		return nil, nil
	}
	out := map[string]any{"type": "object", "properties": props}
	if req, ok := s["required"]; ok {
		out["required"] = req
	}
	return out, nil
}

// overloaded: the provider is busy right now, another try or model may work.
func (e *compatError) overloaded() bool {
	return e.StatusCode == http.StatusServiceUnavailable || e.StatusCode == http.StatusInternalServerError
}

// compatChatRetry sends one chat request, falling back to the next model when
// the provider is overloaded. A busy free model can take over a minute to
// answer 503, so every model but the last gets only compatFallbackAfter and is
// then skipped for compatBusyPause — the next questions go straight to a model
// that answers.
func (h *AssistantHandler) compatChatRetry(ctx context.Context, models []string, body map[string]any) (*compatResponse, error) {
	var lastErr error
	for i, model := range models {
		if i < len(models)-1 && h.modelBusy(model) {
			continue
		}
		body["model"] = model
		attemptCtx, cancel := ctx, context.CancelFunc(func() {})
		if i < len(models)-1 {
			attemptCtx, cancel = context.WithTimeout(ctx, compatFallbackAfter)
		}
		resp, err := h.compatChat(attemptCtx, body)
		cancel()
		var ce *compatError
		slow := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
		if err == nil || !(slow || errors.As(err, &ce) && ce.overloaded()) {
			return resp, err
		}
		h.markBusy(model)
		lastErr = err
	}
	return nil, lastErr
}

func (h *AssistantHandler) modelBusy(model string) bool {
	h.busyMu.Lock()
	defer h.busyMu.Unlock()
	return time.Now().Before(h.busyUntil[model])
}

func (h *AssistantHandler) markBusy(model string) {
	h.busyMu.Lock()
	defer h.busyMu.Unlock()
	if h.busyUntil == nil {
		h.busyUntil = map[string]time.Time{}
	}
	h.busyUntil[model] = time.Now().Add(compatBusyPause)
}

func (h *AssistantHandler) compatChat(ctx context.Context, body map[string]any) (*compatResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(compatSetting("AI_BASE_URL", compatDefaultBaseURL), "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+compatKey())

	resp, err := compatHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &compatError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	var out compatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("ai api: empty answer")
	}
	return &out, nil
}

// compatAnswer is answer() for the compatible API: a hand-written tool loop
// (model → tool calls → results → model …) instead of the SDK's tool runner.
func (h *AssistantHandler) compatAnswer(ctx context.Context, messages []assistantMessage, tools []anthropic.BetaTool) (string, error) {
	byName := map[string]anthropic.BetaTool{}
	var specs []map[string]any
	for _, t := range tools {
		byName[t.Name()] = t
		fn := map[string]any{"name": t.Name(), "description": t.Description()}
		params, err := compatSchema(t)
		if err != nil {
			return "", err
		}
		if params != nil {
			fn["parameters"] = params
		}
		specs = append(specs, map[string]any{"type": "function", "function": fn})
	}

	// Messages are kept as raw JSON: the model's own turns are sent back exactly
	// as received, so provider extras (Gemini's thought signatures) survive.
	history := []any{map[string]string{
		"role":    "system",
		"content": assistantSystem + "\nСегодня: " + time.Now().Format("02.01.2006") + ".",
	}}
	for _, m := range messages {
		history = append(history, map[string]string{"role": m.Role, "content": m.Content})
	}

	var models []string
	for _, m := range strings.Split(compatSetting("AI_MODEL", compatDefaultModels), ",") {
		if m = strings.TrimSpace(m); m != "" {
			models = append(models, m)
		}
	}
	for range compatMaxIterations {
		resp, err := h.compatChatRetry(ctx, models, map[string]any{"messages": history, "tools": specs})
		if err != nil {
			return "", err
		}
		raw := resp.Choices[0].Message
		var msg struct {
			Content   string           `json:"content"`
			ToolCalls []compatToolCall `json:"tool_calls"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			return "", err
		}
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		history = append(history, raw)
		for _, call := range msg.ToolCalls {
			history = append(history, map[string]string{
				"role":         "tool",
				"tool_call_id": call.ID,
				"content":      runCompatTool(ctx, byName[call.Function.Name], call.Function.Arguments),
			})
		}
	}
	return "(Ответ получился слишком длинным по шагам — уточните вопрос.)", nil
}

// runCompatTool executes one tool call; errors go back to the model as text,
// so it can apologise or try other arguments instead of failing the request.
func runCompatTool(ctx context.Context, tool anthropic.BetaTool, args string) string {
	if tool == nil {
		return `{"error":"unknown tool"}`
	}
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	blocks, err := tool.Execute(ctx, json.RawMessage(args))
	if err != nil {
		data, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(data)
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.OfText != nil {
			sb.WriteString(b.OfText.Text)
		}
	}
	return sb.String()
}
