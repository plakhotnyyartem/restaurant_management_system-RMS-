/* =========================================================
   charts.js — графики на чистом SVG, без библиотек
   Столбцы (выручка по дням), тепловая карта (нагрузка),
   матрица инженерии меню (популярность × маржа)
   ========================================================= */

const SVG_NS = "http://www.w3.org/2000/svg";

function svgEl(tag, attrs = {}, parent) {
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  if (parent) parent.appendChild(el);
  return el;
}

// Подписи из данных вставляем только через textContent (не innerHTML).
function svgText(parent, x, y, text, attrs = {}) {
  const el = svgEl("text", { x, y, ...attrs }, parent);
  el.textContent = text;
  return el;
}

function compactNumber(value) {
  const abs = Math.abs(value);
  if (abs >= 1e6) return (value / 1e6).toFixed(abs >= 1e7 ? 0 : 1).replace(".0", "") + " млн";
  if (abs >= 1e3) return (value / 1e3).toFixed(abs >= 1e4 ? 0 : 1).replace(".0", "") + " тыс";
  return String(Math.round(value));
}

// «Красивый» шаг оси: 1, 2, 5 × 10^n
function niceTicks(max, count = 4) {
  if (max <= 0) return [0, 1];
  const raw = max / count;
  const pow = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 5, 10].map((m) => m * pow).find((s) => s >= raw);
  const ticks = [];
  for (let v = 0; v <= max + step * 0.999; v += step) ticks.push(v);
  return ticks;
}

// ---------- Всплывающая подсказка (одна на график) ----------

function createTooltip(container) {
  const tip = document.createElement("div");
  tip.className = "chart-tip";
  tip.hidden = true;
  container.appendChild(tip);

  return {
    // rows: [{ value, label }] — значение крупно, подпись второстепенно
    show(x, y, title, rows) {
      tip.replaceChildren();
      const head = document.createElement("div");
      head.className = "chart-tip-title";
      head.textContent = title;
      tip.appendChild(head);
      for (const row of rows) {
        const line = document.createElement("div");
        line.className = "chart-tip-row";
        const value = document.createElement("strong");
        value.textContent = row.value;
        const label = document.createElement("span");
        label.textContent = row.label;
        line.append(value, label);
        tip.appendChild(line);
      }
      tip.hidden = false;

      // Не даём подсказке вылезти за край контейнера.
      const box = container.getBoundingClientRect();
      const w = tip.offsetWidth;
      const h = tip.offsetHeight;
      let left = x + 14;
      if (left + w > box.width) left = x - w - 14;
      let top = y - h - 10;
      if (top < 0) top = y + 14;
      tip.style.left = Math.max(0, left) + "px";
      tip.style.top = top + "px";
    },
    hide() {
      tip.hidden = true;
    },
  };
}

function pointerIn(container, event) {
  const box = container.getBoundingClientRect();
  return { x: event.clientX - box.left, y: event.clientY - box.top };
}

// ---------- Столбчатый график по дням ----------

