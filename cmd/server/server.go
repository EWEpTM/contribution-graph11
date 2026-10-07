package main

import (
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "os"
    "strconv"
    "time"

    _ "modernc.org/sqlite"
)

// Contribution represents the unified event structure
type Contribution struct {
    Source    string          `json:"source"`
    Context   string          `json:"context"`
    Timestamp time.Time       `json:"timestamp"`
    MetaData  json.RawMessage `json:"metadata"`
    LocalDate string          `json:"local_date,omitempty"`
}

var db *sql.DB
var appLocation *time.Location

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

func main() {
    initLocation()

    dbPath := os.Getenv("DB_PATH")
    if dbPath == "" {
        dbPath = "./data/contributions.db"
    }

    if err := os.MkdirAll("./data", 0755); err != nil {
        log.Fatalf("Failed to create data directory: %v", err)
    }

    var err error
    db, err = sql.Open("sqlite", dbPath)
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    createTableSQL := `
    CREATE TABLE IF NOT EXISTS events (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        source TEXT NOT NULL,
        context TEXT,
        timestamp DATETIME NOT NULL,
        metadata TEXT,
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        UNIQUE(source, context, timestamp)
    );
    CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
    CREATE INDEX IF NOT EXISTS idx_events_source ON events(source);
    `

    if _, err = db.Exec(createTableSQL); err != nil {
        log.Fatalf("Failed to create table: %v", err)
    }

    mux := http.NewServeMux()
    mux.HandleFunc("/api/contributions", handleContributions)
    mux.HandleFunc("/api/stats", handleGetStats)
    mux.HandleFunc("/api/health", handleHealth)
    mux.HandleFunc("/api/sources", handleSources)

    staticDir := os.Getenv("STATIC_DIR")
    if staticDir == "" {
        staticDir = "./static"
    }
    mux.Handle("/", http.FileServer(http.Dir(staticDir)))

    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }

    handler := corsMiddleware(mux)

    fmt.Printf("🚀 Contribution Graph Server running on http://localhost:%s\n", port)
    fmt.Println("   Dashboard: /")
    fmt.Println("   Mobile:    /mobile.html")
    fmt.Println("   API:       POST /api/contributions")
    fmt.Println("   Delete:    DELETE /api/sources?source=<id>")
    log.Fatal(http.ListenAndServe(":"+port, handler))
}

func corsMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Access-Control-Allow-Origin", "*")
        w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
        w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
        w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

        if r.Method == http.MethodOptions {
            w.WriteHeader(http.StatusOK)
            return
        }
        next.ServeHTTP(w, r)
    })
}

func handleContributions(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        handleGetContributions(w, r)
    case http.MethodPost:
        handlePostContribution(w, r)
    case http.MethodOptions:
        w.WriteHeader(http.StatusOK)
    default:
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
    }
}

