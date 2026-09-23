package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"restaurant-management/telegram"
)

// ---------- Telegram bot ----------
//
// One more channel of the same system: the bot runs inside the server,
// reads the same database and calls the same analytics functions.
//
//   guests  — order status notifications, /menu, /orders
//   kitchen — new orders in a group chat (/kitchen)
//   owner   — morning digest, /today /forecast /stock /promo, questions to the AI assistant
//
// Updates are received by long polling, so no public URL is needed.

type TelegramBot struct {
	DB         *pgxpool.Pool
	API        *telegram.Client
	Analytics  *AnalyticsHandler
	Inventory  *InventoryHandler
	Assistant  *AssistantHandler
	DigestHour int // local hour of the morning digest

	Username string // the bot's @username, filled by Init

	wg    sync.WaitGroup // background sends (tests wait for them)
	mu    sync.Mutex
	chats map[int64][]assistantMessage // short assistant history per Telegram user
}

func NewTelegramBot(db *pgxpool.Pool, api *telegram.Client, digestHour int) *TelegramBot {
	analytics := &AnalyticsHandler{DB: db}
	inventory := &InventoryHandler{DB: db, Analytics: analytics}
	return &TelegramBot{
		DB:         db,
		API:        api,
		Analytics:  analytics,
		Inventory:  inventory,
		Assistant:  &AssistantHandler{Analytics: analytics, Inventory: inventory},
		DigestHour: digestHour,
		chats:      map[int64][]assistantMessage{},
	}
}

var botCommands = map[string]string{
	"menu":     "Меню и цены",
	"orders":   "Мои заказы",
	"today":    "Сводка на сегодня (владелец)",
	"forecast": "Прогноз на неделю (владелец)",
	"stock":    "Склад: что заканчивается",
	"promo":    "Что рекламировать (владелец)",
	"digest":   "Вкл/выкл утреннюю сводку",
	"kitchen":  "Сделать этот чат чатом кухни",
	"new":      "Новый диалог с ИИ-ассистентом",
	"help":     "Что умеет бот",
}
var botCommandOrder = []string{"menu", "orders", "today", "forecast", "stock", "promo", "digest", "kitchen", "new", "help"}

// Init checks the token and learns the bot's username.
func (b *TelegramBot) Init(ctx context.Context) error {
	me, err := b.API.GetMe(ctx)
	if err != nil {
		return err
	}
	b.Username = me.Username
	if err := b.API.SetCommands(ctx, botCommands, botCommandOrder); err != nil {
		log.Printf("telegram: could not set the command menu: %v", err)
	}
	return nil
}

// Run receives updates until ctx is cancelled.
func (b *TelegramBot) Run(ctx context.Context) {
	go b.digestLoop(ctx)

	var offset int64
	for ctx.Err() == nil {
		updates, err := b.API.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var apiErr *telegram.APIError
			if errors.As(err, &apiErr) && apiErr.Code == 401 {
				log.Printf("telegram: the bot token is invalid — bot stopped")
				return
			}
			if errors.As(err, &apiErr) && apiErr.Code == 409 {
				log.Printf("telegram: another copy of the bot is running with the same token")
			} else {
				log.Printf("telegram: %v", err)
			}
			sleep(ctx, 3*time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message != nil {
				b.handleSafely(ctx, u.Message)
			}
		}
	}
}

// Wait blocks until all background sends are finished (used by tests).
func (b *TelegramBot) Wait() { b.wg.Wait() }

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// async runs fn in the background with its own timeout, so a slow Telegram
// never delays an HTTP response of the website.
func (b *TelegramBot) async(fn func(ctx context.Context)) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("telegram: background task panicked: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		fn(ctx)
	}()
}

// send delivers a message, splitting it at Telegram's 4096-character limit.
func (b *TelegramBot) send(ctx context.Context, chatID int64, html string) {
	for _, part := range splitMessage(html, 4000) {
		if err := b.API.SendMessage(ctx, chatID, part); err != nil {
			log.Printf("telegram: send to %d: %v", chatID, err)
			return
		}
	}
}