function barChart(container, points, { valueKey, format, extraRows } = {}) {
  container.replaceChildren();
  container.classList.add("chart");

  const width = Math.max(container.clientWidth, 300);
  const height = 260;
  const m = { top: 16, right: 12, bottom: 28, left: 56 };
  const w = width - m.left - m.right;
  const h = height - m.top - m.bottom;

  const svg = svgEl("svg", { viewBox: `0 0 ${width} ${height}`, width, height, role: "img" }, container);
  const values = points.map((p) => p[valueKey]);
  const ticks = niceTicks(Math.max(...values, 0));
  const max = ticks[ticks.length - 1];
  const y = (v) => m.top + h - (v / max) * h;

  // Сетка и подписи оси Y
  for (const t of ticks) {
    svgEl("line", { x1: m.left, x2: m.left + w, y1: y(t), y2: y(t), class: t === 0 ? "axis" : "grid" }, svg);
    svgText(svg, m.left - 8, y(t) + 4, compactNumber(t), { class: "tick", "text-anchor": "end" });
  }

  const band = w / points.length;
  const barW = Math.min(24, Math.max(band - 2, 1)); // тонкие столбцы, зазор между ними
  const labelEvery = Math.ceil(points.length / 8);

  let maxIndex = values.indexOf(Math.max(...values));
  const tooltip = createTooltip(container);

  points.forEach((p, i) => {
    const cx = m.left + band * i + band / 2;
    const top = y(p[valueKey]);
    const barH = m.top + h - top;

    // Скругление только у верхнего края, основание прямое.
    const r = Math.min(4, barH, barW / 2);
    const x0 = cx - barW / 2;
    const x1 = cx + barW / 2;
    const base = m.top + h;
    const d = barH <= 0 ? "" :
      `M${x0},${base} V${top + r} Q${x0},${top} ${x0 + r},${top} H${x1 - r} Q${x1},${top} ${x1},${top + r} V${base} Z`;
    const bar = svgEl("path", { d, class: "bar" }, svg);

    // Зона наведения — вся полоса дня, а не только тонкий столбец.
    const hit = svgEl("rect", { x: m.left + band * i, y: m.top, width: band, height: h, class: "hit", tabindex: 0 }, svg);
    const date = new Date(p.date + "T00:00:00");
    const title = date.toLocaleDateString("ru-RU", { weekday: "short", day: "numeric", month: "long" });
    const show = (event) => {
      bar.classList.add("active");
      const pos = event?.clientX ? pointerIn(container, event) : { x: cx, y: top };
      tooltip.show(pos.x, pos.y, title, [
        { value: format(p[valueKey]), label: "" },
        ...(extraRows ? extraRows(p) : []),
      ]);
    };
    const hide = () => { bar.classList.remove("active"); tooltip.hide(); };
    hit.addEventListener("pointermove", show);
    hit.addEventListener("pointerleave", hide);
    hit.addEventListener("focus", show);
    hit.addEventListener("blur", hide);

    if (i % labelEvery === 0) {
      svgText(svg, cx, height - 8, date.toLocaleDateString("ru-RU", { day: "numeric", month: "short" }).replace(".", ""), {
        class: "tick", "text-anchor": "middle",
      });
    }
  });

  // Подпись только у максимума — не число над каждым столбцом.
  if (maxIndex >= 0 && values[maxIndex] > 0) {
    const cx = m.left + band * maxIndex + band / 2;
    svgText(svg, cx, y(values[maxIndex]) - 6, "макс " + compactNumber(values[maxIndex]), {
      class: "direct-label", "text-anchor": cx > width - 60 ? "end" : "middle",
    });
  }
}

// ---------- Тепловая карта: день недели × час ----------

// Один оттенок синего. Малые значения «тонут» в фоне, большие — контрастны:
// на тёмном фоне шкала идёт к светлому, на светлом — к тёмному.
const HEAT_RAMP_DARK = ["#123055", "#104281", "#1c5cab", "#2a78d6", "#5598e7", "#86b6ef", "#b7d3f6"];
const HEAT_RAMP_LIGHT = ["#dbe9f8", "#b7d3f6", "#86b6ef", "#5598e7", "#2a78d6", "#1c5cab", "#104281"];

function heatRamp() {
  return typeof Theme !== "undefined" && Theme.current() === "light" ? HEAT_RAMP_LIGHT : HEAT_RAMP_DARK;
}

function heatColor(t) {
  const ramp = heatRamp();
  return ramp[Math.min(ramp.length - 1, Math.floor(t * ramp.length))];
}

