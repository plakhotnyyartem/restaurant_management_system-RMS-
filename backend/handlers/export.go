package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// ---------- Excel export ----------
//
// Every report the panel shows can be downloaded as an .xlsx workbook. The
// numbers come from the same functions as the pages, so the file always
// matches what the owner sees on screen.

type ExportHandler struct {
	Analytics *AnalyticsHandler
	Inventory *InventoryHandler
	Orders    *OrderHandler
}

var orderStatusRu = map[string]string{
	"pending": "Ожидает", "confirmed": "Принят", "preparing": "Готовится",
	"ready": "Готов к выдаче", "completed": "Выполнен", "cancelled": "Отменён",
}

var menuClassRu = map[string]string{
	"star": "Звезда", "plowhorse": "Рабочая лошадка", "puzzle": "Загадка", "dog": "Собака",
}

var inventoryStatusRu = map[string]string{
	"ok": "Норма", "low": "Мало", "critical": "Критично", "unused": "Не используется",
}

// Column formats.
const (
	colText = iota
	colInt
	colMoney
	colNumber // two decimals: kilograms, litres
	colPct    // the value is already in percent: 45.5 → «45,5 %»
	colDate
)

type column struct {
	title string
	width float64
	kind  int
}

// workbook wraps excelize with the few things every sheet needs:
// a styled header row, number formats, frozen header and an autofilter.
type workbook struct {
	f      *excelize.File
	header int
	styles map[int]int
	sheets int
}

func newWorkbook() (*workbook, error) {
	f := excelize.NewFile()
	b := &workbook{f: f, styles: map[int]int{}}
	var err error
	b.header, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"1F6FAE"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
	})
	if err != nil {
		return nil, err
	}
	formats := map[int]string{
		colInt:    "#,##0",
		colMoney:  `#,##0 "₸"`,
		colNumber: "#,##0.00",
		colPct:    `0.0" %"`,
		colDate:   "dd.mm.yyyy hh:mm",
	}
	for kind, format := range formats {
		fmtCopy := format
		id, err := f.NewStyle(&excelize.Style{CustomNumFmt: &fmtCopy})
		if err != nil {
			return nil, err
		}
		b.styles[kind] = id
	}
	return b, nil
}

// sheet adds a sheet with a header row and the data rows.
func (b *workbook) sheet(name string, cols []column, rows [][]any) error {
	f := b.f
	if b.sheets == 0 {
		if err := f.SetSheetName("Sheet1", name); err != nil {
			return err
		}
	} else if _, err := f.NewSheet(name); err != nil {
		return err
	}
	b.sheets++

	header := make([]any, len(cols))
	for i, c := range cols {
		header[i] = c.title
		colName, _ := excelize.ColumnNumberToName(i + 1)
		if err := f.SetColWidth(name, colName, colName, c.width); err != nil {
			return err
		}
		if style, ok := b.styles[c.kind]; ok && len(rows) > 0 {
			if err := f.SetCellStyle(name, colName+"2", fmt.Sprintf("%s%d", colName, len(rows)+1), style); err != nil {
				return err
			}
		}
	}
	if err := f.SetSheetRow(name, "A1", &header); err != nil {
		return err
	}
	last, _ := excelize.ColumnNumberToName(len(cols))
	if err := f.SetCellStyle(name, "A1", last+"1", b.header); err != nil {
		return err
	}
	if err := f.SetRowHeight(name, 1, 30); err != nil {
		return err
	}
	for i, row := range rows {
		r := row
		if err := f.SetSheetRow(name, fmt.Sprintf("A%d", i+2), &r); err != nil {
			return err
		}
	}
	if err := f.SetPanes(name, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	if len(rows) > 0 {
		return f.AutoFilter(name, fmt.Sprintf("A1:%s%d", last, len(rows)+1), nil)
	}
	return nil
}

// send streams the workbook as a download. The Russian file name goes into
// filename* (RFC 5987); filename= keeps an ASCII fallback for old clients.
func (b *workbook) send(c *gin.Context, baseRu, baseASCII string) {
	stamp := time.Now().Format("2006-01-02")
	ru := fmt.Sprintf("%s_%s.xlsx", baseRu, stamp)
	ascii := fmt.Sprintf("%s_%s.xlsx", baseASCII, stamp)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii, strings.ReplaceAll(url.QueryEscape(ru), "+", "%20")))
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Status(http.StatusOK)
	if err := b.f.Write(c.Writer); err != nil {
		c.Error(err)
	}
	b.f.Close()
}

func exportFailed(c *gin.Context) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build the Excel file"})
}

