package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AnalyticsHandler — the decision-support module.
// Heavy aggregation is done by PostgreSQL; classification and
// recommendation rules live here in Go so they are easy to explain.
type AnalyticsHandler struct {
	DB *pgxpool.Pool
}

func periodDays(c *gin.Context) int {
	return intQuery(c, "days", 30, 1, 365)
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// growth returns the change in percent, or nil when there is nothing to compare with.
func growth(current, previous float64) *float64 {
	if previous == 0 {
		return nil
	}
	g := round2((current - previous) / previous * 100)
	return &g
}

// ---------- Summary (KPI) ----------

type periodStats struct {
	Orders    int     `json:"orders"`
	Revenue   float64 `json:"revenue"`
	Margin    float64 `json:"margin"`
	ItemsSold int     `json:"items_sold"`
	AvgCheck  float64 `json:"avg_check"`
	Cancelled int     `json:"cancelled"`
}

func (h *AnalyticsHandler) stats(ctx context.Context, fromDaysAgo, toDaysAgo int) (periodStats, error) {
	var s periodStats
	err := h.DB.QueryRow(ctx,
		`WITH o AS (
		     SELECT id, status, total_price FROM orders
		     WHERE created_at >= NOW() - make_interval(days => $1)
		       AND created_at <  NOW() - make_interval(days => $2)
		 )
		 SELECT
		     count(*) FILTER (WHERE status <> 'cancelled'),
		     COALESCE(sum(total_price) FILTER (WHERE status <> 'cancelled'), 0),
		     count(*) FILTER (WHERE status = 'cancelled'),
		     COALESCE((SELECT sum((oi.price - oi.cost_price) * oi.quantity)
		               FROM order_items oi JOIN o ON o.id = oi.order_id
		               WHERE o.status <> 'cancelled'), 0),
		     COALESCE((SELECT sum(oi.quantity)
		               FROM order_items oi JOIN o ON o.id = oi.order_id
		               WHERE o.status <> 'cancelled'), 0)
		 FROM o`,
		fromDaysAgo, toDaysAgo,
	).Scan(&s.Orders, &s.Revenue, &s.Cancelled, &s.Margin, &s.ItemsSold)
	if s.Orders > 0 {
		s.AvgCheck = round2(s.Revenue / float64(s.Orders))
	}
	return s, err
}

// Summary — GET /api/admin/analytics/summary?days=30
// Current period compared with the previous period of the same length.
func (h *AnalyticsHandler) Summary(c *gin.Context) {
	days := periodDays(c)

	current, err := h.stats(c, days, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to calculate summary"})
		return
	}
	previous, err := h.stats(c, days*2, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to calculate summary"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"days":     days,
		"current":  current,
		"previous": previous,
		"growth": gin.H{
			"orders":    growth(float64(current.Orders), float64(previous.Orders)),
			"revenue":   growth(current.Revenue, previous.Revenue),
			"margin":    growth(current.Margin, previous.Margin),
			"avg_check": growth(current.AvgCheck, previous.AvgCheck),
		},
	})
}

// ---------- Sales by day ----------

type dayPoint struct {
	Date    string  `json:"date"`
	Orders  int     `json:"orders"`
	Revenue float64 `json:"revenue"`
}

// Sales — GET /api/admin/analytics/sales?days=30
// generate_series fills days without orders with zeros, so the chart has no gaps.
func (h *AnalyticsHandler) Sales(c *gin.Context) {
	rows, err := h.DB.Query(c,
		`SELECT to_char(d, 'YYYY-MM-DD'),
		        count(o.id),
		        COALESCE(sum(o.total_price), 0)
		 FROM generate_series(current_date - ($1::int - 1), current_date, interval '1 day') AS d
		 LEFT JOIN orders o
		        ON o.created_at >= d AND o.created_at < d + interval '1 day'
		       AND o.status <> 'cancelled'
		 GROUP BY d
		 ORDER BY d`,
		periodDays(c),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get sales"})
		return
	}
	defer rows.Close()

	points := []dayPoint{}
	for rows.Next() {
		var p dayPoint
		if err := rows.Scan(&p.Date, &p.Orders, &p.Revenue); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read sales"})
			return
		}
		points = append(points, p)
	}
	c.JSON(http.StatusOK, points)
}

// ---------- Load by weekday and hour ----------

