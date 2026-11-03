# Образ для деплоя (Render и любой хостинг с Docker).
# Сборка в два этапа: компилятор Go нужен только чтобы собрать бинарник,
# в итоговый образ попадают сам сервер и папка frontend — около 30 МБ.

FROM golang:1.27-alpine AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.22
# ca-certificates — HTTPS к Telegram и Claude API; tzdata — часовой пояс для утренней сводки
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/server ./server
# Сервер ищет папку frontend рядом с собой (frontendDir в app/router.go)
COPY frontend ./frontend

ENV GIN_MODE=release \
    TZ=Asia/Almaty \
    PORT=8080
EXPOSE 8080
CMD ["./server"]
