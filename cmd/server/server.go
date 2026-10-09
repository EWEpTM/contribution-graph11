package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Contribution represents the unified event structure
type Contribution struct {
	ID        int64           `json:"id,omitempty"`
	Source    string          `json:"source"`
	Context   string          `json:"context"`
	Timestamp time.Time       `json:"timestamp"`
	MetaData  json.RawMessage `json:"metadata"`
	LocalDate string          `json:"local_date,omitempty"`
}

type EventSourceConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Emoji     string `json:"emoji"`
	Color     string `json:"color"`
	IsLowFreq bool   `json:"is_low_freq"`
	SortOrder int    `json:"sort_order"`
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
	Count     int    `json:"count"`
}

type sessionUser struct {
	ID       int64
	Username string
	Role     string
}

var db *sql.DB
var appLocation *time.Location

// Auth 全局配置
var authUsername string
var authPassword string

// 多用户会话（内存态，重启后需重新登录）
var (
	sessions   = map[string]sessionUser{}
	sessionsMu sync.RWMutex
)

// 管理员用户 ID（docker 环境变量创建的账号）
var adminUserID int64

type ctxKey int

const userKey ctxKey = 0

func initLocation() {
	tz := os.Getenv("TZ")
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		log.Printf("⚠️  Invalid TZ %q, fallback to Asia/Shanghai: %v", tz, err)
		loc, err = time.LoadLocation("Asia/Shanghai")
		if err != nil {
			log.Fatalf("Failed to load timezone: %v", err)
		}
	}
	appLocation = loc
	log.Printf("🌏 Using timezone: %s", appLocation.String())
}

func initAuth() {
	authUsername = os.Getenv("AUTH_USERNAME")
	if authUsername == "" {
		authUsername = "admin"
	}
	authPassword = os.Getenv("AUTH_PASSWORD")
	if authPassword != "" {
		log.Printf("🔒 Auth enabled, admin user: %s", authUsername)
	} else {
		log.Printf("🔓 Auth disabled (AUTH_PASSWORD not set), single-user mode")
	}
}

func hashPassword(username, password string) string {
	h := sha256.Sum256([]byte("keep_salt_" + password + "_" + username))
	return hex.EncodeToString(h[:])
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		h := sha256.Sum256([]byte(fmt.Sprintf("%d-%s", time.Now().UnixNano(), authUsername)))
		return hex.EncodeToString(h[:])
	}
	return hex.EncodeToString(b)
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

	// PostgreSQL 连接池（SQLite 单文件时代无此概念）
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatalf("Cannot connect to PostgreSQL: %v", err)
	}

	ensureSchema()

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
	log.Fatal(http.ListenAndServe(":"+port, handler))
}

