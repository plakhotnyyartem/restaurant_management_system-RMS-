/* =========================================================
   admin.js — панель управления
   Разделы зависят от роли: admin — всё, owner — аналитика,
   waiter / cook — очередь заказов.
   ========================================================= */

const main = document.getElementById("section");

const SECTIONS = {
  dashboard: { icon: "📊", title: "Dashboard", roles: ["admin", "owner"] },
  assistant: { icon: "🤖", title: "ИИ-ассистент", roles: ["admin", "owner"] },
  analytics: { icon: "🧠", title: "Аналитика", roles: ["admin", "owner"] },
  forecast: { icon: "🔮", title: "Прогноз", roles: ["admin", "owner"] },
  purchasing: { icon: "🛒", title: "Закупки", roles: ["admin", "owner"] },
  inventory: { icon: "📦", title: "Склад", roles: ["admin", "owner", "cook"] },
  orders: { icon: "🧾", title: "Заказы", roles: ["admin", "waiter", "cook"] },
  dishes: { icon: "🍕", title: "Блюда", roles: ["admin"] },
  categories: { icon: "🗂️", title: "Категории", roles: ["admin"] },
  users: { icon: "👥", title: "Пользователи", roles: ["admin"] },
};

const STATUS_LABEL = {
  pending: "Ожидает",
  confirmed: "Принят",
  preparing: "Готовится",
  ready: "Готов к выдаче",
  completed: "Выполнен",
  cancelled: "Отменён",
};

// Та же матрица прав, что и в orders.go — на клиенте только чтобы показать нужные кнопки.
// Проверку всё равно делает сервер.
const NEXT_ACTIONS = {
  pending: [
    { to: "confirmed", label: "Принять", roles: ["waiter", "admin"] },
    { to: "cancelled", label: "Отменить", roles: ["waiter", "admin"], danger: true },
  ],
  confirmed: [{ to: "preparing", label: "Начать готовить", roles: ["cook", "admin"] }],
  preparing: [{ to: "ready", label: "Готово", roles: ["cook", "admin"] }],
  ready: [{ to: "completed", label: "Выдан гостю", roles: ["waiter", "admin"] }],
};

let refreshTimer = null;
let role = null;

// ---------- Утилиты ----------

function fmtPct(value) {
  return (value > 0 ? "+" : "") + value.toFixed(1).replace(".", ",") + "%";
}

function deltaHtml(value) {
  if (value === null || value === undefined) return '<span class="delta flat">нет данных для сравнения</span>';
  if (Math.abs(value) < 0.5) return `<span class="delta flat">≈ ${fmtPct(value)} к прошлому периоду</span>`;
  const up = value > 0;
  return `<span class="delta ${up ? "up" : "down"}">${up ? "▲" : "▼"} ${fmtPct(value)} к прошлому периоду</span>`;
}

function errorBlock(title, error) {
  return `<div class="empty"><div class="big">⚠️</div><h3>${esc(title)}</h3><p>${esc(error.message)}</p></div>`;
}

function adminError(error) {
  const messages = {
    "category already exists": "Такая категория уже есть",
    "category has dishes, remove or move them first": "В категории есть блюда — сначала перенесите их",
    "category not found": "Категория не найдена",
    "dish has orders, make it unavailable instead": "Блюдо уже заказывали — его можно только скрыть из меню",
    "your role cannot set this status": "Ваша роль не может поставить этот статус",
    "invalid status transition": "Такой переход статуса невозможен",
    "order was changed by someone else, reload it": "Заказ уже изменил другой сотрудник",
  };
  if (error.status === 403 && !messages[error.message]) return "Недостаточно прав";
  return messages[error.message] || error.message;
}

// ---------- Dashboard ----------

async function renderDashboard() {
  try {
    const [summary, sales, recs] = await Promise.all([
      API.get("/admin/analytics/summary?days=7"),
      API.get("/admin/analytics/sales?days=14"),
      API.get("/admin/analytics/recommendations?days=30"),
    ]);
    const s = summary.current;

    main.innerHTML = `
      <h2>Dashboard</h2>
      <p class="muted" style="margin:-12px 0 20px">Последние 7 дней</p>
      <div class="stats">
        <div class="stat"><span>Выручка</span><strong>${money(s.revenue)}</strong>${deltaHtml(summary.growth.revenue)}</div>
        <div class="stat"><span>Заказы</span><strong>${s.orders.toLocaleString("ru-RU")}</strong>${deltaHtml(summary.growth.orders)}</div>
        <div class="stat"><span>Средний чек</span><strong>${money(Math.round(s.avg_check))}</strong>${deltaHtml(summary.growth.avg_check)}</div>
        <div class="stat"><span>Валовая прибыль</span><strong>${money(s.margin)}</strong>${deltaHtml(summary.growth.margin)}</div>
      </div>
      <div class="panel">
        <h3>Выручка за 14 дней</h3>
        <div id="dash-sales"></div>
      </div>
      <div class="panel">
        <div class="section-head" style="margin-bottom:14px">
          <h3 style="margin:0">🧠 Главное на сегодня</h3>
          <a href="#analytics" class="btn btn-ghost btn-sm">Вся аналитика →</a>
        </div>
        <div class="recs">${recs.items.slice(0, 3).map(recCard).join("") || '<p class="muted">Пока недостаточно данных.</p>'}</div>
      </div>`;

    barChart(document.getElementById("dash-sales"), sales, {
      valueKey: "revenue",
      format: money,
      extraRows: (p) => [{ value: p.orders, label: "заказов" }],
    });
  } catch (error) {
    main.innerHTML = "<h2>Dashboard</h2>" + errorBlock("Не удалось загрузить данные", error);
  }
}

// ---------- Аналитика ----------

const REC_ICONS = { promote: "📣", price: "💰", remove: "🗑️", combo: "🍱", staff: "👨‍🍳", trend: "📈", forecast: "🔮" };
const REC_TYPES = { promote: "Реклама", price: "Цена", remove: "Меню", combo: "Комбо", staff: "Персонал", trend: "Тренд", forecast: "Прогноз" };

function recCard(r, i = 0) {
  return `
    <div class="rec" style="animation-delay:${i * 50}ms">
      <div class="rec-icon">${REC_ICONS[r.type] || "💡"}</div>
      <div>
        <div class="rec-type">${REC_TYPES[r.type] || esc(r.type)}</div>
        <h4>${esc(r.title)}</h4>
        <p>${esc(r.detail)}</p>
      </div>
    </div>`;
}