func splitMessage(text string, limit int) []string {
	var parts []string
	for len([]rune(text)) > limit {
		runes := []rune(text)
		cut := strings.LastIndex(string(runes[:limit]), "\n")
		if cut <= 0 {
			cut = len(string(runes[:limit]))
		}
		parts = append(parts, text[:cut])
		text = strings.TrimLeft(text[cut:], "\n")
	}
	return append(parts, text)
}

// ---------- Users and settings ----------

type botUser struct {
	ID     int
	Name   string
	Role   string
	Digest bool
}

// linkedUser returns the site account linked to a Telegram user, or nil.
func (b *TelegramBot) linkedUser(ctx context.Context, telegramID int64) (*botUser, error) {
	var u botUser
	err := b.DB.QueryRow(ctx,
		`SELECT u.id, u.name, u.role, tl.digest
		 FROM telegram_links tl JOIN users u ON u.id = tl.user_id
		 WHERE tl.telegram_id = $1`, telegramID).Scan(&u.ID, &u.Name, &u.Role, &u.Digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

func (b *TelegramBot) setting(ctx context.Context, key string) (string, bool) {
	var value string
	err := b.DB.QueryRow(ctx, `SELECT value FROM bot_settings WHERE key = $1`, key).Scan(&value)
	return value, err == nil
}

func (b *TelegramBot) setSetting(ctx context.Context, key, value string) error {
	_, err := b.DB.Exec(ctx,
		`INSERT INTO bot_settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

func (b *TelegramBot) kitchenChat(ctx context.Context) (int64, bool) {
	value, ok := b.setting(ctx, "kitchen_chat")
	if !ok {
		return 0, false
	}
	var id int64
	_, err := fmt.Sscan(value, &id)
	return id, err == nil
}

// ---------- Incoming messages ----------

func (b *TelegramBot) handleSafely(ctx context.Context, m *telegram.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("telegram: message handler panicked: %v", r)
			b.send(ctx, m.Chat.ID, "Что-то пошло не так. Попробуйте ещё раз.")
		}
	}()
	b.handle(ctx, m)
}

var roleNames = map[string]string{
	"customer": "гость", "waiter": "официант", "cook": "повар", "admin": "администратор", "owner": "владелец",
}

func (b *TelegramBot) handle(ctx context.Context, m *telegram.Message) {
	text := strings.TrimSpace(m.Text)
	if text == "" || m.From == nil {
		return
	}
	user, err := b.linkedUser(ctx, m.From.ID)
	if err != nil {
		log.Printf("telegram: %v", err)
		b.send(ctx, m.Chat.ID, "Сервис временно недоступен, попробуйте позже.")
		return
	}

	if !strings.HasPrefix(text, "/") {
		b.freeText(ctx, m, user, text)
		return
	}

	command, arg, forUs := parseCommand(text, b.Username)
	if !forUs {
		return // a command for another bot in a group chat
	}
	reply := b.command(ctx, m, user, command, arg)
	if reply != "" {
		b.send(ctx, m.Chat.ID, reply)
	}
}

// parseCommand turns "/start abc" or "/kitchen@rms_bot" into command and argument.
func parseCommand(text, botName string) (command, arg string, forUs bool) {
	head, arg, _ := strings.Cut(text, " ")
	command = strings.ToLower(strings.TrimPrefix(head, "/"))
	if name, target, ok := strings.Cut(command, "@"); ok {
		if !strings.EqualFold(target, botName) {
			return "", "", false
		}
		command = name
	}
	return command, strings.TrimSpace(arg), true
}

// allowed returns "" when the user may run the command, otherwise the reason.
func allowed(user *botUser, roles ...string) string {
	if user == nil {
		return "Сначала привяжите аккаунт: сайт → Личный кабинет → «Подключить Telegram»."
	}
	if !contains(roles, user.Role) {
		names := make([]string, len(roles))
		for i, r := range roles {
			names[i] = roleNames[r]
		}
		return "Эта команда доступна только: " + strings.Join(names, ", ") + "."
	}
	return ""
}

func (b *TelegramBot) command(ctx context.Context, m *telegram.Message, user *botUser, command, arg string) string {
	private := m.Chat.Type == "private"

	switch command {
	case "start":
		if arg != "" {
			return b.link(ctx, m, arg)
		}
		return b.help(user)
	case "help":
		return b.help(user)
	case "menu":
		return b.menuText(ctx)

	case "orders":
		if why := allowed(user, "customer", "waiter", "cook", "admin", "owner"); why != "" {
			return why
		}
		return b.ordersText(ctx, user)

	case "today":
		if why := allowed(user, "owner", "admin"); why != "" {
			return why
		}
		text, err := b.digest(ctx)
		if err != nil {
			return "Не удалось собрать сводку."
		}
		return text
	case "forecast":
		if why := allowed(user, "owner", "admin"); why != "" {
			return why
		}
		return b.forecastText(ctx)
	case "stock":
		if why := allowed(user, "owner", "admin", "cook"); why != "" {
			return why
		}
		return b.stockText(ctx)
	case "promo":
		if why := allowed(user, "owner", "admin"); why != "" {
			return why
		}
		return b.promoText(ctx)

	case "digest":
		if why := allowed(user, "owner", "admin"); why != "" {
			return why
		}
		_, err := b.DB.Exec(ctx, `UPDATE telegram_links SET digest = NOT digest WHERE user_id = $1`, user.ID)
		if err != nil {
			return "Не удалось изменить настройку."
		}
		if user.Digest {
			return "🔕 Утренняя сводка выключена. Включить снова — /digest"
		}
		return fmt.Sprintf("🔔 Утренняя сводка включена: каждый день в %d:00.", b.DigestHour)

	case "kitchen":
		if why := allowed(user, "admin", "cook"); why != "" {
			return why
		}
		if err := b.setSetting(ctx, "kitchen_chat", fmt.Sprint(m.Chat.ID)); err != nil {
			return "Не удалось сохранить чат кухни."
		}
		return "👨‍🍳 Этот чат теперь чат кухни: сюда будут приходить новые заказы и заказы, принятые в работу.\nОтключить — /kitchen_off"
	case "kitchen_off":
		if why := allowed(user, "admin", "cook"); why != "" {
			return why
		}
		b.DB.Exec(ctx, `DELETE FROM bot_settings WHERE key = 'kitchen_chat'`)
		return "Чат кухни отключён."

	case "unlink":
		if user == nil {
			return "Аккаунт и так не привязан."
		}
		b.DB.Exec(ctx, `DELETE FROM telegram_links WHERE user_id = $1`, user.ID)
		return "Аккаунт отвязан. Уведомления больше не придут."

	case "new":
		if !private {
			return ""
		}
		b.mu.Lock()
		delete(b.chats, m.From.ID)
		b.mu.Unlock()
		return "🆕 Начали новый диалог с ассистентом."
	}
	return "Не знаю такой команды. Список команд — /help"
}

func (b *TelegramBot) help(user *botUser) string {
	var sb strings.Builder
	sb.WriteString("🍽 <b>RMS — бот ресторана</b>\n\n")
	if user == nil {
		sb.WriteString("/menu — меню и цены\n\n")
		sb.WriteString("Чтобы получать уведомления о заказах, привяжите аккаунт: " +
			"сайт → Личный кабинет → <b>«Подключить Telegram»</b>.")
		return sb.String()
	}
	sb.WriteString(fmt.Sprintf("Вы: <b>%s</b> (%s)\n\n", telegram.Escape(user.Name), roleNames[user.Role]))
	sb.WriteString("/menu — меню и цены\n/orders — мои заказы\n")
	switch user.Role {
	case "owner", "admin":
		sb.WriteString("/today — сводка на сегодня\n/forecast — прогноз на неделю\n/stock — склад\n/promo — что рекламировать\n/digest — утренняя сводка вкл/выкл\n")
		if user.Role == "admin" {
			sb.WriteString("/kitchen — сделать чат чатом кухни\n")
		}
		sb.WriteString("\n💬 Можно просто написать вопрос — ответит ИИ-ассистент.")
	case "cook":
		sb.WriteString("/stock — склад\n/kitchen — сделать чат чатом кухни (добавьте бота в группу кухни)\n")
	default:
		sb.WriteString("\nСтатусы ваших заказов будут приходить сюда автоматически.")
	}
	sb.WriteString("\n\n/unlink — отвязать аккаунт")
	return sb.String()
}

// ---------- Linking an account ----------

var linkCodePattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (b *TelegramBot) link(ctx context.Context, m *telegram.Message, code string) string {
	if m.Chat.Type != "private" {
		return "Привязка работает только в личном чате с ботом."
	}
	invalid := "Ссылка недействительна или устарела. Создайте новую: Личный кабинет → «Подключить Telegram»."
	if !linkCodePattern.MatchString(code) {
		return invalid
	}

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return "Сервис временно недоступен."
	}
	defer tx.Rollback(ctx)

	// The code is single-use: it is deleted in the same transaction.
	var userID int
	err = tx.QueryRow(ctx,
		`DELETE FROM telegram_link_codes WHERE code = $1 AND expires_at > NOW() RETURNING user_id`,
		code).Scan(&userID)
	if err != nil {
		return invalid
	}
	// One Telegram account ↔ one site account: move the link if it existed elsewhere.
	if _, err = tx.Exec(ctx, `DELETE FROM telegram_links WHERE telegram_id = $1 AND user_id <> $2`, m.From.ID, userID); err != nil {
		return "Сервис временно недоступен."
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO telegram_links (user_id, telegram_id, username) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO UPDATE
		 SET telegram_id = EXCLUDED.telegram_id, username = EXCLUDED.username, linked_at = CURRENT_TIMESTAMP`,
		userID, m.From.ID, m.From.Username)
	if err != nil || tx.Commit(ctx) != nil {
		return "Сервис временно недоступен."
	}

	user, err := b.linkedUser(ctx, m.From.ID)
	if err != nil || user == nil {
		return "Аккаунт привязан."
	}
	return fmt.Sprintf("✅ Готово, %s! Аккаунт привязан.\n\n", telegram.Escape(user.Name)) + b.help(user)
}

// ---------- Order notifications (OrderNotifier) ----------

var statusMessages = map[string]string{
	"confirmed": "✅ Заказ <b>#%d</b> принят. Скоро начнём готовить.",
	"preparing": "👨‍🍳 Заказ <b>#%d</b> готовится.",
	"ready":     "🔔 Заказ <b>#%d</b> готов — можно забирать!",
	"completed": "🙏 Заказ <b>#%d</b> выдан. Приятного аппетита!",
	"cancelled": "❌ Заказ <b>#%d</b> отменён.",
}

func (b *TelegramBot) loadOrder(ctx context.Context, id int) (*Order, error) {
	orders, err := (&OrderHandler{DB: b.DB}).loadOrders(ctx, orderSelect+`WHERE o.id = $1`, id)
	if err != nil || len(orders) == 0 {
		return nil, fmt.Errorf("order %d not found: %v", id, err)
	}
	return &orders[0], nil
}

func (b *TelegramBot) customerChat(ctx context.Context, userID int) (int64, bool) {
	var id int64
	err := b.DB.QueryRow(ctx, `SELECT telegram_id FROM telegram_links WHERE user_id = $1`, userID).Scan(&id)
	return id, err == nil
}

func orderLines(o *Order) string {
	var sb strings.Builder
	for _, item := range o.Items {
		sb.WriteString(fmt.Sprintf("• %s ×%d\n", telegram.Escape(item.Name), item.Quantity))
	}
	return sb.String()
}

func (b *TelegramBot) OrderCreated(orderID int) {
	b.async(func(ctx context.Context) {
		order, err := b.loadOrder(ctx, orderID)
		if err != nil {
			log.Printf("telegram: %v", err)
			return
		}
		if chat, ok := b.kitchenChat(ctx); ok {
			b.send(ctx, chat, fmt.Sprintf("🆕 <b>Новый заказ #%d</b> · %s\n%s👤 %s",
				order.ID, tenge(order.TotalPrice), orderLines(order), telegram.Escape(order.UserName)))
		}
		if chat, ok := b.customerChat(ctx, order.UserID); ok {
			b.send(ctx, chat, fmt.Sprintf("🧾 Заказ <b>#%d</b> оформлен · %s\n%sСтатус буду присылать сюда.",
				order.ID, tenge(order.TotalPrice), orderLines(order)))
		}
	})
}

func (b *TelegramBot) OrderStatusChanged(orderID int, status string) {
	b.async(func(ctx context.Context) {
		order, err := b.loadOrder(ctx, orderID)
		if err != nil {
			log.Printf("telegram: %v", err)
			return
		}
		if format, ok := statusMessages[status]; ok {
			if chat, ok := b.customerChat(ctx, order.UserID); ok {
				b.send(ctx, chat, fmt.Sprintf(format, order.ID))
			}
		}
		if chat, ok := b.kitchenChat(ctx); ok {
			switch status {
			case "confirmed":
				b.send(ctx, chat, fmt.Sprintf("👨‍🍳 <b>В работу: #%d</b>\n%s", order.ID, orderLines(order)))
			case "cancelled":
				b.send(ctx, chat, fmt.Sprintf("❌ Заказ <b>#%d</b> отменён.", order.ID))
			}
		}
	})
}

// ---------- Texts for commands ----------

var monthsGenitive = []string{"", "января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря"}
var weekdayShort = []string{"", "пн", "вт", "ср", "чт", "пт", "сб", "вс"}

func (b *TelegramBot) menuText(ctx context.Context) string {
	rows, err := b.DB.Query(ctx,
		`SELECT c.name, d.name, d.price
		 FROM dishes d JOIN categories c ON c.id = d.category_id
		 WHERE COALESCE(d.is_available, true)
		 ORDER BY c.id, d.id`)
	if err != nil {
		return "Не удалось загрузить меню."
	}
	defer rows.Close()

	var sb strings.Builder
	sb.WriteString("📋 <b>Меню</b>\n")
	current := ""
	for rows.Next() {
		var category, name string
		var price float64
		if rows.Scan(&category, &name, &price) != nil {
			continue
		}
		if category != current {
			current = category
			sb.WriteString("\n<b>" + telegram.Escape(category) + "</b>\n")
		}
		sb.WriteString(fmt.Sprintf("• %s — %s\n", telegram.Escape(name), tenge(price)))
	}
	sb.WriteString("\nЗаказать можно на сайте — статус придёт сюда.")
	return sb.String()
}

var statusLabels = map[string]string{
	"pending": "⏳ Ожидает", "confirmed": "✅ Принят", "preparing": "👨‍🍳 Готовится",
	"ready": "🔔 Готов", "completed": "🙏 Выдан", "cancelled": "❌ Отменён",
}

func (b *TelegramBot) ordersText(ctx context.Context, user *botUser) string {
	orders, err := (&OrderHandler{DB: b.DB}).loadOrders(ctx,
		orderSelect+`WHERE o.user_id = $1 ORDER BY o.created_at DESC LIMIT 5`, user.ID)
	if err != nil {
		return "Не удалось загрузить заказы."
	}
	if len(orders) == 0 {
		return "У вас пока нет заказов. Меню — /menu"
	}
	var sb strings.Builder
	sb.WriteString("🧾 <b>Последние заказы</b>\n\n")
	for _, o := range orders {
		sb.WriteString(fmt.Sprintf("<b>#%d</b> · %s · %s · %s\n",
			o.ID, o.CreatedAt.Local().Format("02.01 15:04"), statusLabels[o.Status], tenge(o.TotalPrice)))
	}
	return sb.String()
}

func (b *TelegramBot) forecastText(ctx context.Context) string {
	fc, err := b.Analytics.forecast(ctx, 8)
	if err != nil || !fc.Ready {
		return "Прогноз пока недоступен: нужно минимум 6 недель истории заказов."
	}
	var sb strings.Builder
	sb.WriteString("🔮 <b>Прогноз на 7 дней</b>\n\n")
	var orders, revenue float64
	for _, p := range fc.Forecast[1:] {
		date, _ := time.Parse("2006-01-02", p.Date)
		sb.WriteString(fmt.Sprintf("%s %s — ~%.0f (%.0f–%.0f)\n", weekdayShort[p.Weekday], date.Format("02.01"), p.Value, p.Low, p.High))
		orders += p.Value
		revenue += p.Revenue
	}
	sb.WriteString(fmt.Sprintf("\nИтого ~%.0f %s · ~%s\n", orders, ordersWord(orders), tenge(math.Round(revenue/1000)*1000)))
	sb.WriteString("\n<b>Больше всего порций:</b>\n")
	for i, d := range fc.Dishes {
		if i == 5 {
			break
		}
		sb.WriteString(fmt.Sprintf("• %s — ~%.0f\n", telegram.Escape(d.Name), d.NextWeek))
	}
	if fc.Backtest != nil {
		sb.WriteString(fmt.Sprintf("\n<i>Холт — Уинтерс, ошибка %.1f%% (наивный прогноз — %.1f%%)</i>",
			fc.Backtest.MAPEModel, fc.Backtest.MAPENaive))
	}
	return sb.String()
}

func (b *TelegramBot) stockProblems(ctx context.Context) (critical, low []inventoryRow, err error) {
	rows, err := b.Inventory.inventory(ctx)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return coverDays(rows[i]) < coverDays(rows[j]) })
	for _, r := range rows {
		switch r.Status {
		case "critical":
			critical = append(critical, r)
		case "low":
			low = append(low, r)
		}
	}
	return critical, low, nil
}

