package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

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
	Expires  time.Time
}

// 会话有效期：7 天（服务端记录过期时间，重启后失效）
const sessionTTL = 7 * 24 * time.Hour

// 多用户会话（内存态，重启后需重新登录）
var (
	sessions   = map[string]sessionUser{}
	sessionsMu sync.RWMutex
)

// 登录/注册限流（内存态）：防暴力破解与批量注册
var (
	loginLimiter = newWindowLimiter(5, 15*time.Minute)
	regLimiter   = newWindowLimiter(10, 10*time.Minute)
)

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
	secureCookies = os.Getenv("COOKIE_SECURE") == "true"
	// 预生成假哈希用于登录时序抹平（失败路径也执行一次 bcrypt）
	var err error
	dummyHash, err = bcrypt.GenerateFromPassword([]byte("keep-dummy-password"), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("Failed to generate dummy hash: %v", err)
	}
}

// hashPassword 使用 bcrypt（自带随机盐 + 自适应代价），替换原固定盐 SHA-256
func hashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// mustHash 启动期种子密码用；哈希失败属系统级异常，直接终止
func mustHash(password string) string {
	h, err := hashPassword(password)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}
	return h
}

// legacyHash 保留旧版固定盐算法，仅用于校验存量 SHA-256 哈希（渐进迁移）
func legacyHash(username, password string) string {
	h := sha256.Sum256([]byte("keep_salt_" + password + "_" + username))
	return hex.EncodeToString(h[:])
}

// isLegacyHash 判断是否为旧版 64 位 hex 哈希
func isLegacyHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

// verifyPassword 兼容新 bcrypt 与旧 SHA-256 哈希
func verifyPassword(hash, username, password string) bool {
	if isLegacyHash(hash) {
		return hash == legacyHash(username, password)
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// randomToken 使用 crypto/rand 生成 48 位十六进制会话 token；
// 连续失败返回错误而非终止进程（请求路径不得 fatal）
func randomToken() (string, error) {
	b := make([]byte, 24)
	var err error
	for i := 0; i < 3; i++ {
		if _, err = rand.Read(b); err == nil {
			return hex.EncodeToString(b), nil
		}
	}
	return "", fmt.Errorf("crypto/rand failed 3 times: %v", err)
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
	if ok && time.Now().After(su.Expires) {

		sessionsMu.Lock()
		if cur, still := sessions[cookie.Value]; still && cur.Expires.Equal(su.Expires) {
			delete(sessions, cookie.Value)
		}
		sessionsMu.Unlock()
		return sessionUser{}, false
	}
	return su, ok
}
func setAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_session",
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   secureCookies,
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
		Secure:   secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if authPassword == "" {
			r = withUser(r, sessionUser{ID: adminUserID, Username: authUsername, Role: "admin"})
			next.ServeHTTP(w, r)
			return
		}

		path := r.URL.Path

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

		if path == "/users.html" && su.Role != "admin" {
			http.Error(w, "Forbidden: admin only", http.StatusForbidden)
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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(req.Username)
	rateKey := loginRateKey(username, r)
	if !loginLimiter.allow(rateKey) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "尝试过于频繁，请稍后再试"})
		return
	}
	var id int64
	var role, hash string
	err := db.QueryRow(`SELECT id, role, password_hash FROM users WHERE username = $1`, username).Scan(&id, &role, &hash)
	if err != nil {

		bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "账号或密码错误"})
		return
	}
	if !verifyPassword(hash, username, req.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "账号或密码错误"})
		return
	}
	loginLimiter.reset(rateKey)

	if isLegacyHash(hash) {
		if newHash, err := hashPassword(req.Password); err == nil {
			if _, err := db.Exec(`UPDATE users SET password_hash = $1 WHERE id = $2`, newHash, id); err != nil {
				log.Printf("⚠️  Failed to upgrade password hash for user %s: %v", username, err)
			}
		}
	}

	token, err := randomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "系统繁忙，请稍后重试"})
		return
	}
	sessionsMu.Lock()
	sessions[token] = sessionUser{ID: id, Username: username, Role: role, Expires: time.Now().Add(sessionTTL)}
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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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
	rateKey := clientIP(r)
	if !regLimiter.allow(rateKey) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "注册过于频繁，请稍后再试"})
		return
	}

	if strings.EqualFold(username, authUsername) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该用户名不可用"})
		return
	}

	nameRunes := []rune(username)
	if len(nameRunes) < 2 || len(nameRunes) > 32 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名需 2-32 个字符"})
		return
	}
	if utf8.RuneCountInString(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "密码至少 6 位"})
		return
	}

	// 存在性检查统一按小写比较：禁止注册仅大小写不同的同名账号（防账号混淆）
	var exists string
	err := db.QueryRow(`SELECT username FROM users WHERE lower(username) = lower($1)`, username).Scan(&exists)
	if err == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名已存在"})
		return
	}

	pwdHash, err := hashPassword(req.Password)
	if err != nil {
		log.Printf("⚠️  Failed to hash password for %s: %v", username, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "注册失败，请稍后重试"})
		return
	}

	var newID int64
	err = db.QueryRow(`
        INSERT INTO users (username, password_hash, role)
        VALUES ($1, $2, 'user')
        RETURNING id
    `, username, pwdHash).Scan(&newID)
	if err != nil {

		if isUniqueViolation(err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名已存在"})
			return
		}
		log.Printf("⚠️  Failed to insert user %s: %v", username, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "注册失败，请稍后重试"})
		return
	}
	regLimiter.reset(rateKey)
	id := newID

	token, err := randomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "系统繁忙，请稍后重试"})
		return
	}
	sessionsMu.Lock()
	sessions[token] = sessionUser{ID: id, Username: username, Role: "user", Expires: time.Now().Add(sessionTTL)}
	sessionsMu.Unlock()
	setAuthCookie(w, token)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "role": "user", "username": username})
	fmt.Printf("👤 New user registered: %s\n", username)
}

// isUniqueViolation 判断 PostgreSQL 唯一约束冲突（SQLSTATE 23505）
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate key value violates unique constraint")
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
                   COUNT(e.id) AS cnt
            FROM users u
            LEFT JOIN events e ON e.user_id = u.id
            GROUP BY u.id, u.username, u.role, u.created_at
            ORDER BY CASE WHEN u.role = 'admin' THEN 0 ELSE 1 END, u.id ASC
        `)
		if err != nil {
			log.Printf("⚠️  users list query error: %v", err)
			http.Error(w, "Database error", http.StatusInternalServerError)
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
		_, errC := tx.Exec(`DELETE FROM sources_config WHERE user_id = $1`, id)
		_, errU := tx.Exec(`DELETE FROM users WHERE id = $1`, id)
		if errE != nil || errC != nil || errU != nil {
			tx.Rollback()
			log.Printf("⚠️  delete user %d failed: %v %v %v", id, errE, errC, errU)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			log.Printf("⚠️  delete user %d commit failed: %v", id, err)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		sessionsMu.Lock()
		for tk, su := range sessions {
			if su.ID == id {
				delete(sessions, tk)
			}
		}
		sessionsMu.Unlock()

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