let analyticsDays = 30;

async function renderAnalytics() {
  const periods = [7, 30, 90, 180];
  main.innerHTML = `
    <h2>Аналитика и рекомендации</h2>
    <div class="filters">
      <span class="label">Период:</span>
      ${periods.map((d) => `<button class="chip ${d === analyticsDays ? "active" : ""}" data-days="${d}">${d} дн.</button>`).join("")}
    </div>
    <div id="analytics-body"><div class="skeleton" style="height:300px"></div></div>`;

  main.querySelector(".filters").addEventListener("click", (e) => {
    const chip = e.target.closest("[data-days]");
    if (!chip) return;
    analyticsDays = Number(chip.dataset.days);
    main.querySelectorAll(".filters .chip").forEach((c) => c.classList.toggle("active", c === chip));
    loadAnalytics();
  });

  await loadAnalytics();
}

async function loadAnalytics() {
  const body = document.getElementById("analytics-body");
  const days = analyticsDays;
  body.style.opacity = 0.5; // при перезагрузке держим старую картинку, без «прыжка»

  let summary, sales, menu, heat, pairs, recs;
  try {
    [summary, sales, menu, heat, pairs, recs] = await Promise.all([
      API.get(`/admin/analytics/summary?days=${days}`),
      API.get(`/admin/analytics/sales?days=${days}`),
      API.get(`/admin/analytics/menu?days=${days}`),
      API.get(`/admin/analytics/heatmap?days=${Math.max(days, 28)}`),
      API.get(`/admin/analytics/pairs?days=${Math.max(days, 30)}&limit=8`),
      API.get(`/admin/analytics/recommendations?days=${days}`),
    ]);
  } catch (error) {
    body.style.opacity = 1;
    body.innerHTML = errorBlock("Не удалось загрузить аналитику", error);
    return;
  }
  if (days !== analyticsDays) return; // пользователь уже выбрал другой период

  const s = summary.current;
  const cancelRate = s.orders + s.cancelled ? (s.cancelled / (s.orders + s.cancelled)) * 100 : 0;

  body.style.opacity = 1;
  body.innerHTML = `
    <div class="stats">
      <div class="stat"><span>Выручка</span><strong>${money(s.revenue)}</strong>${deltaHtml(summary.growth.revenue)}</div>
      <div class="stat"><span>Заказы</span><strong>${s.orders.toLocaleString("ru-RU")}</strong>${deltaHtml(summary.growth.orders)}</div>
      <div class="stat"><span>Средний чек</span><strong>${money(Math.round(s.avg_check))}</strong>${deltaHtml(summary.growth.avg_check)}</div>
      <div class="stat"><span>Валовая прибыль</span><strong>${money(s.margin)}</strong>
        <small>${s.revenue ? Math.round((s.margin / s.revenue) * 100) : 0}% от выручки · отмен ${cancelRate.toFixed(1)}%</small></div>
    </div>

    <div class="panel">
      <h3>🧠 Что сделать — рекомендации системы</h3>
      <p class="muted" style="font-size:14px;margin:-8px 0 14px">Сформированы автоматически из инженерии меню, анализа корзин, нагрузки и динамики выручки.</p>
      <div class="recs">${recs.items.map(recCard).join("") || '<p class="muted">Недостаточно данных за период.</p>'}</div>
    </div>

    <div class="panel">
      <h3>Выручка по дням</h3>
      <div id="chart-sales"></div>
    </div>

    <div class="panel">
      <h3>Инженерия меню</h3>
      <p class="muted" style="font-size:14px;margin:-8px 0 14px">
        Метод Kasavana &amp; Smith: популярность (порог ${menu.popularity_threshold}% — 70% от «справедливой доли») ×
        маржа с порции (порог ${money(menu.margin_threshold)} — средняя по меню).
      </p>
      <div id="chart-menu"></div>
      <div class="table-wrap" style="margin-top:18px">
        <table>
          <thead><tr><th>Блюдо</th><th>Класс</th><th class="num">Продано</th><th class="num">Доля</th><th class="num">Маржа/порция</th><th class="num">Себест.</th><th>Что делать</th></tr></thead>
          <tbody>
            ${menu.dishes.map((d) => `
              <tr>
                <td><strong>${esc(d.name)}</strong><div class="muted" style="font-size:12px">${esc(d.category)}</div></td>
                <td>${d.class ? `<span class="class-badge">${CLASS_INFO[d.class].icon} ${CLASS_INFO[d.class].name}</span>` : "—"}</td>
                <td class="num">${d.quantity.toLocaleString("ru-RU")}</td>
                <td class="num">${d.popularity}%</td>
                <td class="num">${money(d.unit_margin)}</td>
                <td class="num">${d.food_cost_pct}%</td>
                <td class="advice">${esc(d.advice || "")}</td>
              </tr>`).join("")}
          </tbody>
        </table>
      </div>
    </div>

    <div class="grid-2">
      <div class="panel">
        <h3>Нагрузка по дням и часам</h3>
        <p class="muted" style="font-size:14px;margin:-8px 0 14px">Среднее число заказов в час — для графика смен.</p>
        <div id="chart-heat"></div>
      </div>
      <div class="panel">
        <h3>Что берут вместе</h3>
        <p class="muted" style="font-size:14px;margin:-8px 0 14px">Ассоциативные правила: «из тех, кто взял A, X% взяли и B».</p>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Пара</th><th>Уверенность</th><th class="num">Lift</th></tr></thead>
            <tbody>
              ${pairs.map((p) => `
                <tr>
                  <td>${esc(p.name_a)} → ${esc(p.name_b)}<div class="muted" style="font-size:12px">${p.orders} заказов вместе</div></td>
                  <td><div style="display:flex;gap:8px;align-items:center"><div class="meter" style="flex:1"><div style="width:${Math.min(p.confidence_ab, 100)}%"></div></div><span class="num">${p.confidence_ab}%</span></div></td>
                  <td class="num">×${p.lift}</td>
                </tr>`).join("") || '<tr><td colspan="3" class="muted">Нет данных</td></tr>'}
            </tbody>
          </table>
        </div>
      </div>
    </div>`;

  if (menu.total_items > 0) {
    barChart(document.getElementById("chart-sales"), sales, {
      valueKey: "revenue",
      format: money,
      extraRows: (p) => [{ value: p.orders, label: "заказов" }],
    });
    menuMatrix(document.getElementById("chart-menu"), menu);
    heatmap(document.getElementById("chart-heat"), heat);
  }
}