function heatmap(container, cells) {
  container.replaceChildren();
  container.classList.add("chart");

  const days = ["Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"];
  const hours = [];
  for (let hr = 11; hr <= 22; hr++) hours.push(hr);

  const lookup = new Map(cells.map((c) => [c.weekday + ":" + c.hour, c.orders]));
  const max = Math.max(...cells.map((c) => c.orders), 1);

  const width = Math.max(container.clientWidth, 320);
  const m = { top: 8, right: 8, bottom: 26, left: 32 };
  const cellW = (width - m.left - m.right) / hours.length;
  const cellH = 30;
  const height = m.top + cellH * 7 + m.bottom;

  const svg = svgEl("svg", { viewBox: `0 0 ${width} ${height}`, width, height, role: "img" }, container);
  const tooltip = createTooltip(container);

  days.forEach((day, di) => {
    svgText(svg, m.left - 8, m.top + cellH * di + cellH / 2 + 4, day, { class: "tick", "text-anchor": "end" });
    hours.forEach((hr, hi) => {
      const value = lookup.get(di + 1 + ":" + hr) || 0;
      const x = m.left + cellW * hi;
      const y = m.top + cellH * di;
      // 2px зазор цвета фона между ячейками
      const rect = svgEl("rect", {
        x: x + 1, y: y + 1, width: cellW - 2, height: cellH - 2, rx: 3,
        fill: value ? heatColor(value / max) : "var(--surface-2)",
        class: "cell", tabindex: 0,
      }, svg);
      const show = (event) => {
        rect.classList.add("active");
        const pos = event?.clientX ? pointerIn(container, event) : { x: x + cellW / 2, y };
        tooltip.show(pos.x, pos.y, `${day}, ${hr}:00–${hr + 1}:00`, [
          { value: value.toFixed(1), label: "заказов в среднем" },
        ]);
      };
      const hide = () => { rect.classList.remove("active"); tooltip.hide(); };
      rect.addEventListener("pointermove", show);
      rect.addEventListener("pointerleave", hide);
      rect.addEventListener("focus", show);
      rect.addEventListener("blur", hide);
    });
  });

  hours.forEach((hr, hi) => {
    if (hi % 2 === 0) {
      svgText(svg, m.left + cellW * hi + cellW / 2, height - 8, hr + ":00", { class: "tick", "text-anchor": "middle" });
    }
  });

  // Шкала-легенда
  const legend = document.createElement("div");
  legend.className = "heat-legend";
  const low = document.createElement("span");
  low.textContent = "меньше";
  const high = document.createElement("span");
  high.textContent = "больше заказов";
  const bar = document.createElement("div");
  bar.className = "heat-legend-bar";
  bar.style.background = `linear-gradient(90deg, ${heatRamp().join(", ")})`;
  legend.append(low, bar, high);
  container.appendChild(legend);
}

// ---------- Матрица инженерии меню ----------

const CLASS_INFO = {
  star: { icon: "⭐", name: "Звёзды", hint: "популярные и выгодные" },
  plowhorse: { icon: "🐴", name: "Рабочие лошадки", hint: "популярные, маржа ниже средней" },
  puzzle: { icon: "❓", name: "Загадки", hint: "выгодные, но редко заказывают" },
  dog: { icon: "🐶", name: "Собаки", hint: "мало продаж и низкая маржа" },
};

