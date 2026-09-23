package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
	"github.com/gin-gonic/gin"
)

// ---------- AI assistant for the owner ----------
//
// The language model never calculates anything itself. It gets tools that call
// exactly the same Go functions as the analytics pages (forecast, menu
// engineering, purchasing LP …), reads their JSON and explains it in plain
// Russian — so every number in its answer matches the charts.

const assistantModel = "claude-opus-5"

type AssistantHandler struct {
	Analytics *AnalyticsHandler
	Inventory *InventoryHandler

	once   sync.Once
	client anthropic.Client
}

// configured reports whether an API key is available. Without it the endpoint
// answers 503 instead of failing on every request.
func (h *AssistantHandler) configured() bool {
	return os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != ""
}

func (h *AssistantHandler) api() *anthropic.Client {
	h.once.Do(func() { h.client = anthropic.NewClient() }) // reads ANTHROPIC_API_KEY
	return &h.client
}

const assistantSystem = `Ты — ИИ-ассистент владельца ресторана в системе RMS (Restaurant Management System).
Отвечаешь по-русски, коротко и по делу, как опытный управляющий рестораном.

Правила:
- Любые цифры (выручка, заказы, прогноз, маржа, закупки) бери только из инструментов. Ничего не придумывай и не пересчитывай вручную; если данных нет — так и скажи.
- Называй период, за который приводишь цифры. Деньги пиши в тенге с пробелами между разрядами: 1 250 000 ₸.
- Если вопрос про рекламу, цены или меню — сначала посмотри инженерию меню; про «что будет» — прогноз; про закупки и бюджет — план закупок.
- Объясняй методы простыми словами, если спрашивают «почему» (Холт — Уинтерс, матрица Kasavana & Smith, симплекс-метод, теневая цена).
- Тексты для рекламы пиши живо и без канцелярита: 2–3 варианта, до 350 символов каждый, с эмодзи, опирайся на реальные блюда и цены из данных.
- Заканчивай ответ конкретным действием, которое владелец может сделать сегодня.
- Форматирование: короткие абзацы, списки через «- », выделение через **жирный**. Без таблиц и заголовков.`

// ---------- Tools ----------

type daysInput struct {
	Days int `json:"days" jsonschema:"description=Период анализа в днях (1–365). По умолчанию 30."`
}

type pairsInput struct {
	Days  int `json:"days" jsonschema:"description=Период в днях (по умолчанию 90)"`
	Limit int `json:"limit" jsonschema:"description=Сколько пар вернуть (по умолчанию 10)"`
}

type planInput struct {
	// No commas inside jsonschema descriptions: the tag uses commas as separators.
	Budget int `json:"budget" jsonschema:"description=Бюджет закупки на неделю в тенге. 0 — бюджет на весь прогноз."`
	Safety int `json:"safety" jsonschema:"description=Страховой запас сверх прогноза в процентах (0–50). По умолчанию 10."`
}

type heatInput struct {
	Days int `json:"days" jsonschema:"description=Период анализа в днях (1–365). По умолчанию 56 (8 недель)."`
}

type emptyInput struct{}

func clampDays(d, def int) int {
	if d <= 0 {
		return def
	}
	if d > 365 {
		return 365
	}
	return d
}

// jsonResult turns any value into a text tool result.
func jsonResult(v any, err error) (anthropic.BetaToolResultBlockParamContentUnion, error) {
	if err != nil {
		return anthropic.BetaToolResultBlockParamContentUnion{}, err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return anthropic.BetaToolResultBlockParamContentUnion{}, err
	}
	return anthropic.BetaToolResultBlockParamContentUnion{
		OfText: &anthropic.BetaTextBlockParam{Text: string(data)},
	}, nil
}