// ---------- Прогноз спроса ----------

const WEEKDAYS = ["", "пн", "вт", "ср", "чт", "пт", "сб", "вс"];

async function renderForecast() {
  let fc;
  try {
    fc = await API.get("/admin/analytics/forecast?horizon=14");
  } catch (error) {
    main.innerHTML = "<h2>Прогноз спроса</h2>" + errorBlock("Не удалось построить прогноз", error);
    return;
  }
  if (!fc.ready) {
    main.innerHTML = `<h2>Прогноз спроса</h2><div class="empty"><div class="big">🔮</div><h3>Мало данных</h3><p>${esc(fc.message)}</p></div>`;
    return;
  }

  const tomorrow = fc.forecast[1];
  const week = fc.forecast.slice(1, 8);
  const weekOrders = week.reduce((sum, p) => sum + p.value, 0);
  const weekRevenue = week.reduce((sum, p) => sum + p.revenue, 0);
  const bt = fc.backtest;
  const peak = week.reduce((a, b) => (b.value > a.value ? b : a));

  main.innerHTML = `
    <h2>Прогноз спроса</h2>
    <div class="stats">
      <div class="stat"><span>Завтра, ${WEEKDAYS[tomorrow.weekday]}</span><strong>~${tomorrow.value}</strong>
        <small>заказов · интервал ${tomorrow.low}–${tomorrow.high}</small></div>
      <div class="stat"><span>Следующие 7 дней</span><strong>~${weekOrders.toLocaleString("ru-RU")}</strong>
        <small>заказов · выручка ~${money(Math.round(weekRevenue / 1000) * 1000)}</small></div>
      <div class="stat"><span>Пик недели</span><strong>${WEEKDAYS[peak.weekday]}, ~${peak.value}</strong>
        <small>${new Date(peak.date + "T00:00:00").toLocaleDateString("ru-RU", { day: "numeric", month: "long" })}</small></div>
      <div class="stat"><span>Ошибка прогноза</span><strong>${bt.mape_model.toFixed(1).replace(".", ",")}%</strong>
        <span class="delta ${bt.improvement > 0 ? "up" : "down"}">${bt.improvement > 0 ? "▲" : "▼"} на ${Math.abs(bt.improvement).toFixed(0)}% ${bt.improvement > 0 ? "точнее" : "хуже"} наивного (${bt.mape_naive.toFixed(1).replace(".", ",")}%)</span></div>
    </div>

    <div class="panel">
      <h3>Заказы в день: факт и прогноз на 14 дней</h3>
      <div id="chart-forecast"></div>
    </div>

    <div class="panel">
      <h3>Сколько порций готовить</h3>
      <p class="muted" style="font-size:14px;margin:-8px 0 14px">Та же модель для каждого блюда — основа для плана закупок.</p>
      <div class="table-wrap">
        <table>
          <thead><tr><th>Блюдо</th><th class="num">Завтра</th><th class="num">7 дней</th><th class="num">Прошлые 7 дней</th><th class="num">Изменение</th></tr></thead>
          <tbody>
            ${fc.dishes.map((d) => `
              <tr>
                <td><strong>${esc(d.name)}</strong><div class="muted" style="font-size:12px">${esc(d.category)}</div></td>
                <td class="num">~${d.tomorrow}</td>
                <td class="num"><strong>~${d.next_week}</strong></td>
                <td class="num muted">${d.last_week}</td>
                <td class="num">${d.last_week ? `<span class="delta ${d.change_pct >= 3 ? "up" : d.change_pct <= -3 ? "down" : "flat"}" style="margin:0">${d.change_pct > 0 ? "+" : ""}${Math.round(d.change_pct)}%</span>` : "—"}</td>
              </tr>`).join("")}
          </tbody>
        </table>
      </div>
    </div>

    <div class="panel">
      <h3>Как это считается</h3>
      <p class="muted" style="font-size:14px;line-height:1.7">
        <b style="color:var(--text)">${esc(fc.method)}.</b>
        Прогноз = <i>уровень</i> + <i>тренд</i> × дни + <i>поправка дня недели</i>.
        Коэффициенты сглаживания подобраны перебором по сетке с минимизацией квадратичной ошибки:
        α = ${fc.params.alpha}, β = ${fc.params.beta}, γ = ${fc.params.gamma}. Обучено на ${fc.trained_days} днях.<br>
        <b style="color:var(--text)">Проверка:</b> 4 раза модель обучалась только на прошлом и прогнозировала следующую неделю.
        Средняя ошибка ${bt.mape_model}% против ${bt.mape_naive}% у наивного прогноза «как в тот же день неделю назад».
        Интервал 80% = прогноз ± 1,28 × RMSE (${bt.rmse} заказа).
      </p>
    </div>`;

  forecastChart(document.getElementById("chart-forecast"), fc.history, fc.forecast);
}

// ---------- Закупки: линейное программирование ----------

let purchaseBudget = 0; // 0 = бюджет, покрывающий весь прогноз
let purchaseSafety = 10;
let lastPlan = null;

async function renderPurchasing() {
  main.innerHTML = `
    <h2>Оптимизация закупок</h2>
    <div class="filters" id="plan-filters">
      <span class="label">Бюджет на неделю:</span>
      <input class="input" id="plan-budget" type="number" min="0" step="10000" style="width:160px" placeholder="авто">
      <span id="budget-presets" style="display:flex;gap:6px;flex-wrap:wrap"></span>
      <span class="label" style="margin-left:8px">Запас:</span>
      <select class="input" id="plan-safety" style="width:90px">
        ${[0, 10, 20, 30].map((v) => `<option value="${v}" ${v === purchaseSafety ? "selected" : ""}>${v}%</option>`).join("")}
      </select>
      <button class="btn btn-sm" id="plan-run">Рассчитать</button>
    </div>
    <div id="plan-body"><div class="skeleton" style="height:300px"></div></div>`;

  document.getElementById("plan-run").addEventListener("click", () => {
    purchaseBudget = Number(document.getElementById("plan-budget").value) || 0;
    purchaseSafety = Number(document.getElementById("plan-safety").value);
    loadPlan();
  });
  document.getElementById("budget-presets").addEventListener("click", (e) => {
    const chip = e.target.closest("[data-share]");
    if (!chip || !lastPlan) return;
    purchaseBudget = Math.round((lastPlan.full_budget * Number(chip.dataset.share)) / 1000) * 1000;
    loadPlan();
  });
  await loadPlan();
}