func coverDays(r inventoryRow) float64 {
	if r.DaysCover == nil {
		return math.Inf(1)
	}
	return *r.DaysCover
}

func stockLine(r inventoryRow) string {
	return fmt.Sprintf("• %s — %s %s, хватит на %s дн.\n", telegram.Escape(r.Name),
		strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", r.Stock), "0"), "."), telegram.Escape(r.Unit),
		strings.Replace(fmt.Sprintf("%.1f", coverDays(r)), ".", ",", 1))
}

func (b *TelegramBot) stockText(ctx context.Context) string {
	critical, low, err := b.stockProblems(ctx)
	if err != nil {
		return "Не удалось загрузить склад."
	}
	if len(critical)+len(low) == 0 {
		return "📦 <b>Склад</b>\n\n✅ Всего хватает минимум на 5 дней."
	}
	var sb strings.Builder
	sb.WriteString("📦 <b>Склад</b>\n")
	if len(critical) > 0 {
		sb.WriteString("\n🔴 <b>Заканчиваются</b> (меньше 2 дней)\n")
		for _, r := range critical {
			sb.WriteString(stockLine(r))
		}
	}
	if len(low) > 0 {
		sb.WriteString("\n🟡 <b>Мало</b> (2–5 дней)\n")
		for _, r := range low {
			sb.WriteString(stockLine(r))
		}
	}
	sb.WriteString("\nПлан закупки с учётом бюджета — в панели, раздел «Закупки».")
	return sb.String()
}