type heatCell struct {
	Weekday int     `json:"weekday"` // 1 = Monday … 7 = Sunday
	Hour    int     `json:"hour"`
	Orders  float64 `json:"orders"` // average orders per such hour
}

func (h *AnalyticsHandler) heatmap(ctx context.Context, days int) ([]heatCell, error) {
	// Divide by the number of such weekdays in the period to get an average,
	// otherwise a period with five Fridays would look busier than one with four.
	rows, err := h.DB.Query(ctx,
		`WITH weeks AS (
		     SELECT extract(isodow FROM d)::int AS dow, count(*) AS n
		     FROM generate_series(current_date - ($1::int - 1), current_date, interval '1 day') AS d
		     GROUP BY 1
		 )
		 SELECT extract(isodow FROM o.created_at)::int AS dow,
		        extract(hour FROM o.created_at)::int AS hour,
		        count(*)::float / max(w.n)
		 FROM orders o
		 JOIN weeks w ON w.dow = extract(isodow FROM o.created_at)::int
		 WHERE o.status <> 'cancelled'
		   AND o.created_at >= current_date - ($1::int - 1)
		 GROUP BY 1, 2
		 ORDER BY 1, 2`,
		days,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cells := []heatCell{}
	for rows.Next() {
		var cell heatCell
		if err := rows.Scan(&cell.Weekday, &cell.Hour, &cell.Orders); err != nil {
			return nil, err
		}
		cell.Orders = round2(cell.Orders)
		cells = append(cells, cell)
	}
	return cells, rows.Err()
}

// Heatmap — GET /api/admin/analytics/heatmap?days=56
func (h *AnalyticsHandler) Heatmap(c *gin.Context) {
	cells, err := h.heatmap(c, periodDays(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get heatmap"})
		return
	}
	c.JSON(http.StatusOK, cells)
}

// ---------- Menu engineering (Kasavana & Smith) ----------
//
// Every dish is placed on two axes:
//   popularity — share of all items sold, compared with 70% of the "fair share" (1/N);
//   margin     — contribution margin per portion, compared with the menu average.
//
//              high margin     low margin
//   popular    STAR ⭐          PLOWHORSE 🐴
//   unpopular  PUZZLE ❓        DOG 🐶

type menuDish struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	Price       float64 `json:"price"`
	CostPrice   float64 `json:"cost_price"`
	IsAvailable bool    `json:"is_available"`
	Quantity    int     `json:"quantity"`
	Revenue     float64 `json:"revenue"`
	Margin      float64 `json:"margin"`
	UnitMargin  float64 `json:"unit_margin"`
	FoodCostPct float64 `json:"food_cost_pct"`
	Popularity  float64 `json:"popularity"` // share of items sold, %
	Class       string  `json:"class"`      // star | plowhorse | puzzle | dog
	Advice      string  `json:"advice"`
}

type menuReport struct {
	Days                int        `json:"days"`
	TotalItems          int        `json:"total_items"`
	PopularityThreshold float64    `json:"popularity_threshold"` // %
	MarginThreshold     float64    `json:"margin_threshold"`     // ₸ per portion
	Dishes              []menuDish `json:"dishes"`
}

func (h *AnalyticsHandler) menuEngineering(ctx context.Context, days int) (menuReport, error) {
	report := menuReport{Days: days, Dishes: []menuDish{}}

	rows, err := h.DB.Query(ctx,
		`WITH sold AS (
		     SELECT oi.dish_id,
		            sum(oi.quantity) AS qty,
		            sum(oi.price * oi.quantity) AS revenue,
		            sum((oi.price - oi.cost_price) * oi.quantity) AS margin
		     FROM order_items oi
		     JOIN orders o ON o.id = oi.order_id
		     WHERE o.status <> 'cancelled'
		       AND o.created_at >= NOW() - make_interval(days => $1)
		     GROUP BY oi.dish_id
		 )
		 SELECT d.id, d.name, c.name, d.price, d.cost_price, COALESCE(d.is_available, true),
		        COALESCE(s.qty, 0), COALESCE(s.revenue, 0), COALESCE(s.margin, 0)
		 FROM dishes d
		 JOIN categories c ON c.id = d.category_id
		 LEFT JOIN sold s ON s.dish_id = d.id
		 WHERE COALESCE(d.is_available, true) OR s.qty > 0
		 ORDER BY COALESCE(s.qty, 0) DESC`,
		days,
	)
	if err != nil {
		return report, err
	}
	defer rows.Close()

	var totalMargin float64
	for rows.Next() {
		var d menuDish
		if err := rows.Scan(&d.ID, &d.Name, &d.Category, &d.Price, &d.CostPrice, &d.IsAvailable,
			&d.Quantity, &d.Revenue, &d.Margin); err != nil {
			return report, err
		}
		if d.Quantity > 0 {
			d.UnitMargin = round2(d.Margin / float64(d.Quantity))
		} else {
			d.UnitMargin = round2(d.Price - d.CostPrice)
		}
		if d.Price > 0 {
			d.FoodCostPct = round2(d.CostPrice / d.Price * 100)
		}
		report.TotalItems += d.Quantity
		totalMargin += d.Margin
		report.Dishes = append(report.Dishes, d)
	}
	if err := rows.Err(); err != nil {
		return report, err
	}

	n := len(report.Dishes)
	if n == 0 || report.TotalItems == 0 {
		return report, nil
	}

	report.PopularityThreshold = round2(100.0 / float64(n) * 0.7)
	report.MarginThreshold = round2(totalMargin / float64(report.TotalItems))

	for i := range report.Dishes {
		d := &report.Dishes[i]
		d.Popularity = round2(float64(d.Quantity) / float64(report.TotalItems) * 100)
		popular := d.Popularity >= report.PopularityThreshold
		profitable := d.UnitMargin >= report.MarginThreshold

		switch {
		case popular && profitable:
			d.Class = "star"
			d.Advice = "Лидер меню: сохранить рецепт и цену, выделить в меню и на главной."
		case popular && !profitable:
			d.Class = "plowhorse"
			d.Advice = plowhorseAdvice(*d, report.MarginThreshold)
		case !popular && profitable:
			d.Class = "puzzle"
			d.Advice = "Выгодное, но его редко заказывают: продвигать — акция, фото, место в топе меню, рекомендация официанта."
		default:
			d.Class = "dog"
			d.Advice = "Мало продаж и низкая маржа: переработать рецепт или убрать из меню."
		}
	}
	return report, nil
}

// plowhorseAdvice suggests a price increase that brings the dish closer
// to the average margin, capped at 10% so regular guests don't notice a jump.
func plowhorseAdvice(d menuDish, marginThreshold float64) string {
	need := marginThreshold - d.UnitMargin
	pct := math.Min(need/d.Price*100, 10)
	pct = math.Max(math.Ceil(pct), 3)
	newPrice := math.Round(d.Price*(1+pct/100)/50) * 50 // round to 50 ₸
	return fmt.Sprintf(
		"Популярное, но маржа ниже средней: поднять цену на ~%.0f%% (до %.0f ₸) или снизить себестоимость (сейчас %.0f%% от цены).",
		pct, newPrice, d.FoodCostPct,
	)
}

// MenuEngineering — GET /api/admin/analytics/menu?days=30
func (h *AnalyticsHandler) MenuEngineering(c *gin.Context) {
	report, err := h.menuEngineering(c, periodDays(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to analyse menu"})
		return
	}
	c.JSON(http.StatusOK, report)
}

// ---------- Basket analysis (association rules) ----------
//
//   support    = orders with A and B / all orders
//   confidence = orders with A and B / orders with A     ("of those who took A, X% also took B")
//   lift       = confidence / share of orders with B     (>1 means A and B go together more than by chance)

type dishPair struct {
	DishA       int     `json:"dish_a"`
	NameA       string  `json:"name_a"`
	DishB       int     `json:"dish_b"`
	NameB       string  `json:"name_b"`
	Orders      int     `json:"orders"`
	Support     float64 `json:"support"`
	ConfidenceA float64 `json:"confidence_ab"` // P(B | A)
	ConfidenceB float64 `json:"confidence_ba"` // P(A | B)
	Lift        float64 `json:"lift"`
}

const pairsSQL = `
	WITH o AS (
	    SELECT id FROM orders
	    WHERE status <> 'cancelled' AND created_at >= NOW() - make_interval(days => $1)
	),
	items AS (
	    SELECT DISTINCT oi.order_id, oi.dish_id
	    FROM order_items oi JOIN o ON o.id = oi.order_id
	),
	total AS (SELECT count(*)::float AS n FROM o),
	single AS (SELECT dish_id, count(*)::float AS cnt FROM items GROUP BY dish_id),
	pairs AS (
	    SELECT a.dish_id AS a, b.dish_id AS b, count(*)::float AS cnt
	    FROM items a
	    JOIN items b ON a.order_id = b.order_id AND a.dish_id < b.dish_id
	    GROUP BY 1, 2
	)
	SELECT p.a, da.name, p.b, db.name, p.cnt::int,
	       p.cnt / t.n, p.cnt / sa.cnt, p.cnt / sb.cnt,
	       p.cnt * t.n / (sa.cnt * sb.cnt)
	FROM pairs p
	JOIN single sa ON sa.dish_id = p.a
	JOIN single sb ON sb.dish_id = p.b
	JOIN dishes da ON da.id = p.a
	JOIN dishes db ON db.id = p.b
	CROSS JOIN total t
	WHERE p.cnt / t.n >= 0.01 `

func scanPairs(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]dishPair, error) {
	pairs := []dishPair{}
	for rows.Next() {
		var p dishPair
		if err := rows.Scan(&p.DishA, &p.NameA, &p.DishB, &p.NameB, &p.Orders,
			&p.Support, &p.ConfidenceA, &p.ConfidenceB, &p.Lift); err != nil {
			return nil, err
		}
		p.Support = round2(p.Support * 100)
		p.ConfidenceA = round2(p.ConfidenceA * 100)
		p.ConfidenceB = round2(p.ConfidenceB * 100)
		p.Lift = round2(p.Lift)
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}

func (h *AnalyticsHandler) pairs(ctx context.Context, days, limit int) ([]dishPair, error) {
	rows, err := h.DB.Query(ctx, pairsSQL+`ORDER BY 9 DESC, 5 DESC LIMIT $2`, days, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPairs(rows)
}

// Pairs — GET /api/admin/analytics/pairs?days=90&limit=10
func (h *AnalyticsHandler) Pairs(c *gin.Context) {
	pairs, err := h.pairs(c, intQuery(c, "days", 90, 1, 365), intQuery(c, "limit", 10, 1, 50))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to analyse baskets"})
		return
	}
	c.JSON(http.StatusOK, pairs)
}

// DishPairs — GET /api/dishes/:id/pairs (public)
// "Often ordered together" for the dish page, based on real orders.
func (h *AnalyticsHandler) DishPairs(c *gin.Context) {
	id, ok := parseID(c, "dish")
	if !ok {
		return
	}

	rows, err := h.DB.Query(c,
		pairsSQL+`AND (p.a = $2 OR p.b = $2) AND p.cnt * t.n / (sa.cnt * sb.cnt) > 1
		ORDER BY 9 DESC LIMIT 20`,
		90, id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get recommendations"})
		return
	}
	defer rows.Close()

	pairs, err := scanPairs(rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get recommendations"})
		return
	}

	type suggestion struct {
		DishID     int     `json:"dish_id"`
		Confidence float64 `json:"confidence"` // % of orders with this dish that also had the suggestion
		Lift       float64 `json:"lift"`
	}
	result := []suggestion{}
	for _, p := range pairs {
		if p.DishA == id {
			result = append(result, suggestion{p.DishB, p.ConfidenceA, p.Lift})
		} else {
			result = append(result, suggestion{p.DishA, p.ConfidenceB, p.Lift})
		}
	}
	// For a customer "how often people take it together" matters more than lift.
	sort.Slice(result, func(i, j int) bool { return result[i].Confidence > result[j].Confidence })
	if len(result) > 3 {
		result = result[:3]
	}
	c.JSON(http.StatusOK, result)
}

// ---------- Recommendations: "what to do" ----------

type recommendation struct {
	Type     string `json:"type"`     // promote | price | remove | combo | staff | trend
	Priority int    `json:"priority"` // 1 = most important
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

var weekdayNames = []string{"", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота", "воскресенье"}

// Recommendations — GET /api/admin/analytics/recommendations?days=30
// Combines all analyses into a short list of concrete actions for the owner.
func (h *AnalyticsHandler) Recommendations(c *gin.Context) {
	days := periodDays(c)
	recs := []recommendation{}

	menu, err := h.menuEngineering(c, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build recommendations"})
		return
	}
	if menu.TotalItems == 0 {
		c.JSON(http.StatusOK, gin.H{"days": days, "items": recs, "generated_at": time.Now()})
		return
	}

	// 1. Puzzles with the highest margin → promote.
	var puzzles, plowhorses, dogs []menuDish
	for _, d := range menu.Dishes {
		switch d.Class {
		case "puzzle":
			puzzles = append(puzzles, d)
		case "plowhorse":
			plowhorses = append(plowhorses, d)
		case "dog":
			dogs = append(dogs, d)
		}
	}
	sort.Slice(puzzles, func(i, j int) bool { return puzzles[i].UnitMargin > puzzles[j].UnitMargin })
	for i, d := range puzzles {
		if i == 2 {
			break
		}
		recs = append(recs, recommendation{
			Type: "promote", Priority: 1,
			Title: "Прорекламировать «" + d.Name + "»",
			Detail: fmt.Sprintf("Маржа %.0f ₸ с порции — выше средней (%.0f ₸), но это лишь %.1f%% продаж. "+
				"Скидка 10–15%% в будни или место в топе меню окупится за счёт маржи.",
				d.UnitMargin, menu.MarginThreshold, d.Popularity),
		})
	}

	// 2. Plowhorses → raise the price a little.
	sort.Slice(plowhorses, func(i, j int) bool { return plowhorses[i].Quantity > plowhorses[j].Quantity })
	for i, d := range plowhorses {
		if i == 2 {
			break
		}
		recs = append(recs, recommendation{
			Type: "price", Priority: 2,
			Title:  "Пересмотреть цену «" + d.Name + "»",
			Detail: fmt.Sprintf("Продано %d порций за %d дн. %s", d.Quantity, days, d.Advice),
		})
	}

	// 3. Dogs → consider removing.
	for i, d := range dogs {
		if i == 2 {
			break
		}
		recs = append(recs, recommendation{
			Type: "remove", Priority: 3,
			Title: "Под вопросом: «" + d.Name + "»",
			Detail: fmt.Sprintf("Всего %.1f%% продаж и маржа %.0f ₸ (средняя %.0f ₸). %s",
				d.Popularity, d.UnitMargin, menu.MarginThreshold, d.Advice),
		})
	}

	// 4. Strongest pair → combo offer.
	if pairs, err := h.pairs(c, days, 1); err == nil && len(pairs) > 0 {
		p := pairs[0]
		recs = append(recs, recommendation{
			Type: "combo", Priority: 2,
			Title: "Комбо «" + p.NameA + " + " + p.NameB + "»",
			Detail: fmt.Sprintf("%.0f%% гостей, взявших «%s», берут и «%s» — в %.1f раза чаще случайного. "+
				"Комбо со скидкой 5–7%% поднимет средний чек.",
				p.ConfidenceA, p.NameA, p.NameB, p.Lift),
		})
	}

	// 5. Peak hour → staffing.
	if cells, err := h.heatmap(c, days); err == nil && len(cells) > 0 {
		peak := cells[0]
		for _, cell := range cells {
			if cell.Orders > peak.Orders {
				peak = cell
			}
		}
		recs = append(recs, recommendation{
			Type: "staff", Priority: 3,
			Title: fmt.Sprintf("Пик нагрузки: %s, %d:00–%d:00", weekdayNames[peak.Weekday], peak.Hour, peak.Hour+1),
			Detail: fmt.Sprintf("В среднем %.1f заказа за этот час. Поставить дополнительного повара и официанта на смену.",
				peak.Orders),
		})
	}

	// 6. Trend compared with the previous period.
	if cur, err := h.stats(c, days, 0); err == nil {
		if prev, err := h.stats(c, days*2, days); err == nil && prev.Revenue > 0 {
			g := (cur.Revenue - prev.Revenue) / prev.Revenue * 100
			title := fmt.Sprintf("Выручка растёт: +%.1f%%", g)
			detail := "Рост к прошлому периоду — увеличить закупки ходовых продуктов, чтобы не было стоп-листа."
			if g < 0 {
				title = fmt.Sprintf("Выручка снижается: %.1f%%", g)
				detail = "Падение к прошлому периоду — запустить акцию на «загадки» и проверить отзывы."
			}
			recs = append(recs, recommendation{Type: "trend", Priority: 1, Title: title, Detail: detail})
		}
	}

	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Priority < recs[j].Priority })
	c.JSON(http.StatusOK, gin.H{"days": days, "items": recs, "generated_at": time.Now()})
}