async function loadPlan() {
  const body = document.getElementById("plan-body");
  body.style.opacity = 0.5;
  let plan;
  try {
    plan = await API.get(`/admin/analytics/purchasing?safety=${purchaseSafety}&budget=${purchaseBudget}`);
  } catch (error) {
    body.style.opacity = 1;
    body.innerHTML = errorBlock("Не удалось рассчитать план", error);
    return;
  }
  lastPlan = plan;
  body.style.opacity = 1;

  document.getElementById("plan-budget").value = Math.round(plan.budget);
  document.getElementById("budget-presets").innerHTML = [[1, "100%"], [0.75, "75%"], [0.5, "50%"], [0.25, "25%"]]
    .map(([share, label]) => {
      const active = Math.abs(plan.budget - plan.full_budget * share) < 1000;
      return `<button class="chip ${active ? "active" : ""}" data-share="${share}">${label}</button>`;
    }).join("");

  const toBuy = plan.items.filter((i) => i.packs > 0);
  const shadow = plan.budget_limited
    ? `<strong>+${money(Math.round(plan.budget_shadow_price * 1000))}</strong><small>прибыли на каждые +1 000 ₸ бюджета</small>`
    : `<strong>0 ₸</strong><small>бюджет не ограничивает — спрос покрыт полностью</small>`;

  body.innerHTML = `
    <div class="stats">
      <div class="stat"><span>Закупка</span><strong>${money(plan.total_cost)}</strong>
        <small>из ${money(plan.budget)} · полный план ${money(plan.full_budget)}</small></div>
      <div class="stat"><span>Спрос обеспечен</span><strong>${plan.coverage_pct}%</strong>
        <small>прогноз на ${plan.days} дней + ${plan.safety_pct}% запас</small></div>
      <div class="stat"><span>Ожидаемая выручка</span><strong>${money(plan.expected_revenue)}</strong>
        <small>прибыль после закупки ${money(plan.expected_profit)}</small></div>
      <div class="stat"><span>Теневая цена бюджета</span>${shadow}</div>
    </div>

    ${plan.no_recipe.length ? `<div class="note"><span>⚠️</span><span>Нет техкарты — блюда не учтены в плане: ${plan.no_recipe.map(esc).join(", ")}</span></div>` : ""}

    <div class="grid-2">
      <div class="panel">
        <h3>Что даёт каждый тенге бюджета</h3>
        <p class="muted" style="font-size:14px;margin:-8px 0 10px">Та же задача, решённая для 0–100% полного бюджета: отдача убывает.</p>
        <div id="chart-budget"></div>
      </div>
      <div class="panel">
        <h3>Какие блюда обеспечены</h3>
        <p class="muted" style="font-size:14px;margin:-8px 0 10px">При нехватке денег решатель сам выбирает самые выгодные блюда.</p>
        <div class="table-wrap" style="max-height:260px;overflow-y:auto">
          <table>
            <thead><tr><th>Блюдо</th><th class="num">План / спрос</th><th style="width:40%">Покрытие</th></tr></thead>
            <tbody>
              ${plan.dishes.map((d) => `
                <tr>
                  <td>${esc(d.name)}</td>
                  <td class="num">${d.planned} / ${d.demand}</td>
                  <td><div style="display:flex;gap:8px;align-items:center"><div class="meter" style="flex:1"><div style="width:${Math.min(d.coverage_pct, 100)}%"></div></div><span class="num" style="min-width:42px">${Math.round(d.coverage_pct)}%</span></div></td>
                </tr>`).join("")}
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <div class="panel">
      <div class="section-head" style="margin-bottom:14px">
        <h3 style="margin:0">🛒 Список закупки — ${toBuy.length} позиций</h3>
        ${role === "admin" && toBuy.length ? '<button class="btn btn-sm" id="plan-apply">Оприходовать поставку</button>' : ""}
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>Продукт</th><th class="num">Нужно</th><th class="num">На складе</th><th class="num">Купить</th><th class="num">Упаковок</th><th class="num">Сумма</th><th></th></tr></thead>
          <tbody>
            ${toBuy.map((i) => `
              <tr>
                <td><strong>${esc(i.name)}</strong></td>
                <td class="num">${i.need} ${esc(i.unit)}</td>
                <td class="num muted">${i.stock} ${esc(i.unit)}</td>
                <td class="num"><strong>${i.buy} ${esc(i.unit)}</strong></td>
                <td class="num">${i.packs} × ${i.pack_size}</td>
                <td class="num">${money(i.cost)}</td>
                <td>${i.warning ? `<span class="warn">⚠ ${esc(i.warning)}</span>` : ""}</td>
              </tr>`).join("") || '<tr><td colspan="7" class="muted">Закупать ничего не нужно — на складе хватает.</td></tr>'}
          </tbody>
        </table>
      </div>
    </div>

    <div class="panel">
      <h3>Постановка задачи</h3>
      <div class="lp-formula">максимизировать   Σ цена_блюда · порции  −  Σ цена_продукта · закупка
при условиях      Σ техкарта · порции − закупка ≤ остаток     (${plan.items.length} продуктов)
                  Σ цена_продукта · закупка     ≤ бюджет      (1 ограничение)
                  порции                        ≤ прогноз      (${plan.dishes.length} блюд)
                  порции, закупка ≥ 0</div>
      <p class="muted" style="font-size:14px;margin-top:12px">
        Решено ${esc(plan.solver.method)}: ${plan.solver.variables} переменных, ${plan.solver.constraints} ограничений,
        ${plan.solver.pivots} шагов. Дробные килограммы затем округлены до целых упаковок в пределах бюджета.
        Теневая цена — двойственная оценка ограничения бюджета: на сколько вырастет прибыль, если дать ещё 1 ₸.
      </p>
    </div>`;

  budgetCurveChart(document.getElementById("chart-budget"), plan.curve, plan.budget);

  document.getElementById("plan-apply")?.addEventListener("click", async () => {
    if (!confirm(`Добавить на склад ${toBuy.length} позиций на ${money(plan.total_cost)}?`)) return;
    try {
      await API.post("/admin/inventory/receive", {
        items: toBuy.map((i) => ({ ingredient_id: i.ingredient_id, quantity: i.buy })),
      });
      UI.toast("Поставка оприходована — остатки обновлены", "success");
      loadPlan();
    } catch (error) {
      UI.toast(adminError(error), "error");
    }
  });
}