var recIcons = map[string]string{"promote": "📣", "price": "💰", "remove": "🗑", "combo": "🍱", "staff": "👨‍🍳", "trend": "📈", "forecast": "🔮"}

func (b *TelegramBot) promoText(ctx context.Context) string {
	recs, err := b.Analytics.recommendations(ctx, 30)
	if err != nil {
		return "Не удалось получить рекомендации."
	}
	var sb strings.Builder
	sb.WriteString("📣 <b>Что делать с меню</b> (за 30 дней)\n")
	n := 0
	for _, r := range recs {
		if r.Type != "promote" && r.Type != "combo" && r.Type != "price" {
			continue
		}
		sb.WriteString(fmt.Sprintf("\n%s <b>%s</b>\n%s\n", recIcons[r.Type], telegram.Escape(r.Title), telegram.Escape(r.Detail)))
		n++
	}
	if n == 0 {
		return "Пока нет рекомендаций: мало данных о продажах."
	}
	sb.WriteString("\n💬 Нужен текст для поста? Просто напишите: «напиши пост про …»")
	return sb.String()
}

// ---------- Morning digest ----------

func (b *TelegramBot) digest(ctx context.Context) (string, error) {
	now := time.Now()
	var yOrders, weekAgoOrders, todayOrders int
	var yRevenue, weekAgoRevenue, todayRevenue float64
	err := b.DB.QueryRow(ctx,
		`SELECT
		     count(*) FILTER (WHERE created_at >= current_date - 1 AND created_at < current_date),
		     COALESCE(sum(total_price) FILTER (WHERE created_at >= current_date - 1 AND created_at < current_date), 0),
		     count(*) FILTER (WHERE created_at >= current_date - 8 AND created_at < current_date - 7),
		     COALESCE(sum(total_price) FILTER (WHERE created_at >= current_date - 8 AND created_at < current_date - 7), 0),
		     count(*) FILTER (WHERE created_at >= current_date),
		     COALESCE(sum(total_price) FILTER (WHERE created_at >= current_date), 0)
		 FROM orders
		 WHERE status <> 'cancelled' AND created_at >= current_date - 8`,
	).Scan(&yOrders, &yRevenue, &weekAgoOrders, &weekAgoRevenue, &todayOrders, &todayRevenue)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("☀️ <b>Сводка RMS · %d %s</b>\n\n", now.Day(), monthsGenitive[now.Month()]))

	yesterday := now.AddDate(0, 0, -1)
	sb.WriteString(fmt.Sprintf("<b>Вчера</b> (%s): %d %s · %s", weekdayNames[isoWeekday(yesterday)],
		yOrders, ordersWord(float64(yOrders)), tenge(yRevenue)))
	if weekAgoRevenue > 0 {
		change := (yRevenue - weekAgoRevenue) / weekAgoRevenue * 100
		arrow := "▲"
		if change < 0 {
			arrow = "▼"
		}
		sb.WriteString(fmt.Sprintf("\n%s %+.0f%% к тому же дню неделю назад", arrow, change))
	}
	sb.WriteString("\n")

	if fc, err := b.Analytics.forecast(ctx, 2); err == nil && fc.Ready && len(fc.Forecast) > 0 {
		t := fc.Forecast[0]
		sb.WriteString(fmt.Sprintf("<b>Сегодня прогноз</b>: ~%.0f %s (%.0f–%.0f) · ~%s\n",
			t.Value, ordersWord(t.Value), t.Low, t.High, tenge(math.Round(t.Revenue/1000)*1000)))
	}
	if todayOrders > 0 {
		sb.WriteString(fmt.Sprintf("<b>Уже сегодня</b>: %d %s · %s\n", todayOrders, ordersWord(float64(todayOrders)), tenge(todayRevenue)))
	}

	if critical, low, err := b.stockProblems(ctx); err == nil && len(critical)+len(low) > 0 {
		names := []string{}
		for _, r := range append(critical, low...) {
			if len(names) == 4 {
				names = append(names, "…")
				break
			}
			names = append(names, telegram.Escape(r.Name))
		}
		icon := "🟡"
		if len(critical) > 0 {
			icon = "🔴"
		}
		sb.WriteString(fmt.Sprintf("\n%s <b>Склад</b>: докупить %s — /stock\n", icon, strings.Join(names, ", ")))
	}

	if recs, err := b.Analytics.recommendations(ctx, 30); err == nil {
		sb.WriteString("\n<b>Главное на сегодня</b>\n")
		n := 0
		for _, r := range recs {
			if r.Type == "forecast" || n == 3 {
				continue
			}
			sb.WriteString(fmt.Sprintf("%s %s\n", recIcons[r.Type], telegram.Escape(r.Title)))
			n++
		}
	}
	sb.WriteString("\nПодробнее: /forecast · /promo")
	return sb.String(), nil
}

