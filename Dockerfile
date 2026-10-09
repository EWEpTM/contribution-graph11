# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source
COPY cmd/ cmd/

# Build
RUN CGO_ENABLED=0 GOOS=linux go build -o /server ./cmd/server/server.go

# Runtime stage
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Copy binary
COPY --from=builder /server .

# Copy static files
COPY static/ ./static/

# Create data directory
RUN mkdir -p /app/data

# Environment
ENV PORT=8080
# PostgreSQL 连接串由 docker-compose 通过 DATABASE_URL 注入（示例）：
# ENV DATABASE_URL=postgres://ocg:ocg@postgres:5432/ocg?sslmode=disable&TimeZone=Asia/Shanghai
ENV STATIC_DIR=/app/static

EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/api/health || exit 1

CMD ["./server"]