// ---------- Склад ----------

const STOCK_STATUS = {
  critical: "● Заканчивается",
  low: "● Мало",
  ok: "● В норме",
  unused: "○ Не используется",
};

let inventoryFilter = "all";

async function renderInventory() {
  let rows;
  try {
    rows = await API.get("/admin/inventory");
  } catch (error) {
    main.innerHTML = "<h2>Склад</h2>" + errorBlock("Не удалось загрузить склад", error);
    return;
  }
  const canEdit = role === "admin" || role === "cook";
  const order = { critical: 0, low: 1, ok: 2, unused: 3 };
  rows.sort((a, b) => order[a.status] - order[b.status] || (a.days_cover ?? 999) - (b.days_cover ?? 999));
  const shown = inventoryFilter === "all" ? rows : rows.filter((r) => r.status === "critical" || r.status === "low");
  const critical = rows.filter((r) => r.status === "critical").length;
  const low = rows.filter((r) => r.status === "low").length;

  main.innerHTML = `
    <h2>Склад</h2>
    <div class="stats">
      <div class="stat"><span>Позиций</span><strong>${rows.length}</strong><small>продуктов в техкартах</small></div>
      <div class="stat"><span>Заканчиваются</span><strong>${critical}</strong><small>хватит меньше чем на 2 дня</small></div>
      <div class="stat"><span>Мало</span><strong>${low}</strong><small>хватит на 2–5 дней</small></div>
      <div class="stat"><span>Стоимость запаса</span><strong>${money(Math.round(rows.reduce((s, r) => s + r.stock * r.price, 0)))}</strong><small>по закупочным ценам</small></div>
    </div>
    <div class="filters">
      <button class="chip ${inventoryFilter === "all" ? "active" : ""}" data-inv="all">Все</button>
      <button class="chip ${inventoryFilter === "low" ? "active" : ""}" data-inv="low">Нужно докупить (${critical + low})</button>
      ${role !== "cook" ? '<a href="#purchasing" class="btn btn-ghost btn-sm" style="margin-left:auto">Рассчитать закупку →</a>' : ""}
    </div>
    <div class="panel table-wrap">
      <table>
        <thead><tr><th>Продукт</th><th>Статус</th><th class="num">Остаток</th><th class="num">Нужно на неделю</th><th class="num">Хватит на</th><th class="num">Цена</th></tr></thead>
        <tbody>
          ${shown.map((r) => `
            <tr>
              <td><strong>${esc(r.name)}</strong><div class="muted" style="font-size:12px">срок хранения ${r.shelf_life_days} дн.</div></td>
              <td><span class="stock-badge ${r.status}">${STOCK_STATUS[r.status]}</span></td>
              <td class="num">${canEdit
                ? `<input class="input stock-input" type="number" min="0" step="0.1" value="${r.stock}" data-stock="${r.id}" aria-label="Остаток ${esc(r.name)}"> ${esc(r.unit)}`
                : `${r.stock} ${esc(r.unit)}`}</td>
              <td class="num">${r.weekly_need} ${esc(r.unit)}</td>
              <td class="num">${r.days_cover === null ? "—" : r.days_cover >= 99 ? "99+ дн." : r.days_cover.toFixed(1).replace(".", ",") + " дн."}</td>
              <td class="num muted">${money(r.price)} / ${esc(r.unit)}</td>
            </tr>`).join("")}
        </tbody>
      </table>
    </div>
    ${canEdit ? '<p class="muted" style="font-size:13px">Измените остаток и нажмите Enter — так фиксируется инвентаризация. Продукты списываются автоматически, когда повар начинает готовить заказ.</p>' : ""}`;

  main.querySelector(".filters").addEventListener("click", (e) => {
    const chip = e.target.closest("[data-inv]");
    if (!chip) return;
    inventoryFilter = chip.dataset.inv;
    renderInventory();
  });

  main.querySelectorAll("[data-stock]").forEach((input) => {
    input.addEventListener("change", async () => {
      const value = Number(input.value);
      if (!(value >= 0)) {
        UI.toast("Остаток не может быть отрицательным", "error");
        return;
      }
      try {
        await API.put("/admin/inventory/" + input.dataset.stock, { quantity: value });
        UI.toast("Остаток обновлён", "success");
        renderInventory();
      } catch (error) {
        UI.toast(adminError(error), "error");
      }
    });
  });
}

// ---------- ИИ-ассистент ----------

const chatHistory = []; // [{ role: "user" | "assistant", content, tools? }]
let chatBusy = false;

const SUGGESTIONS = [
  "Что прорекламировать на этой неделе?",
  "Сколько заказов ждать в выходные и сколько поваров поставить?",
  "Что закупить, если бюджет 300 000 ₸?",
  "Напиши пост для Instagram про самое выгодное блюдо",
  "Почему выручка упала по сравнению с прошлым месяцем?",
  "Какое комбо предложить, чтобы вырос средний чек?",
];

// Ответ модели — это текст из внешнего сервиса: сначала экранируем,
// потом превращаем только **жирный** и списки «- » в HTML.
function renderAnswer(text) {
  const blocks = esc(text).split(/\n{2,}/);
  return blocks.map((block) => {
    const lines = block.split("\n");
    const bold = (line) => line.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
    if (lines.every((l) => /^\s*[-•]\s+/.test(l))) {
      return "<ul>" + lines.map((l) => "<li>" + bold(l.replace(/^\s*[-•]\s+/, "")) + "</li>").join("") + "</ul>";
    }
    return "<p>" + lines.map(bold).join("<br>") + "</p>";
  }).join("");
}

