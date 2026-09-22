// Command seed fills the database with a realistic history of orders
// so the analytics module has something to analyse.
//
//	go run ./cmd/seed               # 180 days of orders (keeps existing data)
//	go run ./cmd/seed -reset        # delete previously generated data first
//	go run ./cmd/seed -days 365 -rate 80
//
// The data contains deliberate patterns (weekends, lunch/dinner peaks,
// growth trend, summer drinks, "pizza + cola", "burger + fries") so the
// analytics can be checked: it must discover exactly these patterns.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"restaurant-management/database"
)

const seedEmailDomain = "@seed.rms"

// ---------- Menu ----------

type menuItem struct {
	Name        string
	Category    string
	Price       float64
	Cost        float64
	Weight      float64 // how often it is chosen as a main dish (0 = only as an add-on)
	Description string
}

// Designed so that menu engineering finds all four classes:
// stars (Пепперони, Филадельфия), plowhorses (Чизбургер, drinks),
// puzzles (Дракон, Четыре сыра), dogs (Наггетсы, Ролл с угрём, Лимонад манго).
var menu = []menuItem{
	{"Пепперони", "Пицца", 3200, 1050, 14, "Томатный соус, моцарелла и острая пепперони на тонком тесте."},
	{"Маргарита", "Пицца", 2600, 700, 9, "Томаты, моцарелла, свежий базилик и оливковое масло."},
	{"Четыре сыра", "Пицца", 3500, 1300, 2.2, "Моцарелла, горгонзола, пармезан и чеддер на сливочном соусе."},
	{"Барбекю", "Пицца", 3400, 1350, 5, "Курица, бекон, красный лук и соус барбекю."},

	{"Филадельфия", "Суши", 3900, 1600, 11, "Лосось, сливочный сыр, огурец и рис. 8 штук."},
	{"Калифорния", "Суши", 3400, 1650, 7, "Краб, авокадо, огурец и икра масаго. 8 штук."},
	{"Дракон", "Суши", 4200, 1400, 1.8, "Угорь, авокадо, сливочный сыр и соус унаги."},
	{"Ролл с угрём", "Суши", 3600, 2400, 1.4, "Запечённый ролл с угрём и сырной шапкой."},

	{"Чизбургер", "Бургеры", 2400, 1400, 13, "Говяжья котлета, чеддер, маринованные огурцы и фирменный соус."},
	{"Бургер BBQ", "Бургеры", 2900, 1550, 7, "Двойная котлета, бекон, луковые кольца и соус барбекю."},
	{"Наггетсы", "Бургеры", 1500, 850, 1.3, "Куриные наггетсы, 9 штук, с соусом на выбор."},
	{"Картофель фри", "Бургеры", 900, 250, 0, "Хрустящий картофель с морской солью."},

	{"Цезарь", "Салаты", 2200, 750, 5, "Курица гриль, романо, пармезан, гренки и соус цезарь."},
	{"Греческий", "Салаты", 1900, 800, 1.6, "Томаты, огурцы, перец, маслины и фета."},

	{"Чизкейк", "Десерты", 1600, 450, 0, "Нью-Йорк чизкейк с ягодным соусом."},
	{"Тирамису", "Десерты", 1800, 500, 0, "Маскарпоне, савоярди и эспрессо."},

	{"Кока-кола", "Напитки", 700, 180, 0, "0,5 л, со льдом."},
	{"Мохито", "Напитки", 1200, 250, 0, "Безалкогольный: лайм, мята, тростниковый сахар и содовая."},
	{"Лимонад манго", "Напитки", 1100, 450, 0, "Домашний лимонад из пюре манго и маракуйи."},
	{"Капучино", "Напитки", 900, 200, 0, "Двойной эспрессо и нежная молочная пенка."},
}

// ---------- Hidden patterns ----------

// Orders per weekday relative to average (index = time.Weekday, 0 = Sunday).
var weekdayFactor = [7]float64{1.4, 0.8, 0.85, 0.9, 1.0, 1.3, 1.55}

// Relative load for every hour the restaurant is open (11:00–23:00).
var hourWeights = map[int]float64{
	11: 3, 12: 8, 13: 10, 14: 7, 15: 3, 16: 3, 17: 5,
	18: 8, 19: 11, 20: 10, 21: 6, 22: 3,
}

