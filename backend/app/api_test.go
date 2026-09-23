package app

// Integration tests: real HTTP requests through the same router the server
// uses, against the local PostgreSQL. Everything a test creates (users,
// orders, dishes, stock changes) is removed or restored in t.Cleanup.
//
//	go test ./...        (skipped automatically when the database is not running)

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"restaurant-management/config"
	"restaurant-management/database"
	"restaurant-management/handlers"
	"restaurant-management/telegram"
)

const testSecret = "test-secret-for-integration-tests-0123456789"

var (
	testDB     *pgxpool.Pool
	testRouter *gin.Engine
	skipReason string
	counter    atomic.Int64
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	_ = config.Load()
	if err := handlers.InitJWT(testSecret); err != nil {
		panic(err)
	}
	db, err := database.Connect()
	if err != nil {
		skipReason = "database is not available: " + err.Error()
	} else {
		testDB = db
		testRouter = NewRouter(db, nil)
	}
	code := m.Run()
	if testDB != nil {
		testDB.Close()
	}
	os.Exit(code)
}

// ---------- helpers ----------

func needDB(t *testing.T) {
	t.Helper()
	if testDB == nil {
		t.Skip(skipReason)
	}
}

type response struct {
	Code int
	Body map[string]any
	List []any
	Raw  string
}

func request(t *testing.T, method, path, token string, body any) response {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	testRouter.ServeHTTP(w, req)

	res := response{Code: w.Code, Raw: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &res.Body)
	_ = json.Unmarshal(w.Body.Bytes(), &res.List)
	return res
}

func expect(t *testing.T, res response, code int, what string) {
	t.Helper()
	if res.Code != code {
		t.Fatalf("%s: got %d %s, want %d", what, res.Code, res.Raw, code)
	}
}

func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d-%d@apitest.rms", prefix, time.Now().UnixNano(), counter.Add(1))
}

// createUser inserts a user with any role directly (registration only creates
// customers) and returns its id and a valid token. Removed after the test.
func createUser(t *testing.T, role string) (int, string) {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	var id int
	err := testDB.QueryRow(context.Background(),
		`INSERT INTO users (name, email, password_hash, role) VALUES ($1, $2, $3, $4) RETURNING id`,
		"Test "+role, uniqueEmail(role), string(hash), role).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteUser(id) })

	token, err := handlers.GenerateToken(id, role)
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

func deleteUser(id int) {
	ctx := context.Background()
	testDB.Exec(ctx, `DELETE FROM orders WHERE user_id = $1`, id) // order_items cascade
	testDB.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
}

// dishByName finds a dish of the demo menu or skips the test.
func dishByName(t *testing.T, name string) (int, float64, int) {
	t.Helper()
	var id, category int
	var price float64
	err := testDB.QueryRow(context.Background(),
		`SELECT id, price, category_id FROM dishes WHERE name = $1 AND COALESCE(is_available, true) LIMIT 1`,
		name).Scan(&id, &price, &category)
	if err != nil {
		t.Skipf("demo dish %q not found (run go run ./cmd/seed): %v", name, err)
	}
	return id, price, category
}

func signToken(t *testing.T, claims jwt.MapClaims, secret string) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// ---------- authentication ----------

func TestRegisterAndLogin(t *testing.T) {
	needDB(t)
	email := uniqueEmail("register")
	t.Cleanup(func() {
		var id int
		if testDB.QueryRow(context.Background(), `SELECT id FROM users WHERE email = $1`, email).Scan(&id) == nil {
			deleteUser(id)
		}
	})

	res := request(t, "POST", "/api/auth/register", "", map[string]string{
		"name": "Тестовый гость", "email": email, "password": "secret123",
	})
	expect(t, res, http.StatusCreated, "register")
	user := res.Body["user"].(map[string]any)
	if user["role"] != "customer" {
		t.Fatalf("new user role = %v, want customer (registration must not grant staff roles)", user["role"])
	}

	// The password is stored as a bcrypt hash, never as plain text.
	var hash string
	testDB.QueryRow(context.Background(), `SELECT password_hash FROM users WHERE email = $1`, email).Scan(&hash)
	if hash == "secret123" || bcrypt.CompareHashAndPassword([]byte(hash), []byte("secret123")) != nil {
		t.Fatal("password is not stored as a bcrypt hash")
	}

	expect(t, request(t, "POST", "/api/auth/register", "", map[string]string{
		"name": "Дубль", "email": email, "password": "secret123",
	}), http.StatusConflict, "duplicate email")

	login := request(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "secret123"})
	expect(t, login, http.StatusOK, "login")
	token, _ := login.Body["token"].(string)
	if token == "" {
		t.Fatal("login returned no token")
	}

	profile := request(t, "GET", "/api/profile", token, nil)
	expect(t, profile, http.StatusOK, "profile with the new token")
	if profile.Body["role"] != "customer" {
		t.Fatalf("profile role = %v", profile.Body["role"])
	}
}

