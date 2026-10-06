-- Соус за 67 ₸ — пасхалка: заказ ровно на 67 ₸ показывает анимацию «six seven».
-- Повторный запуск ничего не дублирует.

INSERT INTO categories (name) VALUES ('Соусы')
ON CONFLICT (name) DO NOTHING;

INSERT INTO dishes (name, description, price, cost_price, category_id, image_url, is_available)
SELECT 'Соус 67', 'Фирменный сливочно-чесночный соус. Почему 67 — узнаете, если закажете только его.',
       67, 20, c.id, 'img/dishes/sauce-67.jpg', true
FROM categories c
WHERE c.name = 'Соусы'
  AND NOT EXISTS (SELECT 1 FROM dishes WHERE name = 'Соус 67');

-- Фото для соуса, добавленного до появления этой строки.
UPDATE dishes SET image_url = 'img/dishes/sauce-67.jpg'
WHERE name = 'Соус 67' AND COALESCE(image_url, '') = '';
