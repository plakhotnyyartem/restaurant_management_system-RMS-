/* =========================================================
   app.js — общий код для всех страниц
   API-клиент, авторизация, корзина, шапка, уведомления
   ========================================================= */

// ---------- Утилиты ----------

// Экранирование HTML: всё, что пришло с сервера или от пользователя,
// вставляем в разметку только через esc(), чтобы не было XSS.
function esc(value) {
  return String(value ?? "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[ch]);
}

function money(value) {
  return Number(value).toLocaleString("ru-RU") + " ₸";
}

// Русское склонение: plural(5, "блюдо", "блюда", "блюд") → "блюд"
function plural(n, one, few, many) {
  const mod10 = n % 10, mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) return one;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return few;
  return many;
}

function qs(name) {
  return new URLSearchParams(location.search).get(name);
}

// localStorage может быть недоступен (приватный режим) — не падаем.
const store = {
  get(key, fallback) {
    try {
      const raw = localStorage.getItem(key);
      return raw ? JSON.parse(raw) : fallback;
    } catch {
      return fallback;
    }
  },
  set(key, value) {
    try { localStorage.setItem(key, JSON.stringify(value)); } catch {}
  },
  remove(key) {
    try { localStorage.removeItem(key); } catch {}
  },
};

// ---------- Авторизация ----------

function jwtPayload(token) {
  try {
    const base64 = token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/");
    return JSON.parse(atob(base64));
  } catch {
    return null;
  }
}

const Auth = {
  token() {
    const token = store.get("rm_token", null);
    if (!token) return null;

    // Токен живёт 24 часа (exp в секундах) — просроченный сразу выкидываем.
    const payload = jwtPayload(token);
    if (!payload || payload.exp * 1000 < Date.now()) {
      this.logout();
      return null;
    }
    return token;
  },
  user() {
    return this.token() ? store.get("rm_user", null) : null;
  },
  isLoggedIn() {
    return !!this.token();
  },
  hasRole(...roles) {
    const user = this.user();
    return !!user && roles.includes(user.role);
  },
  save(token, user) {
    store.set("rm_token", token);
    store.set("rm_user", user);
  },
  logout() {
    store.remove("rm_token");
    store.remove("rm_user");
  },
  // Отправить на страницу входа и вернуть обратно после логина.
  requireLogin() {
    if (this.isLoggedIn()) return true;
    const next = location.pathname.split("/").pop() + location.search;
    location.href = "login.html?next=" + encodeURIComponent(next);
    return false;
  },
};

// ---------- API-клиент (fetch) ----------

const API = {
  async request(method, path, body) {
    const headers = { "Content-Type": "application/json" };
    const token = Auth.token();
    if (token) headers.Authorization = "Bearer " + token;

    let response;
    try {
      response = await fetch("/api" + path, {
        method,
        headers,
        body: body ? JSON.stringify(body) : undefined,
      });
    } catch {
      const error = new Error("Сервер недоступен");
      error.status = 0;
      throw error;
    }

    let data = null;
    try { data = await response.json(); } catch {}

    if (!response.ok) {
      const error = new Error(data?.error || "HTTP " + response.status);
      error.status = response.status;
      throw error;
    }
    return data;
  },
  get(path) { return this.request("GET", path); },
  post(path, body) { return this.request("POST", path, body); },
  put(path, body) { return this.request("PUT", path, body); },
  delete(path) { return this.request("DELETE", path); },

  // true, если эндпоинта ещё нет на бэкенде (или сервер не запущен).
  // Бэкенд отвечает {"error":"endpoint not found"} на любые неизвестные /api/* пути.
  isMissing(error) {
    return error.status === 0 || (error.status === 404 && error.message === "endpoint not found");
  },
};

// ---------- Демо-данные ----------
// Используются, пока на бэкенде нет Dishes API (RM-6).
// Как только появится GET /api/dishes — фронт сам переключится на реальные данные.

const CATEGORY_EMOJI = {
  "Пицца": "🍕",
  "Суши": "🍣",
  "Бургеры": "🍔",
  "Напитки": "🥤",
  "Салаты": "🥗",
  "Десерты": "🍰",
  "Супы": "🍲",
};