function chatMessageHtml(m) {
  if (m.role === "user") return `<div class="msg user">${esc(m.content)}</div>`;
  const sources = m.tools?.length
    ? `<div class="msg-sources">📊 Данные: ${m.tools.map((t) => `<span>${esc(t)}</span>`).join("")}</div>`
    : "";
  return `<div class="msg bot ${m.error ? "error" : ""}">${renderAnswer(m.content)}${sources}</div>`;
}

function renderChatLog() {
  const log = document.getElementById("chat-log");
  if (!log) return;
  if (!chatHistory.length) {
    log.innerHTML = `
      <div class="chat-empty">
        <div class="big">🤖</div>
        <h3>Спросите о своём ресторане</h3>
        <p>Ассистент сам заглянет в аналитику, прогноз и план закупок и объяснит цифры простыми словами.
           Он не придумывает числа — берёт их из тех же расчётов, что и графики.</p>
        <div class="suggestions">${SUGGESTIONS.map((q) => `<button class="chip" data-ask="${esc(q)}">${esc(q)}</button>`).join("")}</div>
      </div>`;
    return;
  }
  log.innerHTML = chatHistory.map(chatMessageHtml).join("") +
    (chatBusy ? `<div class="msg bot"><span class="typing"><i></i><i></i><i></i></span>
                 <span class="muted" style="font-size:13px;margin-left:8px">анализирую данные…</span></div>` : "");
  log.scrollTop = log.scrollHeight;
}

async function askAssistant(question) {
  question = question.trim();
  if (!question || chatBusy) return;
  chatHistory.push({ role: "user", content: question });
  chatBusy = true;
  renderChatLog();

  try {
    // На сервер уходят только тексты реплик (успешные ответы, без ошибок).
    const messages = chatHistory
      .filter((m) => !m.error)
      .slice(-20)
      .map((m) => ({ role: m.role, content: m.content }));
    const result = await API.post("/admin/assistant", { messages });
    chatHistory.push({ role: "assistant", content: result.reply || "…", tools: result.tools });
  } catch (error) {
    const text = error.status === 503
      ? "Ассистент ещё не подключён: на сервере не задан ключ ANTHROPIC_API_KEY. Инструкция — в README, раздел «ИИ-ассистент»."
      : error.status === 429
        ? "Ассистент сейчас перегружен — попробуйте через минуту."
        : "Не получилось получить ответ: " + error.message;
    chatHistory.push({ role: "assistant", content: text, error: true });
    // Вопрос остаётся в истории, но без ответа — убираем его из следующих запросов.
    chatHistory[chatHistory.length - 2].error = true;
  }
  chatBusy = false;
  renderChatLog();
}

async function renderAssistant() {
  main.innerHTML = `
    <div class="section-head" style="margin-bottom:16px">
      <h2 style="margin:0">ИИ-ассистент</h2>
      <button class="btn btn-ghost btn-sm" id="chat-clear">Новый диалог</button>
    </div>
    <div class="chat">
      <div class="chat-log" id="chat-log"></div>
      <form class="chat-form" id="chat-form">
        <textarea class="input" id="chat-input" rows="1" maxlength="4000"
          placeholder="Например: что будет с продажами в эти выходные?"></textarea>
        <button class="btn" type="submit">Спросить</button>
      </form>
    </div>
    <p class="muted" style="font-size:13px;margin-top:10px">
      Модель Claude вызывает инструменты системы (KPI, инженерия меню, прогноз, закупки, анализ корзин, загрузка)
      и объясняет результат. Enter — отправить, Shift+Enter — новая строка.
    </p>`;

  const input = document.getElementById("chat-input");
  const send = () => {
    const q = input.value;
    input.value = "";
    input.style.height = "";
    askAssistant(q);
  };
  document.getElementById("chat-form").addEventListener("submit", (e) => { e.preventDefault(); send(); });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); }
  });
  input.addEventListener("input", () => {
    input.style.height = "";
    input.style.height = Math.min(input.scrollHeight, 160) + "px";
  });
  document.getElementById("chat-log").addEventListener("click", (e) => {
    const chip = e.target.closest("[data-ask]");
    if (chip) askAssistant(chip.dataset.ask);
  });
  document.getElementById("chat-clear").addEventListener("click", () => {
    if (chatBusy) return;
    chatHistory.length = 0;
    renderChatLog();
  });

  renderChatLog();
  input.focus();
}

// ---------- Очередь заказов ----------

let ordersFilter = "active";

async function renderOrders() {
  const filters = [
    ["active", "В работе"], ["pending", "Ожидают"], ["confirmed", "Приняты"], ["preparing", "Готовятся"],
    ["ready", "Готовы"], ["completed", "Выполнены"], ["cancelled", "Отменены"],
  ];
  main.innerHTML = `
    <h2>Заказы</h2>
    <div class="filters">
      ${filters.map(([k, label]) => `<button class="chip ${k === ordersFilter ? "active" : ""}" data-filter="${k}">${label}</button>`).join("")}
      <span class="muted" style="font-size:13px;margin-left:auto" id="orders-updated"></span>
    </div>
    <div id="orders-board"></div>`;

  main.querySelector(".filters").addEventListener("click", (e) => {
    const chip = e.target.closest("[data-filter]");
    if (!chip) return;
    ordersFilter = chip.dataset.filter;
    main.querySelectorAll(".filters .chip").forEach((c) => c.classList.toggle("active", c === chip));
    loadOrders();
  });

  await loadOrders();
  // Очередь обновляется сама — новые заказы появляются без перезагрузки страницы.
  refreshTimer = setInterval(loadOrders, 15000);
}