func TestRegisterValidation(t *testing.T) {
	needDB(t)
	cases := map[string]map[string]string{
		"invalid email":  {"name": "A", "email": "not-an-email", "password": "secret123"},
		"short password": {"name": "A", "email": uniqueEmail("short"), "password": "12345"},
		"missing name":   {"email": uniqueEmail("noname"), "password": "secret123"},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			expect(t, request(t, "POST", "/api/auth/register", "", body), http.StatusBadRequest, name)
		})
	}
}

func TestLoginFailures(t *testing.T) {
	needDB(t)
	_, _ = createUser(t, "customer")
	var email string
	testDB.QueryRow(context.Background(), `SELECT email FROM users WHERE email LIKE '%@apitest.rms' ORDER BY id DESC LIMIT 1`).Scan(&email)

	wrong := request(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "wrong-password"})
	unknown := request(t, "POST", "/api/auth/login", "", map[string]string{"email": uniqueEmail("nobody"), "password": "whatever1"})
	expect(t, wrong, http.StatusUnauthorized, "wrong password")
	expect(t, unknown, http.StatusUnauthorized, "unknown email")

	// Same answer for both, so an attacker can't find out which emails are registered.
	if wrong.Body["error"] != unknown.Body["error"] {
		t.Fatalf("different errors reveal registered emails: %v vs %v", wrong.Body["error"], unknown.Body["error"])
	}
}

func TestProtectedRoutesRejectBadTokens(t *testing.T) {
	needDB(t)
	id, _ := createUser(t, "admin")

	tokens := map[string]string{
		"no token":            "",
		"garbage":             "not.a.jwt",
		"expired":             signToken(t, jwt.MapClaims{"user_id": id, "role": "admin", "exp": time.Now().Add(-time.Hour).Unix()}, testSecret),
		"signed by other key": signToken(t, jwt.MapClaims{"user_id": id, "role": "admin", "exp": time.Now().Add(time.Hour).Unix()}, "attacker-secret-attacker-secret-attacker"),
	}
	for name, token := range tokens {
		t.Run(name, func(t *testing.T) {
			expect(t, request(t, "GET", "/api/admin/test", token, nil), http.StatusUnauthorized, name)
		})
	}

	t.Run("alg none", func(t *testing.T) {
		// A token "signed" with alg=none must never be accepted.
		unsigned, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
			"user_id": id, "role": "admin", "exp": time.Now().Add(time.Hour).Unix(),
		}).SignedString(jwt.UnsafeAllowNoneSignatureType)
		expect(t, request(t, "GET", "/api/admin/test", unsigned, nil), http.StatusUnauthorized, "alg none")
	})

	t.Run("wrong scheme", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/admin/test", nil)
		req.Header.Set("Authorization", "Token abc")
		w := httptest.NewRecorder()
		testRouter.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", w.Code)
		}
	})
}

// ---------- roles (RBAC) ----------