const DEMO_CATEGORIES = [
  { id: 1, name: "Пицца" },
  { id: 2, name: "Суши" },
  { id: 3, name: "Бургеры" },
  { id: 4, name: "Напитки" },
];

const DEMO_DISHES = [
  { id: 1, name: "Пепперони", category_id: 1, price: 3200, emoji: "🍕", is_available: true,
    description: "Томатный соус, моцарелла и острая пепперони на тонком тесте из дровяной печи." },
  { id: 2, name: "Маргарита", category_id: 1, price: 2600, emoji: "🍕", is_available: true,
    description: "Классика: томаты, моцарелла, свежий базилик и оливковое масло." },
  { id: 3, name: "Четыре сыра", category_id: 1, price: 3500, emoji: "🧀", is_available: true,
    description: "Моцарелла, горгонзола, пармезан и чеддер на сливочном соусе." },
  { id: 4, name: "Филадельфия", category_id: 2, price: 3900, emoji: "🍣", is_available: true,
    description: "Лосось, сливочный сыр, огурец и рис. 8 штук." },
  { id: 5, name: "Калифорния", category_id: 2, price: 3400, emoji: "🍱", is_available: true,
    description: "Краб, авокадо, огурец и икра масаго. 8 штук." },
  { id: 6, name: "Дракон", category_id: 2, price: 4200, emoji: "🐉", is_available: false,
    description: "Угорь, авокадо, сливочный сыр и соус унаги." },
  { id: 7, name: "Чизбургер", category_id: 3, price: 2400, emoji: "🍔", is_available: true,
    description: "Говяжья котлета, чеддер, маринованные огурцы и фирменный соус." },
  { id: 8, name: "Бургер BBQ", category_id: 3, price: 2900, emoji: "🍔", is_available: true,
    description: "Двойная котлета, бекон, луковые кольца и соус барбекю." },
  { id: 9, name: "Картофель фри", category_id: 3, price: 900, emoji: "🍟", is_available: true,
    description: "Хрустящий картофель с морской солью. Подаётся с соусом на выбор." },
  { id: 10, name: "Мохито", category_id: 4, price: 1200, emoji: "🍹", is_available: true,
    description: "Безалкогольный: лайм, мята, тростниковый сахар и содовая." },
  { id: 11, name: "Лимонад манго", category_id: 4, price: 1100, emoji: "🥭", is_available: true,
    description: "Домашний лимонад из пюре манго и маракуйи." },
  { id: 12, name: "Капучино", category_id: 4, price: 900, emoji: "☕", is_available: true,
    description: "Двойной эспрессо и нежная молочная пенка." },
];

// ---------- Слой данных ----------

function normalizeDish(dish) {
  return { ...dish, price: Number(dish.price), is_available: dish.is_available !== false };
}

const Data = {
  demo: false, // true — показываем демо-блюда
  categoriesCache: null,
  dishesCache: null,

  async categories() {
    if (this.categoriesCache) return this.categoriesCache;
    try {
      const list = await API.get("/categories");
      this.categoriesCache = list.length ? list : DEMO_CATEGORIES;
    } catch {
      this.categoriesCache = DEMO_CATEGORIES;
    }
    return this.categoriesCache;
  },

  async dishes() {
    if (this.dishesCache) return this.dishesCache;
    try {
      const list = await API.get("/dishes");
      this.dishesCache = list.map(normalizeDish);
      this.demo = false;
    } catch (error) {
      if (!API.isMissing(error)) throw error;
      this.dishesCache = DEMO_DISHES;
      this.demo = true;
    }
    return this.dishesCache;
  },

  async dish(id) {
    try {
      return normalizeDish(await API.get("/dishes/" + id));
    } catch (error) {
      if (!API.isMissing(error)) return null;
      this.demo = true;
      return DEMO_DISHES.find((d) => d.id === Number(id)) || null;
    }
  },
};

function categoryName(categories, id) {
  return categories.find((c) => c.id === id)?.name || "";
}

function dishEmoji(dish, categories) {
  return dish.emoji || CATEGORY_EMOJI[categoryName(categories, dish.category_id)] || "🍽️";
}

// ---------- Корзина ----------