// ExportAnalytics — GET /api/admin/export/analytics?days=30
// KPI, sales by day, menu engineering and dish pairs on separate sheets.
func (h *ExportHandler) ExportAnalytics(c *gin.Context) {
	days := periodDays(c)
	ctx := c.Request.Context()
	cur, err1 := h.Analytics.stats(ctx, days, 0)
	prev, err2 := h.Analytics.stats(ctx, days*2, days)
	sales, err3 := h.Analytics.sales(ctx, days)
	menu, err4 := h.Analytics.menuEngineering(ctx, days)
	pairs, err5 := h.Analytics.pairs(ctx, days, 30)
	for _, err := range []error{err1, err2, err3, err4, err5} {
		if err != nil {
			exportFailed(c)
			return
		}
	}

	b, err := newWorkbook()
	if err != nil {
		exportFailed(c)
		return
	}
	change := func(cur, prev float64) any {
		if g := growth(cur, prev); g != nil {
			return *g
		}
		return "—"
	}
	period := fmt.Sprintf("Последние %d дн.", days)
	kpi := [][]any{
		{"Заказы", cur.Orders, prev.Orders, change(float64(cur.Orders), float64(prev.Orders))},
		{"Выручка, ₸", cur.Revenue, prev.Revenue, change(cur.Revenue, prev.Revenue)},
		{"Валовая прибыль, ₸", cur.Margin, prev.Margin, change(cur.Margin, prev.Margin)},
		{"Средний чек, ₸", cur.AvgCheck, prev.AvgCheck, change(cur.AvgCheck, prev.AvgCheck)},
		{"Продано порций", cur.ItemsSold, prev.ItemsSold, change(float64(cur.ItemsSold), float64(prev.ItemsSold))},
		{"Отменено заказов", cur.Cancelled, prev.Cancelled, change(float64(cur.Cancelled), float64(prev.Cancelled))},
	}
	salesRows := make([][]any, len(sales))
	for i, p := range sales {
		salesRows[i] = []any{p.Date, p.Orders, p.Revenue}
	}
	menuRows := make([][]any, len(menu.Dishes))
	for i, d := range menu.Dishes {
		menuRows[i] = []any{d.Name, d.Category, menuClassRu[d.Class], d.Quantity, d.Popularity,
			d.Price, d.CostPrice, d.UnitMargin, d.FoodCostPct, d.Revenue, d.Margin, d.Advice}
	}
	pairRows := make([][]any, len(pairs))
	for i, p := range pairs {
		pairRows[i] = []any{p.NameA, p.NameB, p.Orders, p.Support, p.ConfidenceA, p.ConfidenceB, p.Lift}
	}

	steps := []error{
		b.sheet("Сводка", []column{{"Показатель — " + period, 30, colText}, {"Текущий период", 18, colNumber},
			{"Прошлый период", 18, colNumber}, {"Изменение, %", 15, colPct}}, kpi),
		b.sheet("Продажи по дням", []column{{"Дата", 14, colText}, {"Заказы", 12, colInt}, {"Выручка", 16, colMoney}}, salesRows),
		b.sheet("Инженерия меню", []column{{"Блюдо", 22, colText}, {"Категория", 14, colText}, {"Класс", 18, colText},
			{"Продано", 11, colInt}, {"Доля продаж", 13, colPct}, {"Цена", 12, colMoney}, {"Себестоимость", 15, colMoney},
			{"Маржа с порции", 16, colMoney}, {"Фудкост", 11, colPct}, {"Выручка", 15, colMoney},
			{"Маржа всего", 15, colMoney}, {"Рекомендация", 60, colText}}, menuRows),
		b.sheet("Что берут вместе", []column{{"Блюдо A", 22, colText}, {"Блюдо B", 22, colText}, {"Заказов вместе", 15, colInt},
			{"Support", 11, colPct}, {"Confidence A→B", 16, colPct}, {"Confidence B→A", 16, colPct}, {"Lift", 9, colNumber}}, pairRows),
	}
	for _, err := range steps {
		if err != nil {
			exportFailed(c)
			return
		}
	}
	b.send(c, fmt.Sprintf("Аналитика_RMS_%dдн", days), fmt.Sprintf("RMS_analytics_%dd", days))
}

// ExportOrders — GET /api/admin/export/orders?days=30
// One row per order and, on the second sheet, one row per dish in it.
func (h *ExportHandler) ExportOrders(c *gin.Context) {
	days := periodDays(c)
	orders, err := h.Orders.loadOrders(c.Request.Context(),
		orderSelect+`WHERE o.created_at >= NOW() - make_interval(days => $1) ORDER BY o.created_at DESC`, days)
	if err != nil {
		exportFailed(c)
		return
	}
	b, err := newWorkbook()
	if err != nil {
		exportFailed(c)
		return
	}
	var orderRows, itemRows [][]any
	for _, o := range orders {
		var dishes []string
		portions := 0
		for _, it := range o.Items {
			dishes = append(dishes, fmt.Sprintf("%s × %d", it.Name, it.Quantity))
			portions += it.Quantity
			itemRows = append(itemRows, []any{o.ID, o.CreatedAt, it.Name, it.Quantity, it.Price, it.Price * float64(it.Quantity)})
		}
		orderRows = append(orderRows, []any{o.ID, o.CreatedAt, o.UserName, orderStatusRu[o.Status],
			strings.Join(dishes, ", "), portions, o.Discount, o.PromoCode, o.TotalPrice})
	}
	if err := b.sheet("Заказы", []column{{"№", 8, colInt}, {"Дата", 17, colDate}, {"Гость", 22, colText},
		{"Статус", 15, colText}, {"Состав", 50, colText}, {"Порций", 9, colInt}, {"Скидка", 12, colMoney},
		{"Промокод", 13, colText}, {"Итого", 13, colMoney}}, orderRows); err != nil {
		exportFailed(c)
		return
	}
	if err := b.sheet("Позиции", []column{{"№ заказа", 10, colInt}, {"Дата", 17, colDate}, {"Блюдо", 24, colText},
		{"Количество", 12, colInt}, {"Цена", 12, colMoney}, {"Сумма", 13, colMoney}}, itemRows); err != nil {
		exportFailed(c)
		return
	}
	b.send(c, fmt.Sprintf("Заказы_RMS_%dдн", days), fmt.Sprintf("RMS_orders_%dd", days))
}