func TestRoleAccess(t *testing.T) {
	needDB(t)
	tokens := map[string]string{}
	for _, role := range []string{"customer", "waiter", "cook", "admin", "owner"} {
		_, tokens[role] = createUser(t, role)
	}

	cases := []struct {
		role, method, path string
		body               any
		want               int
	}{
		{"customer", "GET", "/api/admin/test", nil, 403},
		{"admin", "GET", "/api/admin/test", nil, 200},

		{"customer", "GET", "/api/admin/orders", nil, 403},
		{"owner", "GET", "/api/admin/orders", nil, 403},
		{"waiter", "GET", "/api/admin/orders", nil, 200},
		{"cook", "GET", "/api/admin/orders", nil, 200},

		{"customer", "GET", "/api/admin/analytics/summary?days=7", nil, 403},
		{"waiter", "GET", "/api/admin/analytics/summary?days=7", nil, 403},
		{"cook", "GET", "/api/admin/analytics/summary?days=7", nil, 403},
		{"owner", "GET", "/api/admin/analytics/summary?days=7", nil, 200},
		{"admin", "GET", "/api/admin/analytics/summary?days=7", nil, 200},

		{"waiter", "GET", "/api/admin/inventory", nil, 403},
		{"cook", "GET", "/api/admin/inventory", nil, 200},
		{"owner", "GET", "/api/admin/inventory", nil, 200},
		// The owner watches the warehouse but cannot book deliveries.
		{"owner", "POST", "/api/admin/inventory/receive", map[string]any{"items": []map[string]any{{"ingredient_id": 1, "quantity": 1}}}, 403},

		{"cook", "POST", "/api/admin/categories", map[string]string{"name": "Взлом"}, 403},
		{"owner", "POST", "/api/admin/dishes", map[string]any{"name": "Взлом", "price": 1, "category_id": 1}, 403},
		{"customer", "POST", "/api/admin/assistant", map[string]any{"messages": []map[string]string{{"role": "user", "content": "hi"}}}, 403},
		{"waiter", "POST", "/api/admin/assistant", map[string]any{"messages": []map[string]string{{"role": "user", "content": "hi"}}}, 403},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s %s %s", tc.role, tc.method, tc.path), func(t *testing.T) {
			expect(t, request(t, tc.method, tc.path, tokens[tc.role], tc.body), tc.want, tc.role)
		})
	}
}

// ---------- orders ----------

func createOrder(t *testing.T, token string, items ...map[string]any) response {
	t.Helper()
	return request(t, "POST", "/api/orders", token, map[string]any{"items": items})
}

func TestOrderPriceComesFromDatabase(t *testing.T) {
	needDB(t)
	dishID, price, _ := dishByName(t, "Пепперони")
	_, customer := createUser(t, "customer")

	// The client tries to set its own price and splits the dish into two lines.
	res := createOrder(t, customer,
		map[string]any{"dish_id": dishID, "quantity": 2, "price": 1},
		map[string]any{"dish_id": dishID, "quantity": 1, "price": 1},
	)
	expect(t, res, http.StatusCreated, "create order")

	if total := res.Body["total_price"].(float64); total != price*3 {
		t.Fatalf("total = %v, want %v (price from the database × 3)", total, price*3)
	}
	items := res.Body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["quantity"].(float64) != 3 {
		t.Fatalf("duplicate dishes must be merged into one line of 3, got %v", items)
	}
	if res.Body["status"] != "pending" {
		t.Fatalf("new order status = %v, want pending", res.Body["status"])
	}
}

func TestOrderValidation(t *testing.T) {
	needDB(t)
	_, category := func() (int, int) { id, _, c := dishByName(t, "Пепперони"); return id, c }()
	_, customer := createUser(t, "customer")
	_, admin := createUser(t, "admin")

	// A dish hidden from the menu cannot be ordered.
	hidden := request(t, "POST", "/api/admin/dishes", admin, map[string]any{
		"name": "Тест скрытое блюдо", "price": 1000, "category_id": category, "is_available": false,
	})
	expect(t, hidden, http.StatusCreated, "create hidden dish")
	hiddenID := int(hidden.Body["id"].(float64))
	t.Cleanup(func() { testDB.Exec(context.Background(), `DELETE FROM dishes WHERE id = $1`, hiddenID) })

	res := createOrder(t, customer, map[string]any{"dish_id": hiddenID, "quantity": 1})
	expect(t, res, http.StatusBadRequest, "hidden dish")
	if res.Body["error"] != "dish is not available" {
		t.Fatalf("error = %v", res.Body["error"])
	}

	expect(t, createOrder(t, customer), http.StatusBadRequest, "empty order")
	expect(t, createOrder(t, customer, map[string]any{"dish_id": 999999, "quantity": 1}), http.StatusBadRequest, "unknown dish")
	expect(t, createOrder(t, customer, map[string]any{"dish_id": hiddenID, "quantity": 0}), http.StatusBadRequest, "zero quantity")
	expect(t, createOrder(t, customer, map[string]any{"dish_id": hiddenID, "quantity": 51}), http.StatusBadRequest, "quantity over 50")
	expect(t, request(t, "POST", "/api/orders", "", map[string]any{"items": []any{}}), http.StatusUnauthorized, "no token")
}