const Cart = {
  items() {
    return store.get("rm_cart", []);
  },
  save(items) {
    store.set("rm_cart", items);
    UI.updateCartBadge(true);
  },
  add(dish, emoji, quantity = 1) {
    const items = this.items();
    const existing = items.find((i) => i.id === dish.id);
    if (existing) {
      existing.quantity += quantity;
    } else {
      items.push({ id: dish.id, name: dish.name, price: dish.price, emoji, quantity });
    }
    this.save(items);
  },
  setQuantity(id, quantity) {
    let items = this.items();
    if (quantity <= 0) {
      items = items.filter((i) => i.id !== id);
    } else {
      const item = items.find((i) => i.id === id);
      if (item) item.quantity = quantity;
    }
    this.save(items);
  },
  remove(id) {
    this.save(this.items().filter((i) => i.id !== id));
  },
  clear() {
    this.save([]);
  },
  count() {
    return this.items().reduce((sum, i) => sum + i.quantity, 0);
  },
  total() {
    return this.items().reduce((sum, i) => sum + i.price * i.quantity, 0);
  },
};

// ---------- UI: шапка, футер, тосты, карточки ----------

// Логотип RMS — контуры из «щбш лого.svg» (ваш исходный файл).
// Буквы красятся цветом текста (currentColor), точки — акцентным цветом;
// точки вращаются рывками по 60°, как индикатор загрузки (.logo-spin в CSS).
const LOGO_ICON = `<circle cx="252" cy="80" r="80"/><circle cx="252" cy="472" r="80"/><circle cx="424" cy="380" r="80"/><circle cx="80" cy="380" r="80"/><circle cx="80" cy="175" r="80"/><path d="M497.758 192.492C488.7 235.737 463.4 238.523 403.057 254.393C237.624 297.904 222.579 291.688 341.156 159.693C371.615 125.786 392.612 88.7341 435.857 97.7915C479.101 106.849 506.815 149.248 497.758 192.492Z"/>`;
const LOGO_PIVOT = `<circle cx="252" cy="276" r="290" style="fill:none;stroke:none"/>`;
const LOGO_LETTERS = `<path d="M783.624 171.514C801.786 171.536 816.678 177.228 828.299 188.592C839.664 177.256 854.569 171.6 873.015 171.622L906.217 171.662C920.831 171.68 934.165 175.314 946.217 182.563C958.269 189.671 967.835 199.259 974.915 211.326C982.137 223.252 985.738 236.521 985.721 251.133L985.55 392.219L940.854 392.165L941.024 251.717C941.036 241.928 937.641 233.624 930.839 226.807C924.037 219.847 915.811 216.361 906.163 216.35L872.96 216.31C866.717 216.302 861.394 218.282 856.99 222.249C852.587 226.074 850.383 230.753 850.376 236.285L850.187 392.055L805.278 392L805.468 236.231C805.474 230.699 803.352 226.014 799.1 222.179C794.848 218.201 789.671 216.209 783.57 216.201L716.102 216.119L715.888 391.892L670.979 391.837L671.192 216.277C671.207 203.935 675.619 193.371 684.427 184.586C693.235 175.801 703.882 171.417 716.368 171.432L783.624 171.514ZM654.436 171.463L654.381 216.15L605.217 216.091C600.109 216.085 595.708 217.853 592.015 221.395C588.463 224.795 586.684 228.978 586.678 233.943L586.486 391.841L541.578 391.786L541.771 233.038C541.784 221.689 544.635 211.336 550.322 201.979C556.151 192.623 563.822 185.184 573.335 179.663C582.99 174.142 593.636 171.389 605.271 171.403L654.436 171.463ZM1187.69 86.5859C1195.5 86.5954 1202.02 89.299 1207.26 94.6963C1212.65 99.9518 1215.34 106.481 1215.33 114.283C1215.32 122.086 1212.62 128.68 1207.22 134.064C1201.96 139.307 1198.24 137.86 1187.63 141.914C1178.24 145.497 1154.63 158.756 1149.24 153.359C1143.86 147.962 1156.96 123.217 1159.99 114.217C1162.3 107.36 1162.7 100.033 1168.1 94.6484C1173.5 89.2641 1180.03 86.5766 1187.69 86.5859Z"/><path d="M1165.42 172.388L1166.17 172.389L1166.17 173.139L1166.13 205.468L1166.13 206.218L1165.38 206.217L1052.3 206.079C1046.56 206.072 1041.63 208.092 1037.46 212.159C1033.29 216.223 1031.22 221.108 1031.21 226.856L1031.19 241.943C1031.19 247.156 1033.23 251.631 1037.38 255.404C1041.54 259.181 1046.48 261.071 1052.24 261.078L1121.83 261.163C1131.99 261.175 1141.24 263.629 1149.54 268.532H1149.54C1157.84 273.326 1164.43 279.837 1169.3 288.058L1169.76 288.823C1174.43 296.757 1176.76 305.519 1176.75 315.093L1176.72 338.954C1176.71 348.837 1174.2 357.899 1169.2 366.121C1164.32 374.229 1157.71 380.723 1149.41 385.599L1149.4 385.602C1141.08 390.382 1131.83 392.762 1121.67 392.75L997.999 392.6L997.249 392.599L997.25 391.849L997.289 359.365L997.29 358.615L998.04 358.616L1121.71 358.767C1127.57 358.774 1132.51 356.847 1136.57 352.988L1136.95 352.614C1140.86 348.728 1142.8 344.121 1142.8 338.76L1142.83 315.205C1142.84 309.671 1140.78 304.935 1136.63 300.961L1136.62 300.955C1132.57 296.986 1127.64 294.999 1121.79 294.992L1052.2 294.908C1042.14 294.896 1032.9 292.545 1024.49 287.852L1024.48 287.847C1016.19 283.055 1009.55 276.648 1004.58 268.633L1004.58 268.627C999.85 260.848 997.42 252.287 997.284 242.961L997.278 242.057L997.297 225.892C997.309 216.012 999.761 207.002 1004.66 198.881L1004.66 198.875C1009.5 191.022 1015.89 184.73 1023.84 180.005L1024.61 179.552C1033.03 174.67 1042.28 172.238 1052.34 172.25L1165.42 172.388Z"/>`;