// POST: 写入时强制转换为应用时区（Docker TZ）的墙钟时间
func handlePostContribution(w http.ResponseWriter, r *http.Request) {
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
        INSERT OR IGNORE INTO events (source, context, timestamp, metadata)
        VALUES (?, ?, ?, ?)
    `)
    if err != nil {
        tx.Rollback()
        http.Error(w, "Database error", http.StatusInternalServerError)
        return
    }
    defer stmt.Close()

    count := 0
    for _, c := range contributions {
        metaString := string(c.MetaData)
        if metaString == "" {
            metaString = "{}"
        }

        // 前端传来的一般是 UTC；转到 Docker/应用时区后存本地墙钟
        localTs := c.Timestamp.In(appLocation)
        localStr := localTs.Format("2006-01-02 15:04:05")

        if _, err := stmt.Exec(c.Source, c.Context, localStr, metaString); err == nil {
            count++
        }
    }

    if err := tx.Commit(); err != nil {
        http.Error(w, "Database error", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusCreated)
    json.NewEncoder(w).Encode(map[string]interface{}{
        "processed": count,
        "message":   fmt.Sprintf("Processed %d contributions", count),
    })
    fmt.Printf("📥 Received %d events (from %d submitted)\n", count, len(contributions))
}

// GET: 读出后按应用时区解释，JSON 带 +08:00，前端 slice 日期即本地日
func handleGetContributions(w http.ResponseWriter, r *http.Request) {
    year := r.URL.Query().Get("year")
    source := r.URL.Query().Get("source")

    if year == "" {
        year = fmt.Sprintf("%d", time.Now().In(appLocation).Year())
    }

    if _, err := strconv.Atoi(year); err != nil {
        http.Error(w, "Invalid year parameter", http.StatusBadRequest)
        return
    }

    startDate := year + "-01-01"
    endDate := fmt.Sprintf("%d-01-01", mustAtoi(year)+1)

    query := `
        SELECT source, context, timestamp, metadata
        FROM events
        WHERE timestamp >= ? AND timestamp < ?
    `
    args := []interface{}{startDate, endDate}

    if source != "" {
        query += " AND source = ?"
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

        if err := rows.Scan(&c.Source, &c.Context, &ts, &metaString); err != nil {
            continue
        }

        // SQLite 读出的 naive 时间按应用时区解释（不要当成 UTC）
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

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(events)
}

func handleGetStats(w http.ResponseWriter, r *http.Request) {
    stats := make(map[string]interface{})

    var total int
    db.QueryRow("SELECT COUNT(*) FROM events").Scan(&total)
    stats["total"] = total

    rows, err := db.Query(`
        SELECT source, COUNT(*) as count
        FROM events
        GROUP BY source
        ORDER BY count DESC
    `)
    if err == nil {
        defer rows.Close()
        sources := make(map[string]int)
        for rows.Next() {
            var source string
            var count int
            rows.Scan(&source, &count)
            sources[source] = count
        }
        stats["by_source"] = sources
    }

    stats["current_streak"] = calculateStreak()

    // 今日：按应用时区的日历日，不用 UTC 的 date('now')
    todayStr := time.Now().In(appLocation).Format("2006-01-02")
    var today int
    db.QueryRow(`
        SELECT COUNT(*) FROM events
        WHERE date(timestamp) = ?
    `, todayStr).Scan(&today)
    stats["today"] = today
    stats["timezone"] = appLocation.String()
    stats["today_date"] = todayStr

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(stats)
}

// DELETE /api/sources?source=xxx
func handleSources(w http.ResponseWriter, r *http.Request) {
    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }
    if r.Method != http.MethodDelete {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    source := r.URL.Query().Get("source")
    if source == "" {
        http.Error(w, "missing source parameter", http.StatusBadRequest)
        return
    }

    result, err := db.Exec(`DELETE FROM events WHERE source = ?`, source)
    if err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }

    affected, _ := result.RowsAffected()
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "deleted": affected,
        "source":  source,
        "message": fmt.Sprintf("Deleted %d events for source %q", affected, source),
    })
    fmt.Printf("🗑️  Deleted %d events for source %q\n", affected, source)
}

func calculateStreak() int {
    rows, err := db.Query(`
        SELECT DISTINCT date(timestamp) as day
        FROM events
        ORDER BY day DESC
        LIMIT 365
    `)
    if err != nil {
        return 0
    }
    defer rows.Close()

    streak := 0
    nowLocal := time.Now().In(appLocation)
    expectedStr := nowLocal.Format("2006-01-02")
    expectedDate, _ := time.ParseInLocation("2006-01-02", expectedStr, appLocation)

    for rows.Next() {
        var dayStr string
        rows.Scan(&dayStr)
        day, err := time.ParseInLocation("2006-01-02", dayStr, appLocation)
        if err != nil {
            continue
        }

        if day.Equal(expectedDate) || day.Equal(expectedDate.AddDate(0, 0, -1)) {
            if day.Equal(expectedDate.AddDate(0, 0, -1)) && streak == 0 {
                // 今天还没打卡，从昨天开始算连续
                expectedDate = day
            }
            streak++
            expectedDate = day.AddDate(0, 0, -1)
        } else if day.Before(expectedDate) {
            break
        }
    }
    return streak
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "status":   "ok",
        "timezone": appLocation.String(),
        "now":      time.Now().In(appLocation).Format(time.RFC3339),
    })
}

func mustAtoi(s string) int {
    n, err := strconv.Atoi(s)
    if err != nil {
        panic(err)
    }
    return n
}