// tools builds the tool set for one request; used collects which tools the
// model called, so the UI can show where the numbers came from.
func (h *AssistantHandler) tools(used *[]string) ([]anthropic.BetaTool, error) {
	var mu sync.Mutex
	track := func(name string) {
		mu.Lock()
		*used = append(*used, name)
		mu.Unlock()
	}

	type spec struct {
		tool anthropic.BetaTool
		err  error
	}
	var specs []spec
	add := func(t anthropic.BetaTool, err error) { specs = append(specs, spec{t, err}) }

	add(toolrunner.NewBetaToolFromJSONSchema("get_summary",
		"KPI за период и сравнение с предыдущим периодом той же длины: заказы, выручка, валовая прибыль, средний чек, отмены.",
		func(ctx context.Context, in daysInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			track("get_summary")
			days := clampDays(in.Days, 30)
			cur, err := h.Analytics.stats(ctx, days, 0)
			if err != nil {
				return jsonResult(nil, err)
			}
			prev, err := h.Analytics.stats(ctx, days*2, days)
			return jsonResult(gin.H{"days": days, "current": cur, "previous": prev}, err)
		}))

	add(toolrunner.NewBetaToolFromJSONSchema("get_menu_engineering",
		"Инженерия меню (Kasavana & Smith): для каждого блюда продажи, доля, маржа с порции, класс star/plowhorse/puzzle/dog и совет.",
		func(ctx context.Context, in daysInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			track("get_menu_engineering")
			return jsonResult(h.Analytics.menuEngineering(ctx, clampDays(in.Days, 30)))
		}))

	add(toolrunner.NewBetaToolFromJSONSchema("get_forecast",
		"Прогноз спроса на 14 дней (Холт — Уинтерс): заказы и выручка по дням с интервалом 80%, точность модели, прогноз порций по каждому блюду на завтра и неделю.",
		func(ctx context.Context, _ emptyInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			track("get_forecast")
			fc, err := h.Analytics.forecast(ctx, 14)
			fc.History = nil // the model does not need 42 days of raw history
			return jsonResult(fc, err)
		}))

	add(toolrunner.NewBetaToolFromJSONSchema("get_purchase_plan",
		"Оптимальный план закупки продуктов на неделю (линейное программирование): что купить, сколько блюд удастся обеспечить, теневая цена бюджета и кривая «бюджет → прибыль».",
		func(ctx context.Context, in planInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			track("get_purchase_plan")
			safety := in.Safety
			if safety <= 0 || safety > 50 {
				safety = 10
			}
			return jsonResult(h.Inventory.buildPlan(ctx, float64(max(in.Budget, 0)), safety))
		}))

	add(toolrunner.NewBetaToolFromJSONSchema("get_basket_pairs",
		"Анализ корзин: какие блюда заказывают вместе (support, confidence, lift). Основа для комбо-наборов.",
		func(ctx context.Context, in pairsInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			track("get_basket_pairs")
			limit := in.Limit
			if limit <= 0 || limit > 30 {
				limit = 10
			}
			return jsonResult(h.Analytics.pairs(ctx, clampDays(in.Days, 90), limit))
		}))

	add(toolrunner.NewBetaToolFromJSONSchema("get_load_heatmap",
		"Средняя загрузка ресторана по дню недели (1 = пн … 7 = вс) и часу: заказов в час. Для графика смен.",
		func(ctx context.Context, in heatInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			track("get_load_heatmap")
			return jsonResult(h.Analytics.heatmap(ctx, clampDays(in.Days, 56)))
		}))

	add(toolrunner.NewBetaToolFromJSONSchema("get_recommendations",
		"Готовые рекомендации системы: что рекламировать, где поднять цену, что убрать, какое комбо, когда усилить смену, прогноз на завтра.",
		func(ctx context.Context, in daysInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			track("get_recommendations")
			return jsonResult(h.Analytics.recommendations(ctx, clampDays(in.Days, 30)))
		}))

	tools := make([]anthropic.BetaTool, 0, len(specs))
	for _, s := range specs {
		if s.err != nil {
			return nil, s.err
		}
		tools = append(tools, s.tool)
	}
	return tools, nil
}

// ---------- Endpoint ----------

type assistantMessage struct {
	Role    string `json:"role" binding:"required,oneof=user assistant"`
	Content string `json:"content" binding:"required,max=4000"`
}

type AssistantRequest struct {
	Messages []assistantMessage `json:"messages" binding:"required,min=1,max=30,dive"`
}

