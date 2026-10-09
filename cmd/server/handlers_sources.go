package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"
)

type EventSourceConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Emoji     string `json:"emoji"`
	Color     string `json:"color"`
	IsLowFreq bool   `json:"is_low_freq"`
	SortOrder int    `json:"sort_order"`
}

func handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

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
			log.Printf("⚠️  sources query error (user=%s): %v", su.Username, err)
			http.Error(w, "Database error", http.StatusInternalServerError)
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
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var c EventSourceConfig
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		c.ID = strings.TrimSpace(c.ID)
		c.Name = strings.TrimSpace(c.Name)

		if !validSourceID(c.ID) {
			http.Error(w, "ID must be 1-64 chars of letters, digits, '_' or '-'", http.StatusBadRequest)
			return
		}

		nameRunes := []rune(c.Name)
		if len(nameRunes) == 0 {
			http.Error(w, "Name is required", http.StatusBadRequest)
			return
		}
		if len(nameRunes) > 16 {
			c.Name = string(nameRunes[:16])
		} else {
			c.Name = string(nameRunes)
		}
		if c.Emoji != "" {

			if !validSafeText(c.Emoji, 8) {
				http.Error(w, "Emoji must be 1-8 safe characters", http.StatusBadRequest)
				return
			}
		} else {
			c.Emoji = "📱"
		}

		if c.Color != "" && !validHexColor(c.Color) {
			http.Error(w, "Color must be a 6-digit hex like #238636", http.StatusBadRequest)
			return
		}
		if c.Color == "" {
			c.Color = "#8b949e"
		}

		isLowFreqInt := 0
		if c.IsLowFreq {
			isLowFreqInt = 1
		}

		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('ocg_sources_sort'))`); err != nil {
			log.Printf("⚠️  sources advisory lock failed (user=%s): %v", su.Username, err)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		var existingID string
		errExist := tx.QueryRow("SELECT id FROM sources_config WHERE id = $1 AND user_id = $2", c.ID, su.ID).Scan(&existingID)
		switch {
		case errExist == nil:

			_, err := tx.Exec(`
                UPDATE sources_config
                SET name = $1, emoji = $2, color = $3, is_low_freq = $4
                WHERE id = $5 AND user_id = $6
            `, c.Name, c.Emoji, c.Color, isLowFreqInt, c.ID, su.ID)
			if err != nil {
				log.Printf("⚠️  sources update failed (id=%s, user=%s): %v", c.ID, su.Username, err)
				http.Error(w, "Database error", http.StatusInternalServerError)
				return
			}
		case errors.Is(errExist, sql.ErrNoRows):
			// 不存在：先确认 id 未被其他用户占用（id 为全局主键，不按用户隔离）
			var ownerID int64
			errCheck := tx.QueryRow(`SELECT user_id FROM sources_config WHERE id = $1`, c.ID).Scan(&ownerID)
			switch {
			case errCheck == nil:
				http.Error(w, "This source ID is already taken by another user", http.StatusConflict)
				return
			case !errors.Is(errCheck, sql.ErrNoRows):
				log.Printf("⚠️  sources owner check failed (id=%s): %v", c.ID, errCheck)
				http.Error(w, "Database error", http.StatusInternalServerError)
				return
			}
			// 新建排到末尾：当前最大 sort_order + 1（本用户首个事件即为 0）
			var nextOrder int
			if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), -1) + 1 FROM sources_config WHERE user_id = $1", su.ID).Scan(&nextOrder); err != nil {
				log.Printf("⚠️  sources next order failed (user=%s): %v", su.Username, err)
				http.Error(w, "Database error", http.StatusInternalServerError)
				return
			}
			_, err := tx.Exec(`
                INSERT INTO sources_config (id, name, emoji, color, is_low_freq, sort_order, user_id)
                VALUES ($1, $2, $3, $4, $5, $6, $7)
            `, c.ID, c.Name, c.Emoji, c.Color, isLowFreqInt, nextOrder, su.ID)
			if err != nil {
				log.Printf("⚠️  sources insert failed (id=%s, user=%s): %v", c.ID, su.Username, err)
				http.Error(w, "Database error", http.StatusInternalServerError)
				return
			}
		default:
			log.Printf("⚠️  sources select failed (id=%s, user=%s): %v", c.ID, su.Username, errExist)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(); err != nil {
			log.Printf("⚠️  sources commit failed (user=%s): %v", su.Username, err)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
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

		resEvents, errE := tx.Exec(`DELETE FROM events WHERE source = $1 AND user_id = $2`, source, su.ID)
		resConfig, errC := tx.Exec(`DELETE FROM sources_config WHERE id = $1 AND user_id = $2`, source, su.ID)
		if errE != nil || errC != nil {
			tx.Rollback()
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

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

	su, ok := currentUser(r)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

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
			log.Printf("⚠️  sources reorder failed (user=%s): %v", su.Username, err)
			http.Error(w, "Database error", http.StatusInternalServerError)
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

// validSourceID 事件类型 ID：1-64 位，仅字母数字与 _ -（服务端强制，防注入与超长）
func validSourceID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// validSafeText 通用安全文本校验：非空、≤maxRunes 字符、不含 HTML 特殊字符与控制字符。
// 用于 source / emoji 等会被前端 innerHTML 渲染的字段，从服务端堵住存储型 XSS 入口。
func validSafeText(s string, maxRunes int) bool {
	if s == "" || utf8.RuneCountInString(s) > maxRunes {
		return false
	}
	for _, r := range s {
		switch {
		case r == '<' || r == '>' || r == '&' || r == '"' || r == '\'' || r == '`':
			return false
		case r < 0x20 || r == 0x7f:
			return false
		}
	}
	return true
}

// validHexColor 校验 #RRGGBB 格式颜色（服务端强制，堵住样式注入入口）
func validHexColor(c string) bool {
	if len(c) != 7 || c[0] != '#' {
		return false
	}
	for _, r := range c[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