// digestLoop sends the digest once a day at DigestHour. The date of the last
// digest is stored in the database, so a restart never sends it twice.
func (b *TelegramBot) digestLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		b.sendDigestIfDue(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *TelegramBot) sendDigestIfDue(ctx context.Context, now time.Time) bool {
	today := now.Format("2006-01-02")
	if now.Hour() < b.DigestHour {
		return false
	}
	if last, _ := b.setting(ctx, "digest_date"); last == today {
		return false
	}
	if err := b.setSetting(ctx, "digest_date", today); err != nil {
		return false
	}

	rows, err := b.DB.Query(ctx,
		`SELECT tl.telegram_id FROM telegram_links tl JOIN users u ON u.id = tl.user_id
		 WHERE tl.digest AND u.role IN ('owner', 'admin')`)
	if err != nil {
		return false
	}
	var chats []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			chats = append(chats, id)
		}
	}
	rows.Close()
	if len(chats) == 0 {
		return true
	}

	text, err := b.digest(ctx)
	if err != nil {
		log.Printf("telegram: digest: %v", err)
		return false
	}
	for _, chat := range chats {
		b.send(ctx, chat, text)
	}
	return true
}

// ---------- Free text → AI assistant ----------

var (
	mdBold   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdBullet = regexp.MustCompile(`(?m)^\s*[-•]\s+`)
)