func TestOrderVisibility(t *testing.T) {
	needDB(t)
	dishID, _, _ := dishByName(t, "Пепперони")
	_, owner := createUser(t, "customer")
	_, stranger := createUser(t, "customer")
	_, waiter := createUser(t, "waiter")

	order := createOrder(t, owner, map[string]any{"dish_id": dishID, "quantity": 1})
	expect(t, order, http.StatusCreated, "create order")
	path := fmt.Sprintf("/api/orders/%d", int(order.Body["id"].(float64)))

	expect(t, request(t, "GET", path, owner, nil), http.StatusOK, "own order")
	// Someone else's order looks exactly like a missing one.
	expect(t, request(t, "GET", path, stranger, nil), http.StatusNotFound, "someone else's order")
	expect(t, request(t, "GET", path, waiter, nil), http.StatusOK, "staff sees any order")

	mine := request(t, "GET", "/api/orders", stranger, nil)
	expect(t, mine, http.StatusOK, "list")
	if len(mine.List) != 0 {
		t.Fatalf("a new customer sees %d orders, want 0", len(mine.List))
	}
}

func TestOrderCancelByCustomer(t *testing.T) {
	needDB(t)
	dishID, _, _ := dishByName(t, "Пепперони")
	_, customer := createUser(t, "customer")
	_, stranger := createUser(t, "customer")
	_, waiter := createUser(t, "waiter")

	first := createOrder(t, customer, map[string]any{"dish_id": dishID, "quantity": 1})
	cancel := fmt.Sprintf("/api/orders/%d/cancel", int(first.Body["id"].(float64)))
	expect(t, request(t, "POST", cancel, stranger, nil), http.StatusConflict, "cancel someone else's order")
	expect(t, request(t, "POST", cancel, customer, nil), http.StatusOK, "cancel own pending order")
	expect(t, request(t, "POST", cancel, customer, nil), http.StatusConflict, "cancel twice")

	// Once the waiter confirmed the order, the customer can no longer cancel it.
	second := createOrder(t, customer, map[string]any{"dish_id": dishID, "quantity": 1})
	id := int(second.Body["id"].(float64))
	expect(t, request(t, "PUT", fmt.Sprintf("/api/admin/orders/%d/status", id), waiter, map[string]string{"status": "confirmed"}), http.StatusOK, "confirm")
	expect(t, request(t, "POST", fmt.Sprintf("/api/orders/%d/cancel", id), customer, nil), http.StatusConflict, "cancel confirmed order")
}

