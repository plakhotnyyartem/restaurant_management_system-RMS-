package handlers

import (
	"context"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// ---------- Holt-Winters (additive, weekly season) ----------
//
// The forecast is built from three components that are updated every day:
//
//   level  — "normal" number of orders right now
//   trend  — how much the level grows or falls per day
//   season — correction for the day of the week (Saturday +40, Monday −10 …)
//
//   forecast(t+h) = level + h·trend + season[weekday of t+h]
//
// α, β, γ say how quickly each component reacts to new data.
// They are chosen by grid search: the combination with the smallest
// one-step-ahead squared error on the history wins.

const seasonLength = 7

type hwParams struct {
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
	Gamma float64 `json:"gamma"`
}

type hwState struct {
	level, trend float64
	season       []float64
	n            int // length of the training series
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// holtWinters runs the model over y and returns its final state and the sum
// of squared one-step-ahead errors (used to compare parameter sets).
func holtWinters(y []float64, p hwParams) (hwState, float64) {
	m := seasonLength
	season := make([]float64, m)

	// Initial state from the first two weeks.
	level := mean(y[:m])
	trend := (mean(y[m:2*m]) - level) / float64(m)
	for i := 0; i < m; i++ {
		season[i] = y[i] - level
	}

	var sse float64
	for t := m; t < len(y); t++ {
		s := season[t%m]
		predicted := level + trend + s
		err := y[t] - predicted
		sse += err * err

		newLevel := p.Alpha*(y[t]-s) + (1-p.Alpha)*(level+trend)
		trend = p.Beta*(newLevel-level) + (1-p.Beta)*trend
		season[t%m] = p.Gamma*(y[t]-newLevel) + (1-p.Gamma)*s
		level = newLevel
	}

	return hwState{level: level, trend: trend, season: season, n: len(y)}, sse
}

// predict returns the forecast h days after the end of the training series (h ≥ 1).
func (s hwState) predict(h int) float64 {
	v := s.level + float64(h)*s.trend + s.season[(s.n-1+h)%seasonLength]
	return math.Max(v, 0)
}

// fitHoltWinters picks α, β, γ by grid search (minimum SSE).
func fitHoltWinters(y []float64) (hwState, hwParams) {
	best := math.Inf(1)
	var bestState hwState
	var bestParams hwParams

	for a := 0.05; a < 1; a += 0.05 {
		for _, b := range []float64{0, 0.01, 0.02, 0.05, 0.1, 0.2} {
			for g := 0.05; g < 1; g += 0.1 {
				p := hwParams{Alpha: a, Beta: b, Gamma: g}
				state, sse := holtWinters(y, p)
				if sse < best {
					best, bestState, bestParams = sse, state, p
				}
			}
		}
	}

	bestParams = hwParams{Alpha: round2(bestParams.Alpha), Beta: round2(bestParams.Beta), Gamma: round2(bestParams.Gamma)}
	return bestState, bestParams
}

// ---------- Loading daily series ----------

// dailyOrders returns the number of orders per day for the last `days` full days
// (today is excluded: it is not over yet and would look like a drop).
func (h *AnalyticsHandler) dailyOrders(ctx context.Context, days int) ([]string, []float64, error) {
	rows, err := h.DB.Query(ctx,
		`SELECT to_char(d, 'YYYY-MM-DD'), count(o.id)
		 FROM generate_series(current_date - $1::int, current_date - 1, interval '1 day') AS d
		 LEFT JOIN orders o
		        ON o.created_at >= d AND o.created_at < d + interval '1 day'
		       AND o.status <> 'cancelled'
		 GROUP BY d
		 ORDER BY d`,
		days,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var dates []string
	var values []float64
	for rows.Next() {
		var date string
		var count int
		if err := rows.Scan(&date, &count); err != nil {
			return nil, nil, err
		}
		dates = append(dates, date)
		values = append(values, float64(count))
	}
	return dates, values, rows.Err()
}

// historyDays returns how many full days of history exist (capped at 365).
func (h *AnalyticsHandler) historyDays(ctx context.Context) (int, error) {
	var days int
	err := h.DB.QueryRow(ctx,
		`SELECT COALESCE(current_date - min(created_at)::date, 0) FROM orders WHERE status <> 'cancelled'`,
	).Scan(&days)
	if days > 365 {
		days = 365
	}
	return days, err
}

type dishSeries struct {
	ID       int
	Name     string
	Category string
	Values   []float64
}

func (h *AnalyticsHandler) dailyDishSales(ctx context.Context, days int) ([]dishSeries, error) {
	rows, err := h.DB.Query(ctx,
		`WITH days AS (
		     SELECT generate_series(current_date - $1::int, current_date - 1, interval '1 day')::date AS d
		 ),
		 sold AS (
		     SELECT o.created_at::date AS d, oi.dish_id, sum(oi.quantity) AS qty
		     FROM order_items oi
		     JOIN orders o ON o.id = oi.order_id
		     WHERE o.status <> 'cancelled'
		       AND o.created_at >= current_date - $1::int
		       AND o.created_at < current_date
		     GROUP BY 1, 2
		 )
		 SELECT dish.id, dish.name, c.name, COALESCE(s.qty, 0)
		 FROM dishes dish
		 JOIN categories c ON c.id = dish.category_id
		 CROSS JOIN days
		 LEFT JOIN sold s ON s.dish_id = dish.id AND s.d = days.d
		 WHERE COALESCE(dish.is_available, true)
		 ORDER BY dish.id, days.d`,
		days,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []dishSeries
	for rows.Next() {
		var id int
		var name, category string
		var qty float64
		if err := rows.Scan(&id, &name, &category, &qty); err != nil {
			return nil, err
		}
		if len(result) == 0 || result[len(result)-1].ID != id {
			result = append(result, dishSeries{ID: id, Name: name, Category: category})
		}
		last := &result[len(result)-1]
		last.Values = append(last.Values, qty)
	}
	return result, rows.Err()
}

// ---------- Forecast report ----------

type forecastPoint struct {
	Date    string  `json:"date"`
	Weekday int     `json:"weekday"` // 1 = Monday … 7 = Sunday
	Value   float64 `json:"value"`
	Low     float64 `json:"low"`  // 80% interval
	High    float64 `json:"high"` // 80% interval
	Revenue float64 `json:"revenue"`
}

type actualPoint struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

type backtest struct {
	Days        int     `json:"days"`
	MAPEModel   float64 `json:"mape_model"` // mean absolute percentage error, %
	MAPENaive   float64 `json:"mape_naive"` // "same weekday last week"
	RMSE        float64 `json:"rmse"`
	Improvement float64 `json:"improvement"` // how much smaller the model error is than naive, %
}

type dishForecast struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`
	Category  string  `json:"category"`
	Tomorrow  float64 `json:"tomorrow"`
	NextWeek  float64 `json:"next_week"`
	LastWeek  float64 `json:"last_week"`
	ChangePct float64 `json:"change_pct"`
}

type forecastReport struct {
	Ready    bool            `json:"ready"`
	Message  string          `json:"message,omitempty"`
	Method   string          `json:"method"`
	Params   hwParams        `json:"params"`
	Trained  int             `json:"trained_days"`
	AvgCheck float64         `json:"avg_check"`
	History  []actualPoint   `json:"history"`
	Forecast []forecastPoint `json:"forecast"`
	Backtest *backtest       `json:"backtest,omitempty"`
	Dishes   []dishForecast  `json:"dishes"`
}

const backtestDays = 28

func isoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

func mape(actual, predicted []float64) float64 {
	var sum float64
	var n int
	for i := range actual {
		if actual[i] > 0 {
			sum += math.Abs(actual[i]-predicted[i]) / actual[i]
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n) * 100
}

func (h *AnalyticsHandler) forecast(ctx context.Context, horizon int) (forecastReport, error) {
	report := forecastReport{
		Method:   "Холт — Уинтерс (аддитивная модель, недельная сезонность)",
		History:  []actualPoint{},
		Forecast: []forecastPoint{},
		Dishes:   []dishForecast{},
	}

	days, err := h.historyDays(ctx)
	if err != nil {
		return report, err
	}
	// Need two weeks to initialise the model plus a hold-out month to test it.
	if days < 2*seasonLength+backtestDays {
		report.Message = "Для прогноза нужно минимум 6 недель истории заказов."
		return report, nil
	}

	dates, y, err := h.dailyOrders(ctx, days)
	if err != nil {
		return report, err
	}

	// 1. Backtest the way the forecast is really used: every week the model is
	//    retrained on everything known so far and predicts the next 7 days
	//    (rolling origin, 4 weeks). The baseline is the naive forecast
	//    "same weekday last week", which the model has to beat.
	var actual, predicted, naive []float64
	var sq float64
	for fold := 0; fold < backtestDays/seasonLength; fold++ {
		cut := len(y) - backtestDays + fold*seasonLength
		train := y[:cut]
		state, _ := fitHoltWinters(train)
		for i := 0; i < seasonLength; i++ {
			a := y[cut+i]
			p := state.predict(i + 1)
			actual = append(actual, a)
			predicted = append(predicted, p)
			naive = append(naive, train[len(train)-seasonLength+i])
			sq += (a - p) * (a - p)
		}
	}
	rmse := math.Sqrt(sq / float64(len(actual)))
	bt := &backtest{
		Days:      backtestDays,
		MAPEModel: round2(mape(actual, predicted)),
		MAPENaive: round2(mape(actual, naive)),
		RMSE:      round2(rmse),
	}
	if bt.MAPENaive > 0 {
		bt.Improvement = round2((bt.MAPENaive - bt.MAPEModel) / bt.MAPENaive * 100)
	}
	report.Backtest = bt

	// 2. Final model on the whole history.
	state, params := fitHoltWinters(y)
	report.Params = params
	report.Trained = len(y)
	report.Ready = true

	// Average check of the last 4 weeks turns the order forecast into revenue.
	if err := h.DB.QueryRow(ctx,
		`SELECT COALESCE(avg(total_price), 0) FROM orders
		 WHERE status <> 'cancelled' AND created_at >= current_date - 28 AND created_at < current_date`,
	).Scan(&report.AvgCheck); err != nil {
		return report, err
	}
	report.AvgCheck = round2(report.AvgCheck)

	for i := max(0, len(y)-42); i < len(y); i++ {
		report.History = append(report.History, actualPoint{Date: dates[i], Value: y[i]})
	}

	// 80% interval: ±1.28 × RMSE measured on the hold-out month.
	today := time.Now()
	for hDay := 1; hDay <= horizon; hDay++ {
		date := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, hDay-1)
		v := state.predict(hDay)
		report.Forecast = append(report.Forecast, forecastPoint{
			Date:    date.Format("2006-01-02"),
			Weekday: isoWeekday(date),
			Value:   math.Round(v),
			Low:     math.Round(math.Max(v-1.28*rmse, 0)),
			High:    math.Round(v + 1.28*rmse),
			Revenue: math.Round(v * report.AvgCheck),
		})
	}

	// 3. The same model for every dish: portions tomorrow and over the next 7 days.
	series, err := h.dailyDishSales(ctx, days)
	if err != nil {
		return report, err
	}
	for _, s := range series {
		dishState, _ := fitHoltWinters(s.Values)
		// Day 1 of the forecast is today; "tomorrow" is day 2.
		var week float64
		for d := 2; d <= 8; d++ {
			week += dishState.predict(d)
		}
		var lastWeek float64
		for _, v := range s.Values[len(s.Values)-seasonLength:] {
			lastWeek += v
		}
		df := dishForecast{
			ID:       s.ID,
			Name:     s.Name,
			Category: s.Category,
			Tomorrow: math.Round(dishState.predict(2)),
			NextWeek: math.Round(week),
			LastWeek: lastWeek,
		}
		if lastWeek > 0 {
			df.ChangePct = round2((week - lastWeek) / lastWeek * 100)
		}
		report.Dishes = append(report.Dishes, df)
	}
	sort.Slice(report.Dishes, func(i, j int) bool { return report.Dishes[i].NextWeek > report.Dishes[j].NextWeek })

	return report, nil
}

// Forecast — GET /api/admin/analytics/forecast?horizon=14
func (h *AnalyticsHandler) Forecast(c *gin.Context) {
	report, err := h.forecast(c, intQuery(c, "horizon", 14, 1, 28))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build forecast"})
		return
	}
	c.JSON(http.StatusOK, report)
}
