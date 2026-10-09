# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source（服务端 + 三个 agent 源码；镜像只打包 server 二进制）
COPY cmd/ cmd/
COPY agents/ agents/

# Build（编译整个 cmd/server 包，而不是单个文件；CGO 关闭保证纯静态二进制）
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /server ./cmd/server

# Runtime stage
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata \
    && addgroup -S app && adduser -S -G app app

WORKDIR /app

# Copy binary + static files，并交给非 root 用户
COPY --from=builder /server .
COPY static/ ./static/
RUN mkdir -p /app/data && chown -R app:app /app

USER app

# Environment
ENV PORT=8080
# PostgreSQL 连接串由 docker-compose 通过 DATABASE_URL 注入（示例）：
# ENV DATABASE_URL=postgres://ocg:ocg@postgres:5432/ocg?sslmode=disable&TimeZone=Asia/Shanghai
ENV STATIC_DIR=/app/static

EXPOSE 8080

# Health check：/api/health 会探活数据库，DB 不可用时返回 503 使容器判定 unhealthy
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/api/health || exit 1

CMD ["./server"]