function menuMatrix(container, report) {
  container.replaceChildren();
  container.classList.add("chart");

  const dishes = report.dishes.filter((d) => d.quantity > 0);
  const width = Math.max(container.clientWidth, 320);
  const height = 380;
  const m = { top: 20, right: 20, bottom: 40, left: 64 };
  const w = width - m.left - m.right;
  const h = height - m.top - m.bottom;

  const maxPop = Math.max(...dishes.map((d) => d.popularity), report.popularity_threshold) * 1.08;
  const maxMargin = Math.max(...dishes.map((d) => d.unit_margin), report.margin_threshold) * 1.1;
  const x = (v) => m.left + (v / maxPop) * w;
  const y = (v) => m.top + h - (v / maxMargin) * h;

  const svg = svgEl("svg", { viewBox: `0 0 ${width} ${height}`, width, height, role: "img" }, container);

  // Оси и сетка
  for (const t of niceTicks(maxMargin, 4).filter((t) => t <= maxMargin)) {
    svgEl("line", { x1: m.left, x2: m.left + w, y1: y(t), y2: y(t), class: t === 0 ? "axis" : "grid" }, svg);
    svgText(svg, m.left - 8, y(t) + 4, compactNumber(t) + " ₸", { class: "tick", "text-anchor": "end" });
  }
  for (const t of niceTicks(maxPop, 5).filter((t) => t <= maxPop)) {
    svgText(svg, x(t), m.top + h + 18, t + "%", { class: "tick", "text-anchor": "middle" });
  }
  svgText(svg, m.left + w, height - 4, "Популярность (доля продаж) →", { class: "axis-title", "text-anchor": "end" });
  svgText(svg, m.left, 12, "↑ Маржа с порции", { class: "axis-title" });

  // Пороговые линии делят поле на 4 квадранта
  const tx = x(report.popularity_threshold);
  const ty = y(report.margin_threshold);
  svgEl("line", { x1: tx, x2: tx, y1: m.top, y2: m.top + h, class: "threshold" }, svg);
  svgEl("line", { x1: m.left, x2: m.left + w, y1: ty, y2: ty, class: "threshold" }, svg);

  const corner = (cls, qx, qy, anchor) => {
    const info = CLASS_INFO[cls];
    svgText(svg, qx, qy, info.icon + " " + info.name, { class: "quadrant-label", "text-anchor": anchor });
  };
  corner("puzzle", m.left + 8, m.top + 14, "start");
  corner("star", m.left + w - 8, m.top + 14, "end");
  corner("dog", m.left + 8, m.top + h - 8, "start");
  corner("plowhorse", m.left + w - 8, m.top + h - 8, "end");

  const tooltip = createTooltip(container);

  // Подписываем выборочно, по приоритету: «загадки», три самых популярных, «собаки».
  // Подпись, которая наложилась бы на уже размещённую, пропускаем —
  // значение остаётся в подсказке и в таблице под графиком.
  const top3 = [...dishes].sort((a, b) => b.quantity - a.quantity).slice(0, 3).map((d) => d.id);
  const priority = (d) => (d.class === "puzzle" ? 0 : top3.includes(d.id) ? 1 : d.class === "dog" ? 2 : 9);
  const placed = [];
  const overlaps = (a, b) => a.x < b.x + b.width + 4 && b.x < a.x + a.width + 4 && a.y < b.y + b.height && b.y < a.y + a.height;
  const labelLayer = svgEl("g", {}, svg);

  for (const d of dishes) {
    const cx = x(d.popularity);
    const cy = y(d.unit_margin);
    const g = svgEl("g", { class: "dot", tabindex: 0 }, svg);
    svgEl("circle", { cx, cy, r: 14, class: "hit" }, g); // зона наведения больше точки
    svgEl("circle", { cx, cy, r: 5, class: "dot-mark" }, g);


    const show = (event) => {
      g.classList.add("active");
      const pos = event?.clientX ? pointerIn(container, event) : { x: cx, y: cy };
      tooltip.show(pos.x, pos.y, CLASS_INFO[d.class].icon + " " + d.name, [
        { value: d.popularity + "%", label: "доля продаж" },
        { value: money(d.unit_margin), label: "маржа с порции" },
        { value: d.quantity + " шт", label: "продано" },
      ]);
    };
    const hide = () => { g.classList.remove("active"); tooltip.hide(); };
    g.addEventListener("pointermove", show);
    g.addEventListener("pointerleave", hide);
    g.addEventListener("focus", show);
    g.addEventListener("blur", hide);
  }

  // Подписи ставим после точек, в порядке приоритета.
  for (const d of [...dishes].sort((a, b) => priority(a) - priority(b))) {
    if (priority(d) > 2) continue;
    const cx = x(d.popularity);
    const cy = y(d.unit_margin);
    const right = cx > width - 130;
    const label = svgText(labelLayer, right ? cx - 10 : cx + 10, cy + 4, d.name, {
      class: "dot-label", "text-anchor": right ? "end" : "start",
    });
    const box = label.getBBox();
    if (placed.some((p) => overlaps(box, p))) {
      label.remove();
    } else {
      placed.push(box);
    }
  }
}

// ---------- Прогноз: факт + прогноз с интервалом ----------

