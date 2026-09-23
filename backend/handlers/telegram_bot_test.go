package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"restaurant-management/database"
	"restaurant-management/telegram"
)

// ---------- fake Telegram Bot API ----------

type sentMessage struct {
	ChatID int64
	Text   string
}

// fakeTelegram answers like api.telegram.org and records every sent message.
type fakeTelegram struct {
	server *httptest.Server
	mu     sync.Mutex
	sent   []sentMessage
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		body, _ := io.ReadAll(r.Body)
		var params map[string]any
		json.Unmarshal(body, &params)

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"first_name":"RMS","username":"rms_test_bot"}}`)
		case "sendMessage":
			// Telegram rejects malformed HTML — so do we, to catch escaping bugs.
			text, _ := params["text"].(string)
			if params["parse_mode"] == "HTML" && !balancedTags(text) {
				fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`)
				return
			}
			f.mu.Lock()
			f.sent = append(f.sent, sentMessage{ChatID: int64(params["chat_id"].(float64)), Text: text})
			f.mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"result":{}}`)
		default: // setMyCommands, sendChatAction, getUpdates
			fmt.Fprint(w, `{"ok":true,"result":[]}`)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// balancedTags checks that <b> and <i> open and close in pairs and no raw "<" is left.
func balancedTags(s string) bool {
	for _, tag := range []string{"b", "i"} {
		if strings.Count(s, "<"+tag+">") != strings.Count(s, "</"+tag+">") {
			return false
		}
		s = strings.NewReplacer("<"+tag+">", "", "</"+tag+">", "").Replace(s)
	}
	return !strings.Contains(s, "<")
}

func (f *fakeTelegram) messagesTo(chatID int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		if m.ChatID == chatID {
			out = append(out, m.Text)
		}
	}
	return out
}

func (f *fakeTelegram) reset() {
	f.mu.Lock()
	f.sent = nil
	f.mu.Unlock()
}

// ---------- fixtures ----------

var tgCounter atomic.Int64

type botFixture struct {
	db   *pgxpool.Pool
	bot  *TelegramBot
	tg   *fakeTelegram
	ctx  context.Context
	next func() int64 // unique Telegram user ids
}

func newBotFixture(t *testing.T) *botFixture {
	t.Helper()
	db, err := database.Connect()
	if err != nil {
		t.Skip("database is not available: ", err)
	}
	t.Cleanup(db.Close)

	var ok bool
	db.QueryRow(context.Background(), `SELECT to_regclass('telegram_links') IS NOT NULL`).Scan(&ok)
	if !ok {
		t.Skip("run migration 003_telegram.sql first")
	}

	tg := newFakeTelegram(t)
	bot := NewTelegramBot(db, telegram.NewClient("TEST:TOKEN", tg.server.URL), 9)
	if err := bot.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bot.Wait)

	// Bot settings are global: remember them and restore after the test.
	saved := map[string]string{}
	for _, key := range []string{"kitchen_chat", "digest_date"} {
		if v, ok := bot.setting(context.Background(), key); ok {
			saved[key] = v
		}
	}
	t.Cleanup(func() {
		for _, key := range []string{"kitchen_chat", "digest_date"} {
			if v, ok := saved[key]; ok {
				bot.setSetting(context.Background(), key, v)
			} else {
				db.Exec(context.Background(), `DELETE FROM bot_settings WHERE key = $1`, key)
			}
		}
	})

	base := 9_000_000_000 + time.Now().UnixNano()%1_000_000*1000
	return &botFixture{db: db, bot: bot, tg: tg, ctx: context.Background(),
		next: func() int64 { return base + tgCounter.Add(1) }}
}

func (f *botFixture) user(t *testing.T, role string) int {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	var id int
	err := f.db.QueryRow(f.ctx,
		`INSERT INTO users (name, email, password_hash, role) VALUES ($1, $2, $3, $4) RETURNING id`,
		"Тест "+role, fmt.Sprintf("tg-%s-%d@bottest.rms", role, f.next()), string(hash), role).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Exec(f.ctx, `DELETE FROM orders WHERE user_id = $1`, id)
		f.db.Exec(f.ctx, `DELETE FROM users WHERE id = $1`, id) // telegram_links cascade
	})
	return id
}

// linked creates a user already linked to a Telegram id.
func (f *botFixture) linked(t *testing.T, role string) (int, int64) {
	t.Helper()
	id := f.user(t, role)
	tgID := f.next()
	if _, err := f.db.Exec(f.ctx, `INSERT INTO telegram_links (user_id, telegram_id, username) VALUES ($1, $2, 'tester')`, id, tgID); err != nil {
		t.Fatal(err)
	}
	return id, tgID
}

func private(from int64, text string) *telegram.Message {
	return &telegram.Message{From: &telegram.User{ID: from}, Chat: telegram.Chat{ID: from, Type: "private"}, Text: text}
}

func group(chat, from int64, text string) *telegram.Message {
	return &telegram.Message{From: &telegram.User{ID: from}, Chat: telegram.Chat{ID: chat, Type: "group"}, Text: text}
}

// ask sends a message to the bot and returns the replies to that chat.
func (f *botFixture) ask(m *telegram.Message) string {
	f.tg.reset()
	f.bot.handle(f.ctx, m)
	f.bot.Wait()
	return strings.Join(f.tg.messagesTo(m.Chat.ID), "\n---\n")
}

func mustContain(t *testing.T, got string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(got, p) {
			t.Fatalf("reply does not contain %q:\n%s", p, got)
		}
	}
}

// ---------- tests ----------

func TestBotLinkAccount(t *testing.T) {
	f := newBotFixture(t)
	userID := f.user(t, "customer")

	// The profile page asks for a one-time link.
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/telegram/link", nil)
	c.Set("user_id", float64(userID))
	c.Set("role", "customer")
	(&TelegramHandler{DB: f.db, Bot: f.bot}).CreateLink(c)
	if w.Code != 200 {
		t.Fatalf("create link: %d %s", w.Code, w.Body)
	}
	var link struct{ URL string }
	json.Unmarshal(w.Body.Bytes(), &link)
	prefix := "https://t.me/rms_test_bot?start="
	if !strings.HasPrefix(link.URL, prefix) {
		t.Fatalf("link = %q", link.URL)
	}
	code := strings.TrimPrefix(link.URL, prefix)

	tgID := f.next()
	mustContain(t, f.ask(group(-5, tgID, "/start "+code)), "только в личном чате")
	mustContain(t, f.ask(private(tgID, "/start "+code)), "Аккаунт привязан", "Тест customer")

	var linkedTo int
	f.db.QueryRow(f.ctx, `SELECT user_id FROM telegram_links WHERE telegram_id = $1`, tgID).Scan(&linkedTo)
	if linkedTo != userID {
		t.Fatalf("telegram %d is linked to user %d, want %d", tgID, linkedTo, userID)
	}

	// The code is single-use.
	mustContain(t, f.ask(private(f.next(), "/start "+code)), "недействительна")

	// An expired code does not work either.
	expired := "0123456789abcdef0123456789abcdef"
	f.db.Exec(f.ctx, `INSERT INTO telegram_link_codes (code, user_id, expires_at) VALUES ($1, $2, NOW() - interval '1 minute')`, expired, userID)
	t.Cleanup(func() { f.db.Exec(f.ctx, `DELETE FROM telegram_link_codes WHERE code = $1`, expired) })
	mustContain(t, f.ask(private(f.next(), "/start "+expired)), "недействительна")

	mustContain(t, f.ask(private(tgID, "/unlink")), "отвязан")
	mustContain(t, f.ask(private(tgID, "/orders")), "привяжите аккаунт")
}

func TestBotOrderNotifications(t *testing.T) {
	f := newBotFixture(t)
	customerID, customerTG := f.linked(t, "customer")
	_, cookTG := f.linked(t, "cook")

	kitchen := -f.next() // group chat ids are negative
	mustContain(t, f.ask(group(kitchen, cookTG, "/kitchen@rms_test_bot")), "чат кухни")

	var dishID int
	var price float64
	if err := f.db.QueryRow(f.ctx, `SELECT id, price FROM dishes WHERE COALESCE(is_available, true) ORDER BY id LIMIT 1`).Scan(&dishID, &price); err != nil {
		t.Skip("no dishes: run go run ./cmd/seed")
	}
	var orderID int
	f.db.QueryRow(f.ctx, `INSERT INTO orders (user_id, status, total_price) VALUES ($1, 'pending', $2) RETURNING id`, customerID, price*2).Scan(&orderID)
	f.db.Exec(f.ctx, `INSERT INTO order_items (order_id, dish_id, quantity, price) VALUES ($1, $2, 2, $3)`, orderID, dishID, price)

	f.tg.reset()
	f.bot.OrderCreated(orderID)
	f.bot.Wait()
	id := fmt.Sprintf("#%d", orderID)
	mustContain(t, strings.Join(f.tg.messagesTo(kitchen), "\n"), "Новый заказ "+id, "×2", "Тест customer")
	mustContain(t, strings.Join(f.tg.messagesTo(customerTG), "\n"), id, "оформлен", tenge(price*2))

	cases := []struct{ status, customer, kitchen string }{
		{"confirmed", "принят", "В работу: " + id},
		{"preparing", "готовится", ""},
		{"ready", "готов — можно забирать", ""},
		{"completed", "Приятного аппетита", ""},
	}
	for _, tc := range cases {
		f.tg.reset()
		f.bot.OrderStatusChanged(orderID, tc.status)
		f.bot.Wait()
		mustContain(t, strings.Join(f.tg.messagesTo(customerTG), "\n"), id, tc.customer)
		gotKitchen := strings.Join(f.tg.messagesTo(kitchen), "\n")
		if tc.kitchen == "" && gotKitchen != "" {
			t.Fatalf("%s: the kitchen should not be disturbed, got %q", tc.status, gotKitchen)
		}
		if tc.kitchen != "" {
			mustContain(t, gotKitchen, tc.kitchen)
		}
	}

	// A guest without Telegram gets nothing, and nothing breaks.
	otherID := f.user(t, "customer")
	var otherOrder int
	f.db.QueryRow(f.ctx, `INSERT INTO orders (user_id, status, total_price) VALUES ($1, 'pending', 0) RETURNING id`, otherID).Scan(&otherOrder)
	f.tg.reset()
	f.bot.OrderStatusChanged(otherOrder, "ready")
	f.bot.Wait()
	if n := len(f.tg.messagesTo(customerTG)); n != 0 {
		t.Fatalf("someone else's order was reported to the customer (%d messages)", n)
	}
}

func TestBotCommandRoles(t *testing.T) {
	f := newBotFixture(t)
	stranger := f.next()
	_, customer := f.linked(t, "customer")
	_, cook := f.linked(t, "cook")
	_, owner := f.linked(t, "owner")

	mustContain(t, f.ask(private(stranger, "/menu")), "Меню", "₸")
	mustContain(t, f.ask(private(stranger, "/start")), "Подключить Telegram")
	mustContain(t, f.ask(private(stranger, "/today")), "привяжите аккаунт")
	mustContain(t, f.ask(private(customer, "/today")), "только: владелец, администратор")
	mustContain(t, f.ask(private(customer, "/kitchen")), "только: администратор, повар")
	mustContain(t, f.ask(private(customer, "/help")), "Статусы ваших заказов")
	mustContain(t, f.ask(private(customer, "/orders")), "нет заказов")
	mustContain(t, f.ask(private(cook, "/stock")), "Склад")
	mustContain(t, f.ask(private(cook, "/forecast")), "только: владелец, администратор")

	mustContain(t, f.ask(private(owner, "/today")), "Сводка RMS", "Вчера")
	mustContain(t, f.ask(private(owner, "/forecast")), "Прогноз на 7 дней")
	mustContain(t, f.ask(private(owner, "/promo")), "Что делать с меню")
	mustContain(t, f.ask(private(owner, "/digest")), "выключена")
	mustContain(t, f.ask(private(owner, "/digest")), "включена")
	mustContain(t, f.ask(private(owner, "/nonsense")), "/help")

	// In group chats: commands for other bots and plain chatter are ignored.
	if got := f.ask(group(-f.next(), owner, "/today@other_bot")); got != "" {
		t.Fatalf("answered a command for another bot: %q", got)
	}
	if got := f.ask(group(-f.next(), owner, "всем привет")); got != "" {
		t.Fatalf("answered plain text in a group: %q", got)
	}
}

func TestBotFreeTextGoesToAssistant(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	f := newBotFixture(t)
	_, customer := f.linked(t, "customer")
	_, owner := f.linked(t, "owner")

	mustContain(t, f.ask(private(customer, "что рекламировать?")), "Я понимаю команды")
	// Without an API key the owner gets a clear hint instead of silence.
	mustContain(t, f.ask(private(owner, "что рекламировать?")), "ИИ-ассистент ещё не подключён")
}

func TestBotDigestOncePerDay(t *testing.T) {
	f := newBotFixture(t)
	_, owner := f.linked(t, "owner")
	_, customer := f.linked(t, "customer")
	f.db.Exec(f.ctx, `DELETE FROM bot_settings WHERE key = 'digest_date'`)

	day := time.Date(2030, 1, 15, 0, 0, 0, 0, time.Local) // a date no real digest used
	f.tg.reset()
	if f.bot.sendDigestIfDue(f.ctx, day.Add(8*time.Hour)) {
		t.Fatal("digest sent before DigestHour")
	}
	if !f.bot.sendDigestIfDue(f.ctx, day.Add(9*time.Hour+5*time.Minute)) {
		t.Fatal("digest not sent at 9:05")
	}
	mustContain(t, strings.Join(f.tg.messagesTo(owner), "\n"), "Сводка RMS")
	if len(f.tg.messagesTo(customer)) != 0 {
		t.Fatal("a customer received the owner's digest")
	}
	if f.bot.sendDigestIfDue(f.ctx, day.Add(15*time.Hour)) {
		t.Fatal("digest sent twice on the same day")
	}
	if !f.bot.sendDigestIfDue(f.ctx, day.AddDate(0, 0, 1).Add(9*time.Hour)) {
		t.Fatal("digest not sent on the next day")
	}
}

// ---------- pure functions ----------

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in, cmd, arg string
		forUs        bool
	}{
		{"/start abc123", "start", "abc123", true},
		{"/Kitchen@RMS_test_bot", "kitchen", "", true},
		{"/today@other_bot", "", "", false},
		{"/menu", "menu", "", true},
	}
	for _, tc := range cases {
		cmd, arg, forUs := parseCommand(tc.in, "rms_test_bot")
		if cmd != tc.cmd || arg != tc.arg || forUs != tc.forUs {
			t.Errorf("parseCommand(%q) = %q %q %v", tc.in, cmd, arg, forUs)
		}
	}
}

func TestAssistantHTML(t *testing.T) {
	got := assistantHTML("Итог: **Дракон** <script>\n- скидка 10%\n- фото & пост")
	want := "Итог: <b>Дракон</b> &lt;script&gt;\n• скидка 10%\n• фото &amp; пост"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestSplitMessage(t *testing.T) {
	line := strings.Repeat("я", 99) + "\n" // 100 characters, 2-byte letters
	parts := splitMessage(strings.Repeat(line, 100), 4000)
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}
	for i, p := range parts {
		if n := len([]rune(p)); n > 4000 {
			t.Fatalf("part %d has %d characters", i, n)
		}
		if strings.HasPrefix(p, "\n") || !strings.HasPrefix(p, "я") {
			t.Fatalf("part %d is not cut at a line break", i)
		}
	}
}