var toolTitles = map[string]string{
	"get_summary":          "KPI",
	"get_menu_engineering": "инженерия меню",
	"get_forecast":         "прогноз спроса",
	"get_purchase_plan":    "план закупок",
	"get_basket_pairs":     "анализ корзин",
	"get_load_heatmap":     "загрузка по часам",
	"get_recommendations":  "рекомендации",
}

var errAssistantNotConfigured = errors.New("assistant is not configured")

const refusalReply = "Не могу помочь с этим запросом. Спросите про продажи, меню, прогноз или закупки ресторана."

// answer runs the model with the tools and returns the reply text and the
// titles of the tools it used. Shared by the web chat and the Telegram bot.
func (h *AssistantHandler) answer(ctx context.Context, messages []assistantMessage) (string, []string, error) {
	if !h.configured() {
		return "", nil, errAssistantNotConfigured
	}

	var history []anthropic.BetaMessageParam
	for _, m := range messages {
		block := anthropic.NewBetaTextBlock(m.Content)
		if m.Role == "user" {
			history = append(history, anthropic.NewBetaUserMessage(block))
		} else {
			history = append(history, anthropic.BetaMessageParam{
				Role:    anthropic.BetaMessageParamRoleAssistant,
				Content: []anthropic.BetaContentBlockParamUnion{block},
			})
		}
	}

	var used []string
	tools, err := h.tools(&used)
	if err != nil {
		return "", nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	today := time.Now().Format("02.01.2006")
	runner := h.api().Beta.Messages.NewToolRunner(tools, anthropic.BetaToolRunnerParams{
		BetaMessageNewParams: anthropic.BetaMessageNewParams{
			Model:     assistantModel,
			MaxTokens: 16000,
			System: []anthropic.BetaTextBlockParam{
				{Text: assistantSystem},
				{Text: "Сегодня: " + today + "."},
			},
			Messages: history,
			// Chat answers do not need maximum reasoning depth: medium is faster and cheaper.
			OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
			// If the model declines a request, the API re-serves it with a fallback model.
			Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
			Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		},
		MaxIterations: 8, // at most 8 model calls per question
	})

	message, err := runner.RunToCompletion(ctx)
	if err != nil {
		return "", nil, err
	}
	if message.StopReason == anthropic.BetaStopReasonRefusal {
		return refusalReply, []string{}, nil
	}

	var reply strings.Builder
	for _, block := range message.Content {
		if text, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			reply.WriteString(text.Text)
		}
	}
	if message.StopReason == anthropic.BetaStopReasonToolUse {
		reply.WriteString("\n\n(Ответ получился слишком длинным по шагам — уточните вопрос.)")
	}

	// Unique tool titles in call order.
	seen := map[string]bool{}
	titles := []string{}
	for _, name := range used {
		if !seen[name] {
			seen[name] = true
			titles = append(titles, toolTitles[name])
		}
	}
	return strings.TrimSpace(reply.String()), titles, nil
}

// assistantErrorStatus turns an API error into a short human message (for any client).
func assistantErrorStatus(err error) (int, string) {
	var apiErr *anthropic.Error
	switch {
	case errors.Is(err, errAssistantNotConfigured):
		return http.StatusServiceUnavailable, "assistant is not configured: set ANTHROPIC_API_KEY for the server"
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized:
		return http.StatusServiceUnavailable, "assistant API key is invalid"
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests:
		return http.StatusTooManyRequests, "assistant is busy, try again in a minute"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "assistant took too long to answer"
	default:
		return http.StatusBadGateway, "assistant request failed"
	}
}

// Ask — POST /api/admin/assistant
// The client keeps the chat history and sends it every time (the server stays stateless).
func (h *AssistantHandler) Ask(c *gin.Context) {
	if !h.configured() {
		code, msg := assistantErrorStatus(errAssistantNotConfigured)
		c.JSON(code, gin.H{"error": msg})
		return
	}

	var req AssistantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "messages with role user/assistant and content up to 4000 characters are required"})
		return
	}
	if req.Messages[len(req.Messages)-1].Role != "user" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the last message must be from the user"})
		return
	}

	reply, titles, err := h.answer(c, req.Messages)
	if err != nil {
		code, msg := assistantErrorStatus(err)
		c.JSON(code, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"reply": reply, "tools": titles})
}