function logoSvg() {
  return `
    <svg viewBox="0 0 1216 552" aria-hidden="true" focusable="false">
      <g class="logo-icon logo-spin">${LOGO_PIVOT}${LOGO_ICON}</g>
      <g fill="currentColor" stroke="currentColor" stroke-width="1.5">${LOGO_LETTERS}</g>
    </svg>`;
}

const UI = {
  // Реестр отрисованных блюд, чтобы кнопка «В корзину» знала, что добавлять.
  dishIndex: new Map(),

  renderHeader(active) {
    const user = Auth.user();
    const links = [
      { href: "index.html", key: "home", label: "Главная" },
      { href: "menu.html", key: "menu", label: "Меню" },
      { href: "cart.html", key: "cart", label: 'Корзина<span class="cart-badge" id="cart-badge">0</span>' },
      user
        ? { href: "profile.html", key: "profile", label: "👤 " + esc(user.name) }
        : { href: "login.html", key: "login", label: "Войти" },
    ];
    const panelLabel = { admin: "Админка", owner: "Аналитика", waiter: "Заказы", cook: "Кухня" }[user?.role];
    if (panelLabel) {
      links.push({ href: "admin.html", key: "admin", label: panelLabel });
    }

    const header = document.getElementById("site-header");
    header.className = "site-header";
    header.innerHTML = `
      <div class="container header-inner">
        <a href="index.html" class="logo" aria-label="RMS — на главную">
          ${logoSvg()}
        </a>
        <nav class="nav">
          ${links.map((l) => `<a href="${l.href}" class="${l.key === active ? "active" : ""}">${l.label}</a>`).join("")}
        </nav>
        <div class="header-actions">
          <button class="theme-toggle" type="button" aria-label="Переключить тему">
            <svg class="sun" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
              <circle cx="12" cy="12" r="4.5"/>
              <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>
            </svg>
            <svg class="moon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" aria-hidden="true">
              <path d="M20.5 14.5A8.5 8.5 0 0 1 9.5 3.5a8.5 8.5 0 1 0 11 11z"/>
            </svg>
          </button>
          <button class="burger" aria-label="Меню" aria-expanded="false">☰</button>
        </div>
      </div>`;

    header.querySelector(".theme-toggle").addEventListener("click", (event) => {
      Theme.toggle(event.currentTarget);
    });

    const burger = header.querySelector(".burger");
    const nav = header.querySelector(".nav");
    burger.addEventListener("click", () => {
      const open = nav.classList.toggle("open");
      burger.setAttribute("aria-expanded", open);
    });

    this.updateCartBadge(false);
  },

  renderFooter() {
    const footer = document.getElementById("site-footer");
    if (!footer) return;
    footer.className = "site-footer";
    footer.innerHTML = `
      <div class="container footer-inner">
        <div>
          <a href="index.html" class="logo logo-lg" aria-label="RMS — на главную">${logoSvg()}</a>
          <p>Ресторан, в котором заказ идёт от вашего клика до кухни без бумажек.</p>
        </div>
        <div>
          <h4>Контакты</h4>
          <p>📍 Алматы, пр. Абая, 10</p>
          <p>📞 +7 (700) 000-00-00</p>
          <p>✉️ hello@rms.kz</p>
          <p id="footer-telegram" hidden></p>
        </div>
        <div>
          <h4>Часы работы</h4>
          <p>Пн–Чт: 11:00 – 23:00</p>
          <p>Пт–Вс: 11:00 – 01:00</p>
        </div>
      </div>`;

    // Ссылка на бота появляется, только если бот запущен на сервере.
    TelegramBot.info().then((bot) => {
      const line = document.getElementById("footer-telegram");
      if (!bot || !line) return;
      line.innerHTML = `✈️ <a class="tg-link" href="${esc(bot.url)}" target="_blank" rel="noopener">@${esc(bot.username)}</a> — бот в Telegram`;
      line.hidden = false;
    });
  },

  updateCartBadge(animate) {
    const badge = document.getElementById("cart-badge");
    if (!badge) return;
    const count = Cart.count();
    badge.textContent = count;
    badge.classList.toggle("hidden", count === 0);
    if (animate) {
      badge.classList.remove("bump");
      void badge.offsetWidth; // перезапуск CSS-анимации
      badge.classList.add("bump");
    }
  },

  toast(message, type = "info") {
    let box = document.querySelector(".toasts");
    if (!box) {
      box = document.createElement("div");
      box.className = "toasts";
      document.body.appendChild(box);
    }
    const el = document.createElement("div");
    el.className = "toast " + type;
    el.textContent = message;
    box.appendChild(el);
    setTimeout(() => {
      el.classList.add("out");
      el.addEventListener("animationend", () => el.remove());
    }, 3200);
  },

  dishCard(dish, categories, index = 0) {
    const emoji = dishEmoji(dish, categories);
    this.dishIndex.set(dish.id, { dish, emoji });
    return `
      <article class="dish-card" style="animation-delay:${Math.min(index, 12) * 40}ms">
        <a href="dish.html?id=${dish.id}" class="dish-media">
          ${dish.image_url
            ? `<img src="${esc(dish.image_url)}" alt="${esc(dish.name)}" loading="lazy">`
            : `<span class="emoji">${emoji}</span>`}
          ${dish.is_available ? "" : '<span class="tag off">Нет в наличии</span>'}
        </a>
        <div class="dish-body">
          <span class="cat">${esc(categoryName(categories, dish.category_id))}</span>
          <h3><a href="dish.html?id=${dish.id}">${esc(dish.name)}</a></h3>
          <p>${esc(dish.description)}</p>
          <div class="dish-foot">
            <span class="price">${money(dish.price)}</span>
            <button class="btn btn-sm" data-add="${dish.id}" ${dish.is_available ? "" : "disabled"}>В корзину</button>
          </div>
        </div>
      </article>`;
  },

  demoNote() {
    return `
      <div class="note">
        <span>🧪</span>
        <span>Показаны демо-блюда: на бэкенде ещё нет <code>GET /api/dishes</code> (RM-6).
        Как только эндпоинт появится, меню подтянется из PostgreSQL автоматически.</span>
      </div>`;
  },
};