// assistantHTML converts the assistant's light markdown to Telegram HTML.
func assistantHTML(text string) string {
	html := telegram.Escape(text)
	html = mdBold.ReplaceAllString(html, "<b>$1</b>")
	return mdBullet.ReplaceAllString(html, "• ")
}

func (b *TelegramBot) freeText(ctx context.Context, m *telegram.Message, user *botUser, text string) {
	if m.Chat.Type != "private" {
		return // in group chats the bot only reacts to commands
	}
	if user == nil || (user.Role != "owner" && user.Role != "admin") {
		b.send(ctx, m.Chat.ID, "Я понимаю команды — список: /help")
		return
	}
	if !b.Assistant.configured() {
		b.send(ctx, m.Chat.ID, "🤖 ИИ-ассистент ещё не подключён на сервере (нужен ANTHROPIC_API_KEY). "+
			"Пока можно пользоваться командами: /today /forecast /stock /promo")
		return
	}
	if len([]rune(text)) > 4000 {
		b.send(ctx, m.Chat.ID, "Слишком длинный вопрос — сократите до 4000 символов.")
		return
	}

	chat, from := m.Chat.ID, m.From.ID
	b.API.SendTyping(ctx, chat)

	// The model can think for a while: answer in the background so the bot keeps
	// receiving other messages.
	b.async(func(ctx context.Context) {
		b.mu.Lock()
		history := append(append([]assistantMessage{}, b.chats[from]...), assistantMessage{Role: "user", Content: text})
		b.mu.Unlock()
		if len(history) > 10 {
			history = history[len(history)-10:]
			if history[0].Role != "user" {
				history = history[1:]
			}
		}

		reply, tools, err := b.Assistant.answer(ctx, history)
		if err != nil {
			_, msg := assistantErrorStatus(err)
			log.Printf("telegram: assistant: %v", err)
			b.send(ctx, chat, "Не получилось получить ответ ("+telegram.Escape(msg)+"). Попробуйте позже.")
			return
		}

		b.mu.Lock()
		b.chats[from] = append(history, assistantMessage{Role: "assistant", Content: reply})
		b.mu.Unlock()

		html := assistantHTML(reply)
		if len(tools) > 0 {
			html += "\n\n<i>📊 Данные: " + telegram.Escape(strings.Join(tools, " · ")) + "</i>"
		}
		b.send(ctx, chat, html)
	})
}