func ensureSchema() {
	// PostgreSQL 方言建表。
	// 时区策略：事件时间用 TIMESTAMP（无时区）存"上海墙上时间"（与旧 SQLite 的本地时间字符串语义一致），
	// 由应用层 appLocation 统一处理时区，避免 timestamptz 在 date() 查询时按连接时区偏移一天。
	createTableSQL := `
    CREATE TABLE IF NOT EXISTS events (
        id BIGSERIAL PRIMARY KEY,
        source TEXT NOT NULL,
        context TEXT,
        timestamp TIMESTAMP NOT NULL,
        metadata TEXT,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        user_id BIGINT,
        UNIQUE(source, context, timestamp, user_id)
    );
    CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
    CREATE INDEX IF NOT EXISTS idx_events_source ON events(source);
    CREATE INDEX IF NOT EXISTS idx_events_user ON events(user_id);

    CREATE TABLE IF NOT EXISTS users (
        id BIGSERIAL PRIMARY KEY,
        username TEXT NOT NULL UNIQUE,
        password_hash TEXT NOT NULL,
        role TEXT NOT NULL DEFAULT 'user',
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    );

    CREATE TABLE IF NOT EXISTS sources_config (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        emoji TEXT NOT NULL,
        color TEXT NOT NULL,
        is_low_freq INTEGER DEFAULT 0,
        sort_order INTEGER DEFAULT 0,
        user_id BIGINT
    );

    CREATE TABLE IF NOT EXISTS app_config (
        key TEXT PRIMARY KEY,
        value TEXT
    );
    `

	// PostgreSQL 全新库直接建表（IF NOT EXISTS 幂等）
	if _, err := db.Exec(createTableSQL); err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	// 幂等补字段（PG 原生支持 ADD COLUMN IF NOT EXISTS；列已存在则无操作）
	db.Exec("ALTER TABLE sources_config ADD COLUMN IF NOT EXISTS is_low_freq INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE sources_config ADD COLUMN IF NOT EXISTS sort_order INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE sources_config ADD COLUMN IF NOT EXISTS user_id BIGINT")

	// 确保管理员账号存在（docker 环境变量创建的用户默认为管理员）
	var uid int64
	var err error
	err = db.QueryRow("SELECT id FROM users WHERE username = $1", authUsername).Scan(&uid)
	if err != nil {
		seedPwd := authPassword
		if seedPwd == "" {
			seedPwd = "admin" // 单用户模式默认种子，不参与登录校验
		}
		// PostgreSQL 取插入 ID 用 RETURNING（database/sql 的 LastInsertId 不受支持）
		err = db.QueryRow(`
            INSERT INTO users (username, password_hash, role)
            VALUES ($1, $2, 'admin')
            RETURNING id
        `, authUsername, hashPassword(authUsername, seedPwd)).Scan(&uid)
		if err != nil {
			log.Fatalf("Failed to seed admin user: %v", err)
		}
		log.Printf("👑 Seeded admin user: %s", authUsername)
	}
	adminUserID = uid

	// 迁移：历史打卡数据归属管理员
	if _, err := db.Exec("UPDATE events SET user_id = $1 WHERE user_id IS NULL", uid); err != nil {
		log.Printf("⚠️  Failed to backfill user_id: %v", err)
	}
	// 迁移：历史事件类型归属管理员（事件类型按用户隔离，普通用户可自建自己的事件）
	if _, err := db.Exec("UPDATE sources_config SET user_id = $1 WHERE user_id IS NULL", uid); err != nil {
		log.Printf("⚠️  Failed to backfill sources_config.user_id: %v", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		// API 响应禁止缓存；静态资源允许缓存（manifest / sw.js 走 revalidate），否则 PWA 无法利用 HTTP 缓存
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		} else if p == "/" || strings.HasSuffix(p, ".html") || p == "/manifest.json" || p == "/sw.js" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=604800")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PWA 必备资源必须免登录公开，否则浏览器拿不到 manifest、
// 注册不了 Service Worker、校验不了图标，导致无法安装为 PWA
func isPublicPwaAsset(path string) bool {
	switch path {
	case "/manifest.json",
		"/sw.js",
		"/icon-192.png",
		"/icon-512.png",
		"/apple-touch-icon.png",
		"/favicon-16x16.png",
		"/favicon-32x32.png":
		return true
	}
	return false
}

func withUser(r *http.Request, su sessionUser) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userKey, su))
}

func currentUser(r *http.Request) (sessionUser, bool) {
	su, ok := r.Context().Value(userKey).(sessionUser)
	return su, ok
}

func currentSession(r *http.Request) (sessionUser, bool) {
	cookie, err := r.Cookie("auth_session")
	if err != nil {
		return sessionUser{}, false
	}
	sessionsMu.RLock()
	su, ok := sessions[cookie.Value]
	sessionsMu.RUnlock()
	return su, ok
}

func setAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_session",
		Value:    token,
		Path:     "/",
		MaxAge:   315360000,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 单用户模式（未设置 AUTH_PASSWORD）：无需登录，一切视为管理员
		if authPassword == "" {
			r = withUser(r, sessionUser{ID: adminUserID, Username: authUsername, Role: "admin"})
			next.ServeHTTP(w, r)
			return
		}

		path := r.URL.Path

		// GET /api/config 公开（登录页需要提前知道注册是否开放）；PUT 需登录，由 handler 校验管理员
		if path == "/api/config" && r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}

		if path == "/login.html" || path == "/api/login" || path == "/api/register" || path == "/api/health" || isPublicPwaAsset(path) {
			next.ServeHTTP(w, r)
			return
		}

		su, ok := currentSession(r)
		if !ok {
			if strings.HasPrefix(path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			http.Redirect(w, r, "/login.html", http.StatusSeeOther)
			return
		}

		// 用户管理页仅管理员可见；事件类型配置页所有登录用户可用（各自管理自己的事件）
		if path == "/users.html" && su.Role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}

		r = withUser(r, su)
		next.ServeHTTP(w, r)
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(req.Username)
	var id int64
	var role, hash string
	err := db.QueryRow(`SELECT id, role, password_hash FROM users WHERE username = $1`, username).Scan(&id, &role, &hash)
	if err != nil || hashPassword(username, req.Password) != hash {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "账号或密码错误"})
		return
	}

	token := randomToken()
	sessionsMu.Lock()
	sessions[token] = sessionUser{ID: id, Username: username, Role: role}
	sessionsMu.Unlock()

	setAuthCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "role": role, "username": username})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie("auth_session"); err == nil {
		sessionsMu.Lock()
		delete(sessions, cookie.Value)
		sessionsMu.Unlock()
	}
	clearAuthCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// 注册新用户（普通用户角色）
