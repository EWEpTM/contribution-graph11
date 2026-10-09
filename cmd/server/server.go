package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

var db *sql.DB
var appLocation *time.Location

// Auth 全局配置
var authUsername string
var authPassword string

// secureCookies：置 COOKIE_SECURE=true 时会话 Cookie 加 Secure 标志（仅 HTTPS 传输）
var secureCookies bool

// dummyHash：登录时用户不存在也执行一次 bcrypt 对比，抹平时序差异（防用户名枚举）
var dummyHash []byte

// 管理员用户 ID（docker 环境变量创建的账号）
var adminUserID int64

// windowLimiter 简单内存窗口限流：窗口内最多 limit 次尝试，超限锁定一个窗口周期。
// 键带最后访问时间，配合 cleanup 定期清理，防止 map 无限增长（内存 DoS）。
type windowLimiter struct {
	mu    sync.Mutex
	hits  map[string]int
	until map[string]time.Time
	seen  map[string]time.Time
	limit int
	win   time.Duration
}

// clientIP 提取请求来源 IP（RemoteAddr 的 host 部分）
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func loginRateKey(username string, r *http.Request) string {
	return username + "|" + clientIP(r)
}
func main() {
	initLocation()
	initAuth()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL not set (e.g. postgres://user:pass@host:5432/db?sslmode=disable&TimeZone=Asia/Shanghai)")
	}

	var err error
	db, err = sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatalf("Cannot connect to PostgreSQL: %v", err)
	}

	ensureSchema()

	go func() {
		for {
			time.Sleep(10 * time.Minute)
			cutoff := time.Now().Add(-30 * time.Minute)
			loginLimiter.cleanup(cutoff)
			regLimiter.cleanup(cutoff)
			sessionsMu.Lock()
			now := time.Now()
			for tk, su := range sessions {
				if now.After(su.Expires) {
					delete(sessions, tk)
				}
			}
			sessionsMu.Unlock()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/register", handleRegister)
	mux.HandleFunc("/api/me", handleMe)
	mux.HandleFunc("/api/contributions", handleContributions)
	mux.HandleFunc("/api/stats", handleGetStats)
	mux.HandleFunc("/api/users", handleUsers)
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/api/health", handleHealth)
	mux.HandleFunc("/api/sources", handleSources)
	mux.HandleFunc("/api/sources/reorder", handleSourcesReorder)

	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "./static"
	}
	mux.Handle("/", http.FileServer(http.Dir(staticDir)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	handler := corsMiddleware(authMiddleware(mux))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	fmt.Printf("🚀 Contribution Graph Server running on http://localhost:%s\n", port)
	fmt.Println("   Dashboard: /")
	fmt.Println("   Manage:    /manage.html (admin only)")
	fmt.Println("   Users:     /users.html (admin only)")
	fmt.Println("   Login:     /login.html")
	if authPassword != "" {
		if isAllowRegister() {
			fmt.Println("   Register:  POST /api/register (open)")
		} else {
			fmt.Println("   Register:  POST /api/register (CLOSED, enable in /users.html)")
		}
	}
	fmt.Println("   API:       POST /api/contributions")
	fmt.Println("   Sources:   GET/POST/DELETE /api/sources")
	fmt.Println("   Reorder:   PUT /api/sources/reorder")
	log.Fatal(srv.ListenAndServe())
}
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