// Summer is busier and people drink more cold drinks.
func summerFactor(t time.Time) float64 {
	// 1.0 in winter … ~1.25 in mid-July
	day := float64(t.YearDay())
	return 1 + 0.125*(1+math.Cos(2*math.Pi*(day-196)/365))
}

type rule struct {
	If     string // category or dish name of a main dish
	Then   string // dish name added to the order
	Chance float64
}

// Association rules the basket analysis must rediscover.
var addOnRules = []rule{
	{"Пицца", "Кока-кола", 0.45},
	{"Бургеры", "Картофель фри", 0.65},
	{"Бургеры", "Кока-кола", 0.3},
	{"Суши", "Мохито", 0.3},
	{"Салаты", "Капучино", 0.25},
	{"Пепперони", "Тирамису", 0.12},
	{"Маргарита", "Тирамису", 0.12},
	{"Салаты", "Чизкейк", 0.2},
}

func main() {
	days := flag.Int("days", 180, "how many days of history to generate")
	rate := flag.Float64("rate", 55, "average orders per day at the start of the period")
	growth := flag.Float64("growth", 0.25, "total growth of orders over the period (0.25 = +25%)")
	customers := flag.Int("customers", 250, "number of generated customers")
	seed := flag.Uint64("seed", 42, "random seed (same seed = same data)")
	reset := flag.Bool("reset", false, "delete previously generated customers and their orders first")
	flag.Parse()

	ctx := context.Background()
	db, err := database.Connect()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rng := rand.New(rand.NewPCG(*seed, *seed^0x9e3779b97f4a7c15))

	if *reset {
		must(resetSeedData(ctx, db))
	}

	dishes, err := ensureMenu(ctx, db)
	must(err)
	must(ensureStaff(ctx, db))
	userIDs, err := ensureCustomers(ctx, db, *customers)
	must(err)

	start := time.Now()
	orders, items, revenue, err := generateOrders(ctx, db, rng, dishes, userIDs, *days, *rate, *growth)
	must(err)

	fmt.Printf("✓ Сгенерировано за %s: %d заказов, %d позиций, выручка %.0f ₸\n",
		time.Since(start).Round(time.Millisecond), orders, items, revenue)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// ---------- Setup ----------

func resetSeedData(ctx context.Context, db *pgxpool.Pool) error {
	// order_items are removed by ON DELETE CASCADE
	tag, err := db.Exec(ctx,
		`DELETE FROM orders WHERE user_id IN (SELECT id FROM users WHERE email LIKE '%' || $1)`, seedEmailDomain)
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `DELETE FROM users WHERE email LIKE '%' || $1`, seedEmailDomain); err != nil {
		return err
	}
	fmt.Printf("✓ Удалено сгенерированных заказов: %d\n", tag.RowsAffected())
	return nil
}

type dishInfo struct {
	ID       int
	menuItem menuItem
}

// ensureMenu creates missing categories and dishes. Existing dishes are kept
// as the admin left them; only an empty cost price is filled in.
func ensureMenu(ctx context.Context, db *pgxpool.Pool) (map[string]dishInfo, error) {
	dishes := map[string]dishInfo{}
	created := 0

	for _, m := range menu {
		var categoryID int
		err := db.QueryRow(ctx,
			`INSERT INTO categories (name) VALUES ($1)
			 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
			 RETURNING id`, m.Category).Scan(&categoryID)
		if err != nil {
			return nil, fmt.Errorf("category %s: %w", m.Category, err)
		}

		var id int
		var price float64
		err = db.QueryRow(ctx, `SELECT id, price FROM dishes WHERE name = $1 ORDER BY id LIMIT 1`, m.Name).Scan(&id, &price)
		switch {
		case err == pgx.ErrNoRows:
			err = db.QueryRow(ctx,
				`INSERT INTO dishes (name, description, price, cost_price, category_id, is_available)
				 VALUES ($1, $2, $3, $4, $5, true) RETURNING id`,
				m.Name, m.Description, m.Price, m.Cost, categoryID).Scan(&id)
			created++
		case err == nil:
			m.Price = price // orders use the real current price
			_, err = db.Exec(ctx, `UPDATE dishes SET cost_price = $1 WHERE id = $2 AND cost_price = 0`, m.Cost, id)
		}
		if err != nil {
			return nil, fmt.Errorf("dish %s: %w", m.Name, err)
		}
		dishes[m.Name] = dishInfo{ID: id, menuItem: m}
	}

	fmt.Printf("✓ Меню: %d блюд (новых: %d)\n", len(dishes), created)
	return dishes, nil
}