function forecastChart(container, history, forecast) {
  container.replaceChildren();
  container.classList.add("chart");

  const points = [
    ...history.map((p) => ({ date: p.date, actual: p.value })),
    ...forecast.map((p) => ({ date: p.date, value: p.value, low: p.low, high: p.high, revenue: p.revenue })),
  ];

  const width = Math.max(container.clientWidth, 320);
  const height = 280;
  const m = { top: 16, right: 16, bottom: 28, left: 44 };
  const w = width - m.left - m.right;
  const h = height - m.top - m.bottom;

  const maxValue = Math.max(...points.map((p) => p.high ?? p.actual));
  const ticks = niceTicks(maxValue);
  const max = ticks[ticks.length - 1];
  const x = (i) => m.left + (i / (points.length - 1)) * w;
  const y = (v) => m.top + h - (v / max) * h;

  const svg = svgEl("svg", { viewBox: `0 0 ${width} ${height}`, width, height, role: "img" }, container);

  for (const t of ticks) {
    svgEl("line", { x1: m.left, x2: m.left + w, y1: y(t), y2: y(t), class: t === 0 ? "axis" : "grid" }, svg);
    svgText(svg, m.left - 8, y(t) + 4, compactNumber(t), { class: "tick", "text-anchor": "end" });
  }

  const first = history.length; // индекс первого дня прогноза (сегодня)

  // Коридор 80%-го интервала — заливка с низкой прозрачностью.
  const band = points.slice(first).map((p, i) => [x(first + i), y(p.high)])
    .concat(points.slice(first).map((p, i) => [x(first + i), y(p.low)]).reverse());
  svgEl("path", { d: "M" + band.map((pt) => pt.join(",")).join(" L") + " Z", class: "fc-band" }, svg);

  // «Сегодня»
  svgEl("line", { x1: x(first), x2: x(first), y1: m.top, y2: m.top + h, class: "threshold" }, svg);
  svgText(svg, x(first) + 6, m.top + 10, "сегодня", { class: "axis-title" });

  // Линия факта соединяется с началом прогноза, чтобы не было разрыва.
  const actualPath = history.map((p, i) => `${x(i)},${y(p.value)}`);
  svgEl("path", { d: "M" + actualPath.join(" L"), class: "fc-actual" }, svg);
  const fcPath = [`${x(first - 1)},${y(history[history.length - 1].value)}`]
    .concat(forecast.map((p, i) => `${x(first + i)},${y(p.value)}`));
  svgEl("path", { d: "M" + fcPath.join(" L"), class: "fc-line" }, svg);

  // Подписи дат по оси X
  const every = Math.ceil(points.length / 8);
  points.forEach((p, i) => {
    if (i % every === 0) {
      const d = new Date(p.date + "T00:00:00");
      svgText(svg, x(i), height - 8, d.toLocaleDateString("ru-RU", { day: "numeric", month: "short" }).replace(".", ""), {
        class: "tick", "text-anchor": "middle",
      });
    }
  });

  // Подпись зоны прогноза — в свободном верхнем углу, не поверх линий
  svgText(svg, m.left + w, m.top + 10, "прогноз →", { class: "direct-label", "text-anchor": "end" });

  // Перекрестие: вертикальная линия прилипает к ближайшему дню
  const cross = svgEl("line", { y1: m.top, y2: m.top + h, class: "crosshair", visibility: "hidden" }, svg);
  const dot = svgEl("circle", { r: 4, class: "dot-mark", visibility: "hidden" }, svg);
  const hit = svgEl("rect", { x: m.left, y: m.top, width: w, height: h, class: "hit", tabindex: 0 }, svg);
  const tooltip = createTooltip(container);

  const showAt = (i, pos) => {
    const p = points[i];
    const value = p.actual ?? p.value;
    cross.setAttribute("x1", x(i));
    cross.setAttribute("x2", x(i));
    cross.setAttribute("visibility", "visible");
    dot.setAttribute("cx", x(i));
    dot.setAttribute("cy", y(value));
    dot.setAttribute("visibility", "visible");
    const d = new Date(p.date + "T00:00:00");
    const title = d.toLocaleDateString("ru-RU", { weekday: "short", day: "numeric", month: "long" });
    const rows = p.actual !== undefined
      ? [{ value: String(p.actual), label: "заказов (факт)" }]
      : [
          { value: "~" + p.value, label: "заказов (прогноз)" },
          { value: `${p.low}–${p.high}`, label: "интервал 80%" },
          { value: money(p.revenue), label: "выручка" },
        ];
    tooltip.show(pos.x, pos.y, title, rows);
  };
  const hide = () => {
    cross.setAttribute("visibility", "hidden");
    dot.setAttribute("visibility", "hidden");
    tooltip.hide();
  };

  hit.addEventListener("pointermove", (event) => {
    const pos = pointerIn(container, event);
    const i = Math.round(((pos.x - m.left) / w) * (points.length - 1));
    showAt(Math.max(0, Math.min(points.length - 1, i)), pos);
  });
  hit.addEventListener("pointerleave", hide);
  hit.addEventListener("focus", () => showAt(first, { x: x(first), y: y(points[first].value) }));
  hit.addEventListener("blur", hide);

  // Легенда: два ряда + интервал (ключ — короткая линия, как сама отметка)
  const legend = document.createElement("div");
  legend.className = "chart-legend";
  for (const [cls, text] of [["key-actual", "Факт"], ["key-forecast", "Прогноз"], ["key-band", "Интервал 80%"]]) {
    const item = document.createElement("span");
    const key = document.createElement("i");
    key.className = cls;
    const label = document.createElement("span");
    label.textContent = text;
    item.append(key, label);
    legend.appendChild(item);
  }
  container.appendChild(legend);
}