func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if authPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未启用账号注册"})
		return
	}
	if !isAllowRegister() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "注册未开放"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(req.Username)
	nameRunes := []rune(username)
	if len(nameRunes) < 2 || len(nameRunes) > 32 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名需 2-32 个字符"})
		return
	}
	if utf8.RuneCountInString(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "密码至少 6 位"})
		return
	}

	var exists string
	err := db.QueryRow(`SELECT username FROM users WHERE username = $1`, username).Scan(&exists)
	if err == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名已存在"})
		return
	}

	var newID int64
	err = db.QueryRow(`
        INSERT INTO users (username, password_hash, role)
        VALUES ($1, $2, 'user')
        RETURNING id
    `, username, hashPassword(username, req.Password)).Scan(&newID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "注册失败，请稍后重试"})
		return
	}
	id := newID

	// 注册后自动登录
	token := randomToken()
	sessionsMu.Lock()
	sessions[token] = sessionUser{ID: id, Username: username, Role: "user"}
	sessionsMu.Unlock()
	setAuthCookie(w, token)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "role": "user", "username": username})
	fmt.Printf("👤 New user registered: %s\n", username)
}

// GET /api/me  当前登录用户信息
func handleMe(w http.ResponseWriter, r *http.Request) {
	su, ok := currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": su.Username, "role": su.Role})
}

// 用户管理：仅管理员。GET 列表 / DELETE?id=N 删除普通用户及其全部数据
func handleUsers(w http.ResponseWriter, r *http.Request) {
	su, ok := currentUser(r)
	if !ok || su.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`
            SELECT u.id, u.username, u.role,
                   to_char(u.created_at, 'YYYY-MM-DD HH24:MI:SS') AS created_at,
                   (SELECT COUNT(*) FROM events e WHERE e.user_id = u.id) AS cnt
            FROM users u
            ORDER BY u.role DESC, u.id ASC
        `)
		if err != nil {
			http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var users []User
		for rows.Next() {
			var u User
			var createdAt string
			if err := rows.Scan(&u.ID, &u.Username, &u.Role, &createdAt, &u.Count); err == nil {
				u.CreatedAt = createdAt
				users = append(users, u)
			}
		}
		if users == nil {
			users = []User{}
		}
		writeJSON(w, http.StatusOK, users)

	case http.MethodDelete:
		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			http.Error(w, "missing id parameter", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid id parameter", http.StatusBadRequest)
			return
		}
		if id == su.ID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不能删除当前登录的账号"})
			return
		}

		var role string
		err = db.QueryRow(`SELECT role FROM users WHERE id = $1`, id).Scan(&role)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "用户不存在"})
			return
		}
		if role != "user" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "只能删除普通用户"})
			return
		}

		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		resE, errE := tx.Exec(`DELETE FROM events WHERE user_id = $1`, id)
		_, errU := tx.Exec(`DELETE FROM users WHERE id = $1`, id)
		if errE != nil || errU != nil {
			tx.Rollback()
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		n, _ := resE.RowsAffected()
		fmt.Printf("🗑️  Deleted user id=%d and %d contributions\n", id, n)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":         "success",
			"deleted_events": n,
			"deleted_user":   1,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleContributions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleGetContributions(w, r)
	case http.MethodPost:
		handlePostContribution(w, r)
	case http.MethodDelete:
		handleDeleteContribution(w, r)
	case http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handlePostContribution(w http.ResponseWriter, r *http.Request) {
	su, _ := currentUser(r)

	var contributions []Contribution
	if err := json.NewDecoder(r.Body).Decode(&contributions); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	stmt, err := tx.Prepare(`
        INSERT INTO events (source, context, timestamp, metadata, user_id)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT DO NOTHING
    `)
	if err != nil {
		tx.Rollback()
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer stmt.Close()

	count := 0
	for _, c := range contributions {
		source := strings.TrimSpace(c.Source)
		if source == "" {
			continue // 丢弃无 source 的脏记录
		}
		ts := c.Timestamp
		if ts.IsZero() {
			ts = time.Now() // 客户端未带时间则取服务器当前时间，避免写入 year 1 的脏数据
		}
		localStr := ts.In(appLocation).Format("2006-01-02 15:04:05")

		metaString := string(c.MetaData)
		if metaString == "" {
			metaString = "{}"
		}

		if _, err := stmt.Exec(source, c.Context, localStr, metaString, su.ID); err == nil {
			count++
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"processed": count,
		"message":   fmt.Sprintf("Processed %d contributions", count),
	})
	fmt.Printf("📥 Received %d events (from %d submitted) by user %s\n", count, len(contributions), su.Username)
}

// DELETE /api/contributions?id=<event_id>  删除单条打卡记录（仅限本人）
func handleDeleteContribution(w http.ResponseWriter, r *http.Request) {
	su, _ := currentUser(r)

	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id parameter", http.StatusBadRequest)
		return
	}

	res, err := db.Exec(`DELETE FROM events WHERE id = $1 AND user_id = $2`, id, su.ID)
	if err != nil {
		http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"status": "not_found", "deleted": 0})
		return
	}
	fmt.Printf("🗑️  Deleted event id=%d (%d row) by user %s\n", id, n, su.Username)
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "success", "deleted": n})
}