// ensureStaff creates demo accounts for every staff role.
func ensureStaff(ctx context.Context, db *pgxpool.Pool) error {
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	staff := [][3]string{
		{"Официант Айдар", "waiter@rms.kz", "waiter"},
		{"Повар Мадина", "cook@rms.kz", "cook"},
		{"Владелец", "owner@rms.kz", "owner"},
	}
	for _, s := range staff {
		_, err := db.Exec(ctx,
			`INSERT INTO users (name, email, password_hash, role) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (email) DO NOTHING`, s[0], s[1], string(hash), s[2])
		if err != nil {
			return err
		}
	}
	fmt.Println("✓ Демо-сотрудники: waiter@rms.kz, cook@rms.kz, owner@rms.kz (пароль password123)")
	return nil
}

func ensureCustomers(ctx context.Context, db *pgxpool.Pool, count int) ([]int, error) {
	// Generated customers cannot log in: the hash is of a random, discarded password.
	hash, err := bcrypt.GenerateFromPassword([]byte(fmt.Sprint(time.Now().UnixNano())), bcrypt.MinCost)
	if err != nil {
		return nil, err
	}

	firstNames := []string{"Алия", "Данияр", "Аружан", "Нурлан", "Дана", "Ерлан", "Камила", "Тимур",
		"Айгерим", "Санжар", "Мария", "Иван", "Асель", "Руслан", "Жанна", "Арман"}

	for i := 1; i <= count; i++ {
		_, err := db.Exec(ctx,
			`INSERT INTO users (name, email, password_hash, role) VALUES ($1, $2, $3, 'customer')
			 ON CONFLICT (email) DO NOTHING`,
			firstNames[i%len(firstNames)], fmt.Sprintf("guest%03d%s", i, seedEmailDomain), string(hash))
		if err != nil {
			return nil, err
		}
	}

	rows, err := db.Query(ctx, `SELECT id FROM users WHERE email LIKE '%' || $1 ORDER BY id`, seedEmailDomain)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err == nil {
		fmt.Printf("✓ Клиенты: %d\n", len(ids))
	}
	return ids, err
}

// ---------- Orders ----------

type genItem struct {
	dishID   int
	quantity int
	price    float64
	cost     float64
}

type genOrder struct {
	userID    int
	status    string
	createdAt time.Time
	items     []genItem
}

func weightedPick[T any](rng *rand.Rand, items []T, weight func(T) float64) T {
	var total float64
	for _, it := range items {
		total += weight(it)
	}
	r := rng.Float64() * total
	for _, it := range items {
		r -= weight(it)
		if r <= 0 {
			return it
		}
	}
	return items[len(items)-1]
}