// ---------- Кривая «бюджет → прибыль» ----------

function budgetCurveChart(container, curve, current) {
  container.replaceChildren();
  container.classList.add("chart");

  const width = Math.max(container.clientWidth, 300);
  const height = 240;
  const m = { top: 16, right: 20, bottom: 34, left: 56 };
  const w = width - m.left - m.right;
  const h = height - m.top - m.bottom;

  const maxBudget = Math.max(...curve.map((p) => p.budget), current, 1);
  const ticks = niceTicks(Math.max(...curve.map((p) => p.profit)));
  const maxProfit = ticks[ticks.length - 1];
  const x = (v) => m.left + (v / maxBudget) * w;
  const y = (v) => m.top + h - (v / maxProfit) * h;

  const svg = svgEl("svg", { viewBox: `0 0 ${width} ${height}`, width, height, role: "img" }, container);
  for (const t of ticks) {
    svgEl("line", { x1: m.left, x2: m.left + w, y1: y(t), y2: y(t), class: t === 0 ? "axis" : "grid" }, svg);
    svgText(svg, m.left - 8, y(t) + 4, compactNumber(t), { class: "tick", "text-anchor": "end" });
  }
  for (const p of curve) {
    svgText(svg, x(p.budget), m.top + h + 16, compactNumber(p.budget), { class: "tick", "text-anchor": "middle" });
  }
  svgText(svg, m.left + w, height - 2, "Бюджет закупки, ₸ →", { class: "axis-title", "text-anchor": "end" });

  // Текущий бюджет — вертикальная линия
  svgEl("line", { x1: x(current), x2: x(current), y1: m.top, y2: m.top + h, class: "threshold" }, svg);
  svgText(svg, x(current) + (x(current) > width - 100 ? -6 : 6), m.top + 10, "ваш бюджет", {
    class: "axis-title", "text-anchor": x(current) > width - 100 ? "end" : "start",
  });

  svgEl("path", { d: "M" + curve.map((p) => `${x(p.budget)},${y(p.profit)}`).join(" L"), class: "fc-actual" }, svg);

  const tooltip = createTooltip(container);
  for (const p of curve) {
    const g = svgEl("g", { class: "dot", tabindex: 0 }, svg);
    svgEl("circle", { cx: x(p.budget), cy: y(p.profit), r: 14, class: "hit" }, g);
    svgEl("circle", { cx: x(p.budget), cy: y(p.profit), r: 4.5, class: "dot-mark" }, g);
    const show = (event) => {
      g.classList.add("active");
      const pos = event?.clientX ? pointerIn(container, event) : { x: x(p.budget), y: y(p.profit) };
      tooltip.show(pos.x, pos.y, "Бюджет " + money(p.budget), [
        { value: money(p.profit), label: "прибыль недели" },
        { value: p.coverage_pct + "%", label: "спроса обеспечено" },
      ]);
    };
    const hide = () => { g.classList.remove("active"); tooltip.hide(); };
    g.addEventListener("pointermove", show);
    g.addEventListener("pointerleave", hide);
    g.addEventListener("focus", show);
    g.addEventListener("blur", hide);
  }
}
