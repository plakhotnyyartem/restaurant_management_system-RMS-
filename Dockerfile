# Сборка сервера RMS для хостинга (Render и любой другой с Docker).
# Секреты в образ не попадают: .env исключён в .dockerignore,
# DATABASE_URL, JWT_SECRET и TELEGRAM_BOT_TOKEN задаются в панели хостинга.

FROM golang:1.27-alpine AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.22
# ca-certificates — HTTPS к Telegram и Claude API; tzdata — время Алматы для сводки в 9:00
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/server ./server
COPY frontend ./frontend
ENV GIN_MODE=release TZ=Asia/Almaty PORT=8080
EXPOSE 8080
CMD ["./server"]