// Одна подписка на все кнопки «В корзину» на странице (делегирование событий).
document.addEventListener("click", (event) => {
  const button = event.target.closest("[data-add]");
  if (!button) return;
  const entry = UI.dishIndex.get(Number(button.dataset.add));
  if (!entry) return;
  Cart.add(entry.dish, entry.emoji);
  UI.toast(`${entry.dish.name} добавлен в корзину`, "success");
});

// ---------- Telegram-бот ----------
// Имя бота сайт узнаёт у сервера (GET /api/bot), а не хранит в коде:
// если бот переименуют или выключат, на странице не останется битой ссылки.

const TelegramBot = {
  promise: null,
  info() {
    if (!this.promise) {
      this.promise = API.get("/bot")
        .then((bot) => (bot?.enabled ? bot : null))
        .catch(() => null);
    }
    return this.promise;
  },
};

// ---------- Тема: светлая / тёмная ----------
// Выбор хранится в localStorage (удобство одного пользователя). Если выбора нет —
// тема берётся из настроек системы через CSS @media (prefers-color-scheme).
// Атрибут data-theme ставится ещё в <head> каждой страницы, чтобы не было «вспышки».

const Theme = {
  current() {
    const chosen = document.documentElement.dataset.theme;
    if (chosen === "light" || chosen === "dark") return chosen;
    return matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
  },
  apply(theme) {
    document.documentElement.dataset.theme = theme;
    try { localStorage.setItem("rm_theme", theme); } catch {}
    // Графики рисуются в SVG с цветами под тему — сообщаем, что их пора перерисовать.
    window.dispatchEvent(new CustomEvent("themechange", { detail: theme }));
  },
  toggle(button) {
    const next = this.current() === "dark" ? "light" : "dark";
    const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;

    // Новая тема «раскрывается» кругом из кнопки (View Transitions API).
    if (document.startViewTransition && !reduce) {
      const box = button.getBoundingClientRect();
      const x = box.left + box.width / 2;
      const y = box.top + box.height / 2;
      const r = Math.hypot(Math.max(x, innerWidth - x), Math.max(y, innerHeight - y));
      const root = document.documentElement.style;
      root.setProperty("--vt-x", x + "px");
      root.setProperty("--vt-y", y + "px");
      root.setProperty("--vt-r", r + "px");
      document.startViewTransition(() => this.apply(next));
      return;
    }

    // Запасной путь: плавный переход цветов.
    document.documentElement.classList.add("theme-fade");
    this.apply(next);
    setTimeout(() => document.documentElement.classList.remove("theme-fade"), 400);
  },
};

