package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

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
				log.Printf("⚠️  set allow_register failed: %v", err)
				http.Error(w, "Database error", http.StatusInternalServerError)
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

// handleHealth 健康检查：同时探活数据库连接，DB 不可用返回 503（供容器 HEALTHCHECK 判定）
func handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	code := http.StatusOK
	if err := db.Ping(); err != nil {
		log.Printf("⚠️  health check db ping failed: %v", err)
		status = "error"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]interface{}{
		"status":   status,
		"timezone": appLocation.String(),
		"now":      time.Now().In(appLocation).Format(time.RFC3339),
	})
}