function ticket(o) {
  const actions = (NEXT_ACTIONS[o.status] || []).filter((a) => a.roles.includes(role));
  const time = new Date(o.created_at);
  const minutes = Math.round((Date.now() - time) / 60000);
  const ago = minutes < 60 ? `${minutes} мин назад` : time.toLocaleString("ru-RU", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

  return `
    <div class="ticket">
      <div class="ticket-head">
        <strong>#${o.id}</strong>
        <span class="status ${o.status}">${STATUS_LABEL[o.status]}</span>
      </div>
      <ul>${o.items.map((i) => `<li><span>${esc(i.name)}</span><span>×${i.quantity}</span></li>`).join("")}</ul>
      <div class="meta"><span>👤 ${esc(o.user_name)}</span><span>${ago}</span></div>
      <div class="meta"><span>Сумма</span><strong style="color:var(--text)">${money(o.total_price)}</strong></div>
      ${actions.length ? `<div class="actions">${actions.map((a) =>
        `<button class="btn btn-sm ${a.danger ? "btn-danger" : ""}" data-order="${o.id}" data-to="${a.to}">${a.label}</button>`).join("")}</div>` : ""}
    </div>`;
}

async function loadOrders() {
  const board = document.getElementById("orders-board");
  if (!board) return;
  try {
    const orders = await API.get(`/admin/orders?status=${ordersFilter}&limit=60`);
    board.innerHTML = orders.length
      ? `<div class="board">${orders.map(ticket).join("")}</div>`
      : `<div class="empty"><div class="big">☕</div><h3>Заказов нет</h3><p>Новые заказы появятся здесь автоматически.</p></div>`;
    document.getElementById("orders-updated").textContent =
      "обновлено " + new Date().toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  } catch (error) {
    board.innerHTML = errorBlock("Не удалось загрузить заказы", error);
  }
}

// ---------- Блюда ----------

let editingDish = null;

async function renderDishes() {
  let dishes, categories;
  try {
    [dishes, categories] = await Promise.all([API.get("/admin/dishes"), API.get("/categories")]);
  } catch (error) {
    main.innerHTML = "<h2>Блюда</h2>" + errorBlock("Не удалось загрузить блюда", error);
    return;
  }
  Data.dishesCache = null;

  const d = editingDish || { name: "", description: "", price: "", cost_price: "", category_id: categories[0]?.id, image_url: "", is_available: true };

  main.innerHTML = `
    <h2>Блюда</h2>
    <div class="panel">
      <h3>${editingDish ? `Редактирование: ${esc(editingDish.name)}` : "Новое блюдо"}</h3>
      <form id="dish-form" class="form-grid">
        <div class="field"><label for="f-name">Название</label><input class="input" id="f-name" maxlength="150" required value="${esc(d.name)}"></div>
        <div class="field"><label for="f-cat">Категория</label>
          <select class="input" id="f-cat">${categories.map((c) => `<option value="${c.id}" ${c.id === d.category_id ? "selected" : ""}>${esc(c.name)}</option>`).join("")}</select></div>
        <div class="field"><label for="f-price">Цена, ₸</label><input class="input" id="f-price" type="number" min="0" step="50" required value="${d.price}"></div>
        <div class="field"><label for="f-cost">Себестоимость, ₸ <span class="muted">(для расчёта маржи)</span></label><input class="input" id="f-cost" type="number" min="0" step="10" value="${d.cost_price}"></div>
        <div class="field full"><label for="f-desc">Описание</label><textarea class="input" id="f-desc">${esc(d.description)}</textarea></div>
        <div class="field full"><label for="f-img">Ссылка на фото <span class="muted">(необязательно)</span></label><input class="input" id="f-img" value="${esc(d.image_url)}"></div>
        <label class="checkbox full"><input type="checkbox" id="f-avail" ${d.is_available ? "checked" : ""}> Доступно для заказа</label>
        <div class="full" style="display:flex;gap:10px;margin-top:16px">
          <button class="btn" type="submit">${editingDish ? "Сохранить" : "+ Добавить блюдо"}</button>
          ${editingDish ? '<button class="btn btn-ghost" type="button" id="cancel-edit">Отмена</button>' : ""}
        </div>
      </form>
    </div>
    <div class="panel table-wrap">
      <table>
        <thead><tr><th>Блюдо</th><th>Категория</th><th class="num">Цена</th><th class="num">Себест.</th><th class="num">Маржа</th><th>В меню</th><th></th></tr></thead>
        <tbody>
          ${dishes.map((x) => `
            <tr>
              <td><strong>${esc(x.name)}</strong></td>
              <td class="muted">${esc(categoryName(categories, x.category_id))}</td>
              <td class="num">${money(x.price)}</td>
              <td class="num">${money(x.cost_price)}</td>
              <td class="num">${x.price ? Math.round(((x.price - x.cost_price) / x.price) * 100) : 0}%</td>
              <td><button class="switch ${x.is_available ? "on" : ""}" data-toggle="${x.id}" aria-label="Доступность" aria-pressed="${x.is_available}"></button></td>
              <td class="actions">
                <button class="btn btn-ghost btn-sm" data-edit="${x.id}">Изменить</button>
                <button class="btn btn-danger btn-sm" data-delete-dish="${x.id}">Удалить</button>
              </td>
            </tr>`).join("")}
        </tbody>
      </table>
    </div>`;

  document.getElementById("dish-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const body = {
      name: document.getElementById("f-name").value,
      category_id: Number(document.getElementById("f-cat").value),
      price: Number(document.getElementById("f-price").value),
      cost_price: Number(document.getElementById("f-cost").value || 0),
      description: document.getElementById("f-desc").value,
      image_url: document.getElementById("f-img").value.trim(),
      is_available: document.getElementById("f-avail").checked,
    };
    try {
      if (editingDish) {
        await API.put("/admin/dishes/" + editingDish.id, body);
        UI.toast("Блюдо сохранено", "success");
      } else {
        await API.post("/admin/dishes", body);
        UI.toast(`«${body.name}» добавлено в меню`, "success");
      }
      editingDish = null;
      renderDishes();
    } catch (error) {
      UI.toast(adminError(error), "error");
    }
  });

  document.getElementById("cancel-edit")?.addEventListener("click", () => {
    editingDish = null;
    renderDishes();
  });

  main.querySelector("tbody").addEventListener("click", async (e) => {
    const toggle = e.target.closest("[data-toggle]");
    const edit = e.target.closest("[data-edit]");
    const del = e.target.closest("[data-delete-dish]");

    if (toggle) {
      const next = !toggle.classList.contains("on");
      try {
        await API.request("PATCH", `/admin/dishes/${toggle.dataset.toggle}/availability`, { is_available: next });
        toggle.classList.toggle("on", next);
        toggle.setAttribute("aria-pressed", next);
        UI.toast(next ? "Блюдо снова в меню" : "Блюдо скрыто (стоп-лист)", "success");
      } catch (error) {
        UI.toast(adminError(error), "error");
      }
    }
    if (edit) {
      editingDish = dishes.find((x) => x.id === Number(edit.dataset.edit));
      renderDishes();
      window.scrollTo({ top: 0, behavior: "smooth" });
    }
    if (del) {
      if (!confirm("Удалить блюдо?")) return;
      try {
        await API.delete("/admin/dishes/" + del.dataset.deleteDish);
        UI.toast("Блюдо удалено", "success");
        renderDishes();
      } catch (error) {
        UI.toast(adminError(error), "error");
      }
    }
  });
}