func TestOrderLifecycleAndStockWriteOff(t *testing.T) {
	needDB(t)
	ctx := context.Background()
	dishID, _, _ := dishByName(t, "Пепперони")
	_, customer := createUser(t, "customer")
	_, waiter := createUser(t, "waiter")
	_, cook := createUser(t, "cook")
	_, admin := createUser(t, "admin")

	// Remember the stock of every ingredient of the dish and restore it afterwards.
	type line struct {
		id          int
		perPortion  float64
		stockBefore float64
	}
	rows, err := testDB.Query(ctx,
		`SELECT ri.ingredient_id, ri.quantity, COALESCE(s.quantity, 0)
		 FROM recipe_items ri LEFT JOIN stock s ON s.ingredient_id = ri.ingredient_id
		 WHERE ri.dish_id = $1`, dishID)
	if err != nil {
		t.Fatal(err)
	}
	var recipe []line
	for rows.Next() {
		var l line
		rows.Scan(&l.id, &l.perPortion, &l.stockBefore)
		recipe = append(recipe, l)
	}
	rows.Close()
	if len(recipe) == 0 {
		t.Skip("no recipe for the demo dish (run the migrations and go run ./cmd/seed)")
	}
	t.Cleanup(func() {
		for _, l := range recipe {
			testDB.Exec(ctx, `UPDATE stock SET quantity = $1 WHERE ingredient_id = $2`, l.stockBefore, l.id)
		}
	})

	const portions = 2
	order := createOrder(t, customer, map[string]any{"dish_id": dishID, "quantity": portions})
	expect(t, order, http.StatusCreated, "create order")
	status := fmt.Sprintf("/api/admin/orders/%d/status", int(order.Body["id"].(float64)))
	set := func(token, to string) response {
		return request(t, "PUT", status, token, map[string]string{"status": to})
	}

	// Who may set what: the check is by role, before the transition itself.
	expect(t, set(cook, "confirmed"), http.StatusForbidden, "cook confirms")
	expect(t, set(waiter, "preparing"), http.StatusForbidden, "waiter starts cooking")
	expect(t, set(customer, "confirmed"), http.StatusForbidden, "customer confirms own order")

	// Skipping a step is impossible even for the admin.
	skip := set(admin, "ready")
	expect(t, skip, http.StatusConflict, "pending → ready")
	if allowed := fmt.Sprint(skip.Body["allowed"]); !strings.Contains(allowed, "confirmed") {
		t.Fatalf("the error should list allowed statuses, got %v", skip.Body["allowed"])
	}

	expect(t, set(waiter, "confirmed"), http.StatusOK, "waiter confirms")
	for _, l := range recipe {
		var now float64
		testDB.QueryRow(ctx, `SELECT COALESCE((SELECT quantity FROM stock WHERE ingredient_id = $1), 0)`, l.id).Scan(&now)
		if now != l.stockBefore {
			t.Fatalf("stock changed before cooking started: %v → %v", l.stockBefore, now)
		}
	}

	expect(t, set(cook, "preparing"), http.StatusOK, "cook starts cooking")
	for _, l := range recipe {
		var now float64
		testDB.QueryRow(ctx, `SELECT COALESCE((SELECT quantity FROM stock WHERE ingredient_id = $1), 0)`, l.id).Scan(&now)
		want := math.Max(l.stockBefore-l.perPortion*portions, 0)
		if math.Abs(now-want) > 0.001 {
			t.Fatalf("ingredient %d: stock %v, want %v (recipe %v × %d portions written off)", l.id, now, want, l.perPortion, portions)
		}
	}

	expect(t, set(cook, "ready"), http.StatusOK, "cook marks ready")
	expect(t, set(waiter, "completed"), http.StatusOK, "waiter hands it over")
	expect(t, set(admin, "cancelled"), http.StatusConflict, "cancel a completed order")
}

// ---------- Telegram wiring ----------

