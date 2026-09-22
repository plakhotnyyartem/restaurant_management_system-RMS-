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
    if (user && user.role === "admin") {
      links.push({ href: "admin.html", key: "admin", label: "Админка" });
    }

    const header = document.getElementById("site-header");
    header.className = "site-header";
    header.innerHTML = `
      <div class="container header-inner">
        <a href="index.html" class="logo">
          <span class="logo-mark">🔥</span>
          <span>Ember<b>.</b></span>
        </a>
        <button class="burger" aria-label="Меню" aria-expanded="false">☰</button>
        <nav class="nav">
          ${links.map((l) => `<a href="${l.href}" class="${l.key === active ? "active" : ""}">${l.label}</a>`).join("")}
        </nav>
      </div>`;

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
          <a href="index.html" class="logo" style="margin-bottom:10px"><span class="logo-mark">🔥</span><span>Ember<b>.</b></span></a>
          <p>Ресторан, в котором заказ идёт от вашего клика до кухни без бумажек.</p>
        </div>
        <div>
          <h4>Контакты</h4>
          <p>📍 Алматы, пр. Абая, 10</p>
          <p>📞 +7 (700) 000-00-00</p>
          <p>✉️ hello@ember.kz</p>
        </div>
        <div>
          <h4>Часы работы</h4>
          <p>Пн–Чт: 11:00 – 23:00</p>
          <p>Пт–Вс: 11:00 – 01:00</p>
        </div>
      </div>`;
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

// Общая инициализация страницы.
function initPage(active) {
  UI.renderHeader(active);
  UI.renderFooter();
}