// ---------- Категории ----------

async function renderCategories() {
  Data.categoriesCache = null;
  let categories;
  try {
    categories = await API.get("/categories");
  } catch (error) {
    main.innerHTML = "<h2>Категории</h2>" + errorBlock("Ошибка загрузки", error);
    return;
  }

  main.innerHTML = `
    <h2>Категории</h2>
    <div class="panel">
      <h3>Новая категория</h3>
      <form class="inline-form" id="create-form">
        <input class="input" id="new-name" placeholder="Например: Десерты" maxlength="100" required>
        <button class="btn" type="submit">+ Добавить</button>
      </form>
    </div>
    <div class="panel table-wrap">
      <table>
        <thead><tr><th style="width:70px">ID</th><th>Название</th><th></th></tr></thead>
        <tbody>
          ${categories.map((c) => `
            <tr>
              <td class="muted">${c.id}</td>
              <td><input class="input" value="${esc(c.name)}" data-name="${c.id}" maxlength="100"></td>
              <td class="actions">
                <button class="btn btn-ghost btn-sm" data-save="${c.id}">Сохранить</button>
                <button class="btn btn-danger btn-sm" data-delete="${c.id}">Удалить</button>
              </td>
            </tr>`).join("") || '<tr><td colspan="3" class="muted">Категорий пока нет</td></tr>'}
        </tbody>
      </table>
    </div>`;

  document.getElementById("create-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      const created = await API.post("/admin/categories", { name: document.getElementById("new-name").value });
      UI.toast(`Категория «${created.name}» создана`, "success");
      renderCategories();
    } catch (error) {
      UI.toast(adminError(error), "error");
    }
  });

  main.querySelector("tbody").addEventListener("click", async (e) => {
    const saveId = e.target.dataset.save;
    const deleteId = e.target.dataset.delete;
    if (saveId) {
      try {
        await API.put("/admin/categories/" + saveId, { name: main.querySelector(`[data-name="${saveId}"]`).value });
        UI.toast("Сохранено", "success");
      } catch (error) {
        UI.toast(adminError(error), "error");
      }
    }
    if (deleteId) {
      if (!confirm("Удалить категорию?")) return;
      try {
        await API.delete("/admin/categories/" + deleteId);
        UI.toast("Категория удалена", "success");
        renderCategories();
      } catch (error) {
        UI.toast(adminError(error), "error");
      }
    }
  });
}

// ---------- Пользователи ----------

async function renderUsers() {
  main.innerHTML = `
    <h2>Пользователи</h2>
    <div class="note"><span>🚧</span><span>Раздел ждёт бэкенд (RM-10): <code>GET /api/admin/users</code>, смена роли сотрудника.</span></div>`;
}

// ---------- Маршрутизация ----------

// Раздел заказов — через делегирование, чтобы работало и после автообновления.
main.addEventListener("click", async (e) => {
  const button = e.target.closest("[data-order]");
  if (!button) return;
  button.disabled = true;
  try {
    await API.put(`/admin/orders/${button.dataset.order}/status`, { status: button.dataset.to });
    UI.toast(`Заказ #${button.dataset.order}: ${STATUS_LABEL[button.dataset.to]}`, "success");
  } catch (error) {
    UI.toast(adminError(error), "error");
  }
  loadOrders();
});

const renderers = {
  dashboard: renderDashboard,
  analytics: renderAnalytics,
  assistant: renderAssistant,
  forecast: renderForecast,
  purchasing: renderPurchasing,
  inventory: renderInventory,
  orders: renderOrders,
  dishes: renderDishes,
  categories: renderCategories,
  users: renderUsers,
};

function allowedSections() {
  return Object.keys(SECTIONS).filter((key) => SECTIONS[key].roles.includes(role));
}

function route() {
  clearInterval(refreshTimer);
  const allowed = allowedSections();
  const name = location.hash.slice(1);
  const section = allowed.includes(name) ? name : allowed[0];

  document.querySelectorAll("#admin-nav a").forEach((a) =>
    a.classList.toggle("active", a.dataset.section === section));
  main.innerHTML = '<div class="skeleton" style="height:200px"></div>';
  renderers[section]();
}

// После смены темы перерисовываем раздел: у тепловой карты своя шкала для каждой темы.
window.addEventListener("themechange", () => {
  if (role) route();
});

// Перерисовать графики при изменении ширины окна (SVG строится под ширину контейнера).
let resizeTimer;
window.addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => {
    if (location.hash === "#analytics") loadAnalytics();
    if (location.hash === "#forecast") renderForecast();
    if (location.hash === "#purchasing" && lastPlan) loadPlan();
  }, 250);
});

// Проверка роли на клиенте — только для удобства интерфейса.
// Настоящая защита — RequireRole(...) на бэкенде.
if (Auth.requireLogin()) {
  initPage("admin");
  role = Auth.user().role;
  const allowed = allowedSections();

  if (!allowed.length) {
    document.getElementById("admin").innerHTML = `
      <div class="empty" style="grid-column:1/-1">
        <div class="big">🔒</div>
        <h3>Панель только для сотрудников</h3>
        <p>Ваша роль: ${esc(role)}</p>
        <a href="index.html" class="btn">На главную</a>
      </div>`;
  } else {
    const roleTitle = { admin: "АДМИНИСТРАТОР", owner: "ВЛАДЕЛЕЦ", waiter: "ОФИЦИАНТ", cook: "ПОВАР" }[role];
    document.getElementById("admin-nav").innerHTML =
      `<div class="side-title">${roleTitle}</div>` +
      allowed.map((key) => `<a href="#${key}" data-section="${key}">${SECTIONS[key].icon} ${SECTIONS[key].title}</a>`).join("");
    window.addEventListener("hashchange", route);
    route();
  }
}
