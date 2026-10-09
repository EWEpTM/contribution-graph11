package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	var contributions []Contribution
	if err := json.NewDecoder(r.Body).Decode(&contributions); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(contributions) > 10000 {
		http.Error(w, "Too many contributions in one request (max 10000)", http.StatusBadRequest)
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
	realErrs := 0
	for _, c := range contributions {
		source := strings.TrimSpace(c.Source)

		if !validSafeText(source, 64) {
			continue
		}
		ts := c.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		localStr := ts.In(appLocation).Format("2006-01-02 15:04:05")

		ctx := c.Context
		if utf8.RuneCountInString(ctx) > 500 {
			ctx = string([]rune(ctx)[:500])
		}
		metaString := string(c.MetaData)
		if metaString == "" {
			metaString = "{}"
		}
		if len(metaString) > 8192 {
			metaString = metaString[:8192]
		}

		if _, err := stmt.Exec(source, ctx, localStr, metaString, su.ID); err != nil {

			if !isUniqueViolation(err) {
				log.Printf("⚠️  insert event failed (source=%q, user=%s): %v", source, su.Username, err)
				realErrs++
			}
			continue
		}
		count++
	}

	if err := tx.Commit(); err != nil {
		log.Printf("⚠️  contributions commit failed: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"processed": count,
		"errors":    realErrs,
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
		log.Printf("⚠️  delete event %d failed: %v", id, err)
		http.Error(w, "Database error", http.StatusInternalServerError)
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

	fields := r.URL.Query().Get("fields")
	wantMeta := fields == "" || strings.Contains(","+fields+",", ",metadata,")

	if year == "" {
		year = fmt.Sprintf("%d", time.Now().In(appLocation).Year())
	}

	y, err := strconv.Atoi(year)
	if err != nil || y < 1900 || y > 9999 {
		http.Error(w, "Invalid year parameter", http.StatusBadRequest)
		return
	}

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
		log.Printf("⚠️  contributions query error (user=%s): %v", su.Username, err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var events []Contribution
	for rows.Next() {
		var c Contribution
		var metaString string
		var ts time.Time

		if err := rows.Scan(&c.ID, &c.Source, &c.Context, &ts, &metaString); err != nil {
			log.Printf("⚠️  contributions scan error: %v", err)
			continue
		}

		c.Timestamp = time.Date(
			ts.Year(), ts.Month(), ts.Day(),
			ts.Hour(), ts.Minute(), ts.Second(), ts.Nanosecond(),
			appLocation,
		)
		c.LocalDate = c.Timestamp.Format("2006-01-02")
		if wantMeta {
			c.MetaData = json.RawMessage(metaString)
		}
		events = append(events, c)
	}
	if rows.Err() != nil {
		log.Printf("⚠️  contributions rows error: %v", rows.Err())
	}

	if events == nil {
		events = []Contribution{}
	}

	writeJSON(w, http.StatusOK, events)
}
func handleGetStats(w http.ResponseWriter, r *http.Request) {
	su, _ := currentUser(r)
	stats := make(map[string]interface{})

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
		if rows.Err() != nil {
			log.Printf("⚠️  stats by_source scan error: %v", rows.Err())
		}
		rows.Close()
	} else {
		log.Printf("⚠️  stats by_source query error: %v", err)
	}
	stats["total"] = total
	stats["by_source"] = sources

	stats["current_streak"] = calculateStreak(su.ID)

	now := time.Now().In(appLocation)
	todayStr := now.Format("2006-01-02")
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, appLocation)
	dayStart := midnight.Format("2006-01-02 15:04:05")
	dayEnd := midnight.Add(24 * time.Hour).Format("2006-01-02 15:04:05")
	var today int
	if err := db.QueryRow(`
        SELECT COUNT(*) FROM events
        WHERE user_id = $1 AND timestamp >= $2 AND timestamp < $3
    `, su.ID, dayStart, dayEnd).Scan(&today); err != nil {
		log.Printf("⚠️  stats today query error: %v", err)
	}
	stats["today"] = today
	stats["timezone"] = appLocation.String()
	stats["today_date"] = todayStr

	writeJSON(w, http.StatusOK, stats)
}
func calculateStreak(userID int64) int {
	now := time.Now().In(appLocation)

	rows, err := db.Query(`
        SELECT DISTINCT timestamp::date AS d
        FROM events
        WHERE user_id = $1 AND timestamp >= $2
        ORDER BY d DESC
    `, userID, now.AddDate(-10, 0, 0).Format("2006-01-02 15:04:05"))
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
	if rows.Err() != nil {
		rows.Close()
		return 0
	}
	rows.Close()

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
			return streak
		}

	}
	return streak
}

// midnightOf 把任意时间归一化为 appLocation 时区的当日 0 点（仅比较日期用）。
func midnightOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, appLocation)
}