// ---------- Живые эффекты ----------

// Секции с data-reveal плавно появляются, когда доходят до экрана.
function initReveal() {
  const items = document.querySelectorAll("[data-reveal]:not(.revealed)");
  if (!("IntersectionObserver" in window)) {
    items.forEach((el) => el.classList.add("revealed"));
    return;
  }
  const observer = new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue;
      entry.target.classList.add("revealed");
      entry.target.querySelectorAll("[data-count]").forEach(countUp);
      observer.unobserve(entry.target);
    }
  }, { threshold: 0.15 });
  items.forEach((el) => observer.observe(el));
}

// Число «набегает» от 0 до значения из data-count.
function countUp(el) {
  const target = Number(el.dataset.count);
  const suffix = el.dataset.suffix || "";
  const decimals = (el.dataset.count.split(".")[1] || "").length;
  const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (reduce) {
    el.textContent = target.toFixed(decimals) + suffix;
    return;
  }
  const start = performance.now();
  const duration = 1200;
  const step = (now) => {
    const t = Math.min((now - start) / duration, 1);
    const eased = 1 - Math.pow(1 - t, 3);
    el.textContent = (target * eased).toFixed(decimals).replace(".", ",") + suffix;
    if (t < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

// Подсветка карточки следует за курсором: координаты передаём в CSS-переменные.
document.addEventListener("pointermove", (event) => {
  const card = event.target.closest?.(".dish-card, .category-tile, .feature");
  if (!card) return;
  const box = card.getBoundingClientRect();
  card.style.setProperty("--mx", event.clientX - box.left + "px");
  card.style.setProperty("--my", event.clientY - box.top + "px");
});

// Общая инициализация страницы.
function initPage(active) {
  UI.renderHeader(active);
  UI.renderFooter();
  initReveal();
}