// ExportPurchasing — GET /api/admin/export/purchasing?budget=N&safety=10
// The same LP plan as the «Закупки» page.
func (h *ExportHandler) ExportPurchasing(c *gin.Context) {
	budget := float64(intQuery(c, "budget", 0, 0, 1_000_000_000))
	plan, err := h.Inventory.buildPlan(c.Request.Context(), budget, intQuery(c, "safety", 10, 0, 50))
	if err != nil {
		exportFailed(c)
		return
	}
	b, err := newWorkbook()
	if err != nil {
		exportFailed(c)
		return
	}
	summary := [][]any{
		{"Горизонт, дней", plan.Days},
		{"Страховой запас, %", plan.SafetyPct},
		{"Бюджет, ₸", plan.Budget},
		{"Полный бюджет (весь прогноз), ₸", plan.FullBudget},
		{"Стоимость закупки, ₸", plan.TotalCost},
		{"Спрос обеспечен, %", plan.CoveragePct},
		{"Ожидаемая выручка, ₸", plan.ExpectedRevenue},
		{"Ожидаемая прибыль, ₸", plan.ExpectedProfit},
		{"Теневая цена бюджета (прибыль на +1 ₸)", plan.BudgetShadowPrice},
	}
	items := make([][]any, len(plan.Items))
	for i, it := range plan.Items {
		items[i] = []any{it.Name, it.Unit, it.Need, it.Stock, it.Buy, it.Packs, it.PackSize, it.Cost, it.Warning}
	}
	dishes := make([][]any, len(plan.Dishes))
	for i, d := range plan.Dishes {
		dishes[i] = []any{d.Name, d.Price, d.Demand, d.Planned, d.CoveragePct}
	}
	steps := []error{
		b.sheet("Итог", []column{{"Показатель", 40, colText}, {"Значение", 18, colNumber}}, summary),
		b.sheet("Список закупки", []column{{"Продукт", 26, colText}, {"Ед.", 6, colText}, {"Нужно", 11, colNumber},
			{"На складе", 11, colNumber}, {"Купить", 11, colNumber}, {"Упаковок", 10, colInt}, {"Упаковка", 10, colNumber},
			{"Сумма", 13, colMoney}, {"Предупреждение", 40, colText}}, items),
		b.sheet("Обеспечение блюд", []column{{"Блюдо", 24, colText}, {"Цена", 12, colMoney}, {"Спрос, порций", 14, colNumber},
			{"По плану", 12, colNumber}, {"Покрытие", 11, colPct}}, dishes),
	}
	for _, err := range steps {
		if err != nil {
			exportFailed(c)
			return
		}
	}
	b.send(c, "Закупки_RMS", "RMS_purchasing")
}

// ExportInventory — GET /api/admin/export/inventory
func (h *ExportHandler) ExportInventory(c *gin.Context) {
	rows, err := h.Inventory.inventory(c.Request.Context())
	if err != nil {
		exportFailed(c)
		return
	}
	b, err := newWorkbook()
	if err != nil {
		exportFailed(c)
		return
	}
	data := make([][]any, len(rows))
	for i, r := range rows {
		var cover any = "—"
		if r.DaysCover != nil {
			cover = *r.DaysCover
		}
		data[i] = []any{r.Name, r.Unit, r.Stock, r.WeeklyNeed, cover, inventoryStatusRu[r.Status],
			r.Price, r.Stock * r.Price, r.ShelfLife}
	}
	if err := b.sheet("Склад", []column{{"Продукт", 26, colText}, {"Ед.", 6, colText}, {"Остаток", 11, colNumber},
		{"Расход в неделю", 15, colNumber}, {"Хватит, дней", 13, colNumber}, {"Статус", 16, colText},
		{"Цена за ед.", 12, colMoney}, {"Стоимость остатка", 17, colMoney}, {"Срок хранения, дн.", 17, colInt}}, data); err != nil {
		exportFailed(c)
		return
	}
	b.send(c, "Склад_RMS", "RMS_inventory")
}