// poisson draws a random count with the given mean (Knuth's algorithm; fine for mean < 200).
func poisson(rng *rand.Rand, mean float64) int {
	l := math.Exp(-mean)
	k, p := 0, 1.0
	for {
		p *= rng.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

func quantity(rng *rand.Rand) int {
	switch r := rng.Float64(); {
	case r < 0.85:
		return 1
	case r < 0.98:
		return 2
	default:
		return 3
	}
}

// statusFor gives old orders a final status and today's orders a status
// depending on how long ago they were placed, so the staff queue is not empty.
func statusFor(rng *rand.Rand, created, now time.Time) string {
	age := now.Sub(created)
	if age > time.Hour {
		if rng.Float64() < 0.04 {
			return "cancelled"
		}
		return "completed"
	}
	switch {
	case age < 8*time.Minute:
		return "pending"
	case age < 15*time.Minute:
		return "confirmed"
	case age < 35*time.Minute:
		return "preparing"
	case age < 45*time.Minute:
		return "ready"
	default:
		return "completed"
	}
}

func generateOrders(ctx context.Context, db *pgxpool.Pool, rng *rand.Rand, dishes map[string]dishInfo,
	userIDs []int, days int, rate, growth float64) (int, int, float64, error) {

	// Iterate over slices, not maps: Go randomises map order, and the same
	// -seed must always produce the same data.
	var mains []dishInfo
	for _, m := range menu {
		if m.Weight > 0 {
			mains = append(mains, dishes[m.Name])
		}
	}
	hours := make([]int, 0, len(hourWeights))
	for h := range hourWeights {
		hours = append(hours, h)
	}
	sort.Ints(hours)

	// Regular customers order much more often than occasional ones (Zipf-like).
	customerWeight := func(i int) float64 { return 1 / math.Pow(float64(i+1), 0.8) }
	customerIdx := make([]int, len(userIDs))
	for i := range customerIdx {
		customerIdx[i] = i
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	var orders []genOrder

	for d := days - 1; d >= 0; d-- {
		day := today.AddDate(0, 0, -d)
		progress := float64(days-1-d) / float64(max(days-1, 1))
		mean := rate * (1 + growth*progress) * weekdayFactor[day.Weekday()] * summerFactor(day)
		count := poisson(rng, mean)

		for i := 0; i < count; i++ {
			hour := weightedPick(rng, hours, func(h int) float64 { return hourWeights[h] })
			created := day.Add(time.Duration(hour)*time.Hour + time.Duration(rng.IntN(3600))*time.Second)
			if created.After(now) {
				continue
			}

			o := genOrder{
				userID:    userIDs[weightedPick(rng, customerIdx, customerWeight)],
				createdAt: created,
				status:    statusFor(rng, created, now),
			}

			chosen := map[string]int{}
			var names []string
			add := func(name string, qty int) {
				if _, ok := chosen[name]; !ok {
					names = append(names, name)
				}
				chosen[name] += qty
			}

			mainsCount := 1
			if r := rng.Float64(); r < 0.05 {
				mainsCount = 3
			} else if r < 0.30 {
				mainsCount = 2
			}
			for m := 0; m < mainsCount; m++ {
				main := weightedPick(rng, mains, func(d dishInfo) float64 { return d.menuItem.Weight })
				add(main.menuItem.Name, quantity(rng))

				for _, r := range addOnRules {
					if (r.If == main.menuItem.Category || r.If == main.menuItem.Name) && rng.Float64() < r.Chance {
						add(r.Then, 1)
					}
				}
			}

			// Independent extras: a drink or a dessert "just because".
			drinkChance := 0.15 * summerFactor(day)
			if rng.Float64() < drinkChance {
				if rng.Float64() < 0.5*summerFactor(day)-0.2 {
					add("Лимонад манго", 1)
				} else {
					add([]string{"Мохито", "Капучино", "Кока-кола"}[rng.IntN(3)], 1)
				}
			}
			if rng.Float64() < 0.04 {
				add([]string{"Чизкейк", "Тирамису"}[rng.IntN(2)], 1)
			}

			for _, name := range names {
				info := dishes[name]
				o.items = append(o.items, genItem{info.ID, chosen[name], info.menuItem.Price, info.menuItem.Cost})
			}
			orders = append(orders, o)
		}
	}

	return insertOrders(ctx, db, orders)
}

// insertOrders writes everything in one transaction with COPY, the fastest
// way to load many rows into PostgreSQL. Order ids are reserved from the
// sequence first so that order_items can reference them.
func insertOrders(ctx context.Context, db *pgxpool.Pool, orders []genOrder) (int, int, float64, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `SELECT nextval('orders_id_seq')::int FROM generate_series(1, $1)`, len(orders))
	if err != nil {
		return 0, 0, 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return 0, 0, 0, err
	}

	var orderRows, itemRows [][]any
	var revenue float64
	for i, o := range orders {
		var total float64
		for _, it := range o.items {
			total += it.price * float64(it.quantity)
			itemRows = append(itemRows, []any{ids[i], it.dishID, it.quantity, it.price, it.cost})
		}
		if o.status != "cancelled" {
			revenue += total
		}
		orderRows = append(orderRows, []any{ids[i], o.userID, o.status, total, o.createdAt})
	}

	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"orders"},
		[]string{"id", "user_id", "status", "total_price", "created_at"},
		pgx.CopyFromRows(orderRows)); err != nil {
		return 0, 0, 0, fmt.Errorf("copy orders: %w", err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"order_items"},
		[]string{"order_id", "dish_id", "quantity", "price", "cost_price"},
		pgx.CopyFromRows(itemRows)); err != nil {
		return 0, 0, 0, fmt.Errorf("copy order items: %w", err)
	}

	return len(orderRows), len(itemRows), revenue, tx.Commit(ctx)
}