func handleGetContributions(w http.ResponseWriter, r *http.Request) {
	su, _ := currentUser(r)
	year := r.URL.Query().Get("year")
	source := r.URL.Query().Get("source")

	if year == "" {
		year = fmt.Sprintf("%d", time.Now().In(appLocation).Year())
	}

	if _, err := strconv.Atoi(year); err != nil {
		http.Error(w, "Invalid year parameter", http.StatusBadRequest)
		return
	}

	// 上方已校验 year 为合法整数
	y, _ := strconv.Atoi(year)
	startDate := fmt.Sprintf("%d-01-01", y)
	endDate := fmt.Sprintf("%d-01-01", y+1)

	query := `
        SELECT id, source, context, timestamp, metadata
        FROM events
        WHERE timestamp >= $1 AND timestamp < $2 AND user_id = $3
    `
	args := []interface{}{startDate, endDate, su.ID}

	if source != "" {
		query += " AND source = $4"
		args = append(args, source)
	}
	query += " ORDER BY timestamp DESC"

	rows, err := db.Query(query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var events []Contribution
	for rows.Next() {
		var c Contribution
		var metaString string
		var ts time.Time

		if err := rows.Scan(&c.ID, &c.Source, &c.Context, &ts, &metaString); err != nil {
			continue
		}

		c.Timestamp = time.Date(
			ts.Year(), ts.Month(), ts.Day(),
			ts.Hour(), ts.Minute(), ts.Second(), ts.Nanosecond(),
			appLocation,
		)
		c.LocalDate = c.Timestamp.Format("2006-01-02")
		c.MetaData = json.RawMessage(metaString)
		events = append(events, c)
	}

	if events == nil {
		events = []Contribution{}
	}

	writeJSON(w, http.StatusOK, events)
}

func handleGetStats(w http.ResponseWriter, r *http.Request) {
	su, _ := currentUser(r)
	stats := make(map[string]interface{})

	// 一次 GROUP BY 同时得到各 source 计数与总计数（total = 各 source 之和），
	// 省掉一次独立的全表 COUNT。
	sources := make(map[string]int)
	total := 0
	rows, err := db.Query(`
        SELECT source, COUNT(*) AS count
        FROM events
        WHERE user_id = $1
        GROUP BY source
    `, su.ID)
	if err == nil {
		for rows.Next() {
			var source string
			var count int
			if rows.Scan(&source, &count) == nil {
				sources[source] = count
				total += count
			}
		}
		rows.Close()
	}
	stats["total"] = total
	stats["by_source"] = sources

	stats["current_streak"] = calculateStreak(su.ID)

	// 今日计数：用 [今日00:00, 次日00:00) 范围比较，可命中 idx_events_timestamp；
	// 旧实现 to_char(timestamp,'YYYY-MM-DD')=? 使索引失效导致全表扫描。
	// timestamp 为无时区列、存本地墙上时间，边界同样按墙上时间格式化。
	now := time.Now().In(appLocation)
	todayStr := now.Format("2006-01-02")
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, appLocation)
	dayStart := midnight.Format("2006-01-02 15:04:05")
	dayEnd := midnight.Add(24 * time.Hour).Format("2006-01-02 15:04:05")
	var today int
	db.QueryRow(`
        SELECT COUNT(*) FROM events
        WHERE user_id = $1 AND timestamp >= $2 AND timestamp < $3
    `, su.ID, dayStart, dayEnd).Scan(&today)
	stats["today"] = today
	stats["timezone"] = appLocation.String()
	stats["today_date"] = todayStr

	writeJSON(w, http.StatusOK, stats)
}

func handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 事件类型按用户隔离：每个登录用户管理自己的事件（管理员与普通用户平等）
	su, _ := currentUser(r)

	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`
            SELECT id, name, emoji, color, is_low_freq, COALESCE(sort_order, 0)
            FROM sources_config
            WHERE user_id = $1
            ORDER BY sort_order ASC, name ASC
        `, su.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var configs []EventSourceConfig
		for rows.Next() {
			var c EventSourceConfig
			var isLowFreqInt int
			if err := rows.Scan(&c.ID, &c.Name, &c.Emoji, &c.Color, &isLowFreqInt, &c.SortOrder); err == nil {
				c.IsLowFreq = isLowFreqInt == 1
				configs = append(configs, c)
			}
		}
		if configs == nil {
			configs = []EventSourceConfig{}
		}
		writeJSON(w, http.StatusOK, configs)

	case http.MethodPost:
		var c EventSourceConfig
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if c.ID == "" || c.Name == "" {
			http.Error(w, "ID and Name are required", http.StatusBadRequest)
			return
		}
		// 名称最多 16 个字符（按 rune 计）
		nameRunes := []rune(strings.TrimSpace(c.Name))
		if len(nameRunes) == 0 {
			http.Error(w, "Name is required", http.StatusBadRequest)
			return
		}
		if len(nameRunes) > 16 {
			c.Name = string(nameRunes[:16])
		} else {
			c.Name = string(nameRunes)
		}
		if c.Emoji == "" {
			c.Emoji = "📱"
		}
		if c.Color == "" {
			c.Color = "#8b949e"
		}

		isLowFreqInt := 0
		if c.IsLowFreq {
			isLowFreqInt = 1
		}

		// 已存在则更新（保留原 sort_order，不被客户端默认的 0 覆盖）；
		// 不存在则新建并排到本用户末尾。
		var existingID string
		errExist := db.QueryRow("SELECT id FROM sources_config WHERE id = $1 AND user_id = $2", c.ID, su.ID).Scan(&existingID)
		if errExist == nil {
			// 更新（仅限本人）
			_, err := db.Exec(`
                UPDATE sources_config
                SET name = $1, emoji = $2, color = $3, is_low_freq = $4
                WHERE id = $5 AND user_id = $6
            `, c.Name, c.Emoji, c.Color, isLowFreqInt, c.ID, su.ID)
			if err != nil {
				http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			// 新建排到末尾：当前最大 sort_order + 1（本用户首个事件即为 0）
			var nextOrder int
			_ = db.QueryRow("SELECT COALESCE(MAX(sort_order), -1) + 1 FROM sources_config WHERE user_id = $1", su.ID).Scan(&nextOrder)
			_, err := db.Exec(`
                INSERT INTO sources_config (id, name, emoji, color, is_low_freq, sort_order, user_id)
                VALUES ($1, $2, $3, $4, $5, $6, $7)
            `, c.ID, c.Name, c.Emoji, c.Color, isLowFreqInt, nextOrder, su.ID)
			if err != nil {
				http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "success"})

	case http.MethodDelete:
		source := r.URL.Query().Get("source")
		if source == "" {
			http.Error(w, "missing source parameter", http.StatusBadRequest)
			return
		}

		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		resEvents, _ := tx.Exec(`DELETE FROM events WHERE source = $1 AND user_id = $2`, source, su.ID)
		resConfig, _ := tx.Exec(`DELETE FROM sources_config WHERE id = $1 AND user_id = $2`, source, su.ID)

		if err := tx.Commit(); err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		affectedEvents, _ := resEvents.RowsAffected()
		affectedConfig, _ := resConfig.RowsAffected()

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"deleted_events": affectedEvents,
			"deleted_config": affectedConfig,
			"source":         source,
		})
		fmt.Printf("🗑️  Deleted source %q: %d events removed\n", source, affectedEvents)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT /api/sources/reorder  body: [{"id":"xxx","sort_order":0}, ...]
func handleSourcesReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 事件类型按用户隔离：每个登录用户都能重排自己的事件（UPDATE 语句已带 user_id 过滤，
	// 无法越权改动他人）。旧实现误要求 admin，导致普通用户在配置页拖拽排序必然 403。
	su, ok := currentUser(r)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	var items []struct {
		ID        string `json:"id"`
		SortOrder int    `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	stmt, err := tx.Prepare(`UPDATE sources_config SET sort_order = $1 WHERE id = $2 AND user_id = $3`)
	if err != nil {
		tx.Rollback()
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer stmt.Close()

	for _, it := range items {
		if it.ID == "" {
			continue
		}
		if _, err := stmt.Exec(it.SortOrder, it.ID, su.ID); err != nil {
			tx.Rollback()
			http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "success",
		"count":  len(items),
	})
}

func calculateStreak(userID int64) int {
	now := time.Now().In(appLocation)
	// 只回溯约 400 天并走 idx_events_timestamp 范围扫描，避免对全表做 DISTINCT+to_char。
	lookback := now.AddDate(0, 0, -400).Format("2006-01-02 15:04:05")
	rows, err := db.Query(`
        SELECT DISTINCT timestamp::date AS d
        FROM events
        WHERE user_id = $1 AND timestamp >= $2
        ORDER BY d DESC
    `, userID, lookback)
	if err != nil {
		return 0
	}
	var days []time.Time
	for rows.Next() {
		var d time.Time
		if rows.Scan(&d) == nil {
			days = append(days, d)
		}
	}
	rows.Close()

	// 期望连续到"今天"；若今天尚无记录，则允许从"昨天"起算（今天空白不断签）。
	expected := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, appLocation)
	if len(days) > 0 {
		first := midnightOf(days[0])
		if first.Equal(expected.AddDate(0, 0, -1)) {
			expected = first
		}
	}

	streak := 0
	for _, d := range days {
		day := midnightOf(d)
		switch {
		case day.Equal(expected):
			streak++
			expected = expected.AddDate(0, 0, -1)
		case day.Before(expected):
			return streak // 出现缺口，连续中断
		}
		// day.After(expected) 为未来日期，理论上不存在，忽略
	}
	return streak
}

// midnightOf 把任意时间归一化为 appLocation 时区的当日 0 点（仅比较日期用）。
func midnightOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, appLocation)
}

// 站点配置：GET 公开读取（登录页需要提前知道注册是否开放）；PUT 仅管理员修改
func handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"allow_register": isAllowRegister(),
		})

	case http.MethodPut:
		su, ok := currentUser(r)
		if !ok || su.Role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		var req struct {
			AllowRegister *bool `json:"allow_register"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if req.AllowRegister != nil {
			if err := setAllowRegister(*req.AllowRegister); err != nil {
				http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":         "success",
			"allow_register": isAllowRegister(),
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// 注册开关：默认关闭（未设置记录 = 未开放），管理员在用户管理页开启后用户才能注册
func isAllowRegister() bool {
	var v string
	err := db.QueryRow(`SELECT value FROM app_config WHERE key = 'allow_register'`).Scan(&v)
	if err != nil {
		return false
	}
	return v == "1"
}

func setAllowRegister(v bool) error {
	val := "0"
	if v {
		val = "1"
	}
	_, err := db.Exec(`
        INSERT INTO app_config (key, value) VALUES ('allow_register', $1)
        ON CONFLICT(key) DO UPDATE SET value = excluded.value
    `, val)
	return err
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "ok",
		"timezone": appLocation.String(),
		"now":      time.Now().In(appLocation).Format(time.RFC3339),
	})
}