// TestTelegramWiring: with a bot, an order placed through the HTTP API reaches
// the kitchen chat, and status changes reach the linked customer.
func TestTelegramWiring(t *testing.T) {
	needDB(t)
	ctx := context.Background()
	var migrated bool
	testDB.QueryRow(ctx, `SELECT to_regclass('telegram_links') IS NOT NULL`).Scan(&migrated)
	if !migrated {
		t.Skip("run migration 003_telegram.sql first")
	}

	// A fake api.telegram.org that remembers who got which text.
	var mu sync.Mutex
	sent := map[int64][]string{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&p)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"username":"rms_test_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			mu.Lock()
			sent[p.ChatID] = append(sent[p.ChatID], p.Text)
			mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"result":{}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	defer fake.Close()

	bot := handlers.NewTelegramBot(testDB, telegram.NewClient("TEST:TOKEN", fake.URL), 9)
	if err := bot.Init(ctx); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(testDB, bot)
	call := func(method, path, token string, body any) response {
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		res := response{Code: w.Code, Raw: w.Body.String()}
		json.Unmarshal(w.Body.Bytes(), &res.Body)
		return res
	}

	// Kitchen chat and a linked customer (bot settings are restored afterwards).
	var savedKitchen string
	hadKitchen := testDB.QueryRow(ctx, `SELECT value FROM bot_settings WHERE key = 'kitchen_chat'`).Scan(&savedKitchen) == nil
	t.Cleanup(func() {
		if hadKitchen {
			testDB.Exec(ctx, `UPDATE bot_settings SET value = $1 WHERE key = 'kitchen_chat'`, savedKitchen)
		} else {
			testDB.Exec(ctx, `DELETE FROM bot_settings WHERE key = 'kitchen_chat'`)
		}
	})
	const kitchen, customerTG = int64(-424242), int64(8_800_555_35_35)
	testDB.Exec(ctx, `INSERT INTO bot_settings (key, value) VALUES ('kitchen_chat', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, fmt.Sprint(kitchen))

	customerID, customer := createUser(t, "customer")
	_, waiter := createUser(t, "waiter")
	testDB.Exec(ctx, `DELETE FROM telegram_links WHERE telegram_id = $1`, customerTG)
	if _, err := testDB.Exec(ctx, `INSERT INTO telegram_links (user_id, telegram_id) VALUES ($1, $2)`, customerID, customerTG); err != nil {
		t.Fatal(err)
	}

	// The public bot link for the website (no login).
	public := call("GET", "/api/bot", "", nil)
	expect(t, public, http.StatusOK, "public bot info")
	if public.Body["url"] != "https://t.me/rms_test_bot" || public.Body["enabled"] != true {
		t.Fatalf("public bot info = %v", public.Body)
	}

	// The link endpoint works when the bot is configured.
	status := call("GET", "/api/telegram", customer, nil)
	expect(t, status, http.StatusOK, "telegram status")
	if status.Body["linked"] != true || status.Body["bot"] != "@rms_test_bot" {
		t.Fatalf("status = %v", status.Body)
	}

	dishID, _, _ := dishByName(t, "Пепперони")
	order := call("POST", "/api/orders", customer, map[string]any{"items": []map[string]any{{"dish_id": dishID, "quantity": 1}}})
	expect(t, order, http.StatusCreated, "create order")
	id := int(order.Body["id"].(float64))
	expect(t, call("PUT", fmt.Sprintf("/api/admin/orders/%d/status", id), waiter, map[string]string{"status": "confirmed"}), http.StatusOK, "confirm")
	bot.Wait()

	mu.Lock()
	defer mu.Unlock()
	want := fmt.Sprintf("#%d", id)
	if k := strings.Join(sent[kitchen], "\n"); !strings.Contains(k, "Новый заказ "+want) || !strings.Contains(k, "В работу: "+want) {
		t.Fatalf("kitchen chat got: %q", k)
	}
	if c := strings.Join(sent[customerTG], "\n"); !strings.Contains(c, want) || !strings.Contains(c, "принят") {
		t.Fatalf("customer got: %q", c)
	}
}

// Without a bot the Telegram endpoints report "disabled" instead of failing.
func TestTelegramDisabled(t *testing.T) {
	needDB(t)
	_, token := createUser(t, "customer")
	status := request(t, "GET", "/api/telegram", token, nil)
	expect(t, status, http.StatusOK, "status")
	if status.Body["enabled"] != false {
		t.Fatalf("enabled = %v, want false", status.Body["enabled"])
	}
	expect(t, request(t, "POST", "/api/telegram/link", token, nil), http.StatusServiceUnavailable, "link without a bot")

	// The website hides the bot link instead of showing a dead one.
	public := request(t, "GET", "/api/bot", "", nil)
	expect(t, public, http.StatusOK, "public bot info without a bot")
	if public.Body["enabled"] != false || public.Body["url"] != nil {
		t.Fatalf("public bot info without a bot = %v", public.Body)
	}
}

// ---------- recipes (tech cards) ----------

// newIngredient creates an ingredient through the API and removes it afterwards.
func newIngredient(t *testing.T, admin, name, unit string, price float64) int {
	t.Helper()
	res := request(t, "POST", "/api/admin/ingredients", admin, map[string]any{
		"name": name, "unit": unit, "price": price, "pack_size": 1, "shelf_life_days": 7,
	})
	expect(t, res, http.StatusCreated, "create ingredient "+name)
	id := int(res.Body["id"].(float64))
	t.Cleanup(func() {
		ctx := context.Background()
		testDB.Exec(ctx, `DELETE FROM recipe_items WHERE ingredient_id = $1`, id)
		testDB.Exec(ctx, `DELETE FROM ingredients WHERE id = $1`, id)
	})
	return id
}

func TestRecipeEditor(t *testing.T) {
	needDB(t)
	ctx := context.Background()
	_, admin := createUser(t, "admin")
	_, cook := createUser(t, "cook")
	_, owner := createUser(t, "owner")
	_, waiter := createUser(t, "waiter")
	customerID, customer := createUser(t, "customer")
	_ = customerID

	_, _, category := dishByName(t, "Пепперони")
	suffix := fmt.Sprint(time.Now().UnixNano())
	dough := newIngredient(t, admin, "Тест тесто "+suffix, "кг", 400)
	cheese := newIngredient(t, admin, "Тест сыр "+suffix, "кг", 5000)
	expect(t, request(t, "POST", "/api/admin/ingredients", admin, map[string]any{
		"name": "Тест тесто " + suffix, "unit": "кг", "price": 1, "pack_size": 1, "shelf_life_days": 1,
	}), http.StatusConflict, "duplicate ingredient name")
	expect(t, request(t, "POST", "/api/admin/ingredients", admin, map[string]any{
		"name": "Плохая единица", "unit": "ведро", "price": 1, "pack_size": 1, "shelf_life_days": 1,
	}), http.StatusBadRequest, "unknown unit")

	// A new dish added from the admin panel has no recipe yet.
	dish := request(t, "POST", "/api/admin/dishes", admin, map[string]any{
		"name": "Тест пицца " + suffix, "price": 3000, "cost_price": 0, "category_id": category,
	})
	expect(t, dish, http.StatusCreated, "create dish")
	dishID := int(dish.Body["id"].(float64))
	// Cleanups run in reverse order, so this one runs BEFORE the users' cleanups:
	// remove the orders with this dish first, or the foreign key keeps the dish.
	t.Cleanup(func() {
		testDB.Exec(ctx, `DELETE FROM orders WHERE id IN (SELECT order_id FROM order_items WHERE dish_id = $1)`, dishID)
		testDB.Exec(ctx, `DELETE FROM dishes WHERE id = $1`, dishID)
	})
	recipePath := fmt.Sprintf("/api/admin/dishes/%d/recipe", dishID)

	empty := request(t, "GET", recipePath, cook, nil)
	expect(t, empty, http.StatusOK, "empty recipe")
	if n := len(empty.Body["items"].([]any)); n != 0 {
		t.Fatalf("new dish has %d recipe lines", n)
	}

	// Validation.
	bad := map[string]any{
		"zero quantity":      []map[string]any{{"ingredient_id": dough, "quantity": 0}},
		"huge quantity":      []map[string]any{{"ingredient_id": dough, "quantity": 500}},
		"duplicate line":     []map[string]any{{"ingredient_id": dough, "quantity": 0.2}, {"ingredient_id": dough, "quantity": 0.1}},
		"unknown ingredient": []map[string]any{{"ingredient_id": 99999999, "quantity": 0.2}},
	}
	for name, items := range bad {
		expect(t, request(t, "PUT", recipePath, admin, map[string]any{"items": items}), http.StatusBadRequest, name)
	}

	// Rights: admin and cook edit recipes; owner and waiter don't; the catalogue is admin-only.
	body := map[string]any{"items": []map[string]any{
		{"ingredient_id": dough, "quantity": 0.25},
		{"ingredient_id": cheese, "quantity": 0.12},
	}, "update_cost": true}
	expect(t, request(t, "PUT", recipePath, owner, body), http.StatusForbidden, "owner edits a recipe")
	expect(t, request(t, "PUT", recipePath, waiter, body), http.StatusForbidden, "waiter edits a recipe")
	expect(t, request(t, "GET", "/api/admin/recipes", customer, nil), http.StatusForbidden, "customer lists recipes")
	expect(t, request(t, "POST", "/api/admin/ingredients", cook, map[string]any{
		"name": "Повар добавил", "unit": "кг", "price": 1, "pack_size": 1, "shelf_life_days": 1,
	}), http.StatusForbidden, "cook edits the catalogue")

	saved := request(t, "PUT", recipePath, cook, body)
	expect(t, saved, http.StatusOK, "cook saves the recipe")
	// 0.25 × 400 + 0.12 × 5000 = 100 + 600 = 700 ₸
	if cost := saved.Body["recipe_cost"].(float64); cost != 700 {
		t.Fatalf("recipe cost = %v, want 700", cost)
	}
	if cp := saved.Body["cost_price"].(float64); cp != 700 {
		t.Fatalf("dish cost price = %v, want 700 (update_cost)", cp)
	}

	// The list shows the dish with its recipe.
	list := request(t, "GET", "/api/admin/recipes", cook, nil)
	expect(t, list, http.StatusOK, "list recipes")
	found := false
	for _, r := range list.List {
		row := r.(map[string]any)
		if int(row["dish_id"].(float64)) == dishID {
			found = row["items"].(float64) == 2 && row["recipe_cost"].(float64) == 700
		}
	}
	if !found {
		t.Fatal("the recipes list does not show the new recipe correctly")
	}

	// A new ingredient price changes the recipe cost; saving without update_cost keeps the dish cost.
	expect(t, request(t, "PUT", fmt.Sprintf("/api/admin/ingredients/%d", cheese), admin, map[string]any{
		"name": "Тест сыр " + suffix, "unit": "кг", "price": 6000, "pack_size": 1, "shelf_life_days": 7,
	}), http.StatusOK, "raise the cheese price")
	body["update_cost"] = false
	resaved := request(t, "PUT", recipePath, admin, body)
	expect(t, resaved, http.StatusOK, "save again")
	if resaved.Body["recipe_cost"].(float64) != 820 || resaved.Body["cost_price"].(float64) != 700 {
		t.Fatalf("recipe cost %v (want 820), cost price %v (want 700 — not updated)",
			resaved.Body["recipe_cost"], resaved.Body["cost_price"])
	}

	// An ingredient used in a recipe cannot be deleted.
	expect(t, request(t, "DELETE", fmt.Sprintf("/api/admin/ingredients/%d", cheese), admin, nil), http.StatusConflict, "delete used ingredient")

	// The recipe now drives the stock write-off for the new dish.
	testDB.Exec(ctx, `INSERT INTO stock (ingredient_id, quantity) VALUES ($1, 10), ($2, 10)
		ON CONFLICT (ingredient_id) DO UPDATE SET quantity = 10`, dough, cheese)
	order := request(t, "POST", "/api/orders", customer, map[string]any{"items": []map[string]any{{"dish_id": dishID, "quantity": 2}}})
	expect(t, order, http.StatusCreated, "order the new dish")
	status := fmt.Sprintf("/api/admin/orders/%d/status", int(order.Body["id"].(float64)))
	expect(t, request(t, "PUT", status, admin, map[string]string{"status": "confirmed"}), http.StatusOK, "confirm")
	expect(t, request(t, "PUT", status, cook, map[string]string{"status": "preparing"}), http.StatusOK, "cook")
	var doughLeft, cheeseLeft float64
	testDB.QueryRow(ctx, `SELECT quantity FROM stock WHERE ingredient_id = $1`, dough).Scan(&doughLeft)
	testDB.QueryRow(ctx, `SELECT quantity FROM stock WHERE ingredient_id = $1`, cheese).Scan(&cheeseLeft)
	if doughLeft != 9.5 || cheeseLeft != 9.76 {
		t.Fatalf("stock after 2 portions: dough %v (want 9.5), cheese %v (want 9.76)", doughLeft, cheeseLeft)
	}

	// An empty list removes the recipe.
	cleared := request(t, "PUT", recipePath, admin, map[string]any{"items": []any{}})
	expect(t, cleared, http.StatusOK, "clear recipe")
	if n := len(cleared.Body["items"].([]any)); n != 0 {
		t.Fatalf("recipe still has %d lines", n)
	}
	expect(t, request(t, "PUT", "/api/admin/dishes/99999999/recipe", admin, map[string]any{"items": []any{}}), http.StatusNotFound, "unknown dish")
}
