package main

import (
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
    "time"
    "unicode/utf8"

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

type EventSourceConfig struct {
    ID        string `json:"id"`
    Name      string `json:"name"`
    Emoji     string `json:"emoji"`
    Color     string `json:"color"`
    IsLowFreq bool   `json:"is_low_freq"`
    SortOrder int    `json:"sort_order"`
}

var db *sql.DB
var appLocation *time.Location

// Auth 全局配置
var authUsername string
var authPassword string
var expectedToken string

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
        h := sha256.Sum256([]byte("keep_salt_" + authPassword + "_" + authUsername))
        expectedToken = hex.EncodeToString(h[:])
        log.Printf("🔒 Auth enabled for user: %s", authUsername)
    } else {
        log.Printf("🔓 Auth disabled (AUTH_PASSWORD not set)")
    }
}

func main() {
    initLocation()
    initAuth()

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

    if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
        log.Printf("⚠️  Failed to set WAL mode: %v", err)
    }
    if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
        log.Printf("⚠️  Failed to set busy_timeout: %v", err)
    }

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

    CREATE TABLE IF NOT EXISTS sources_config (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        emoji TEXT NOT NULL,
        color TEXT NOT NULL,
        is_low_freq INTEGER DEFAULT 0,
        sort_order INTEGER DEFAULT 0
    );
    `

    if _, err = db.Exec(createTableSQL); err != nil {
        log.Fatalf("Failed to create table: %v", err)
    }

    // 旧库自动补字段
    db.Exec("ALTER TABLE sources_config ADD COLUMN is_low_freq INTEGER DEFAULT 0")
    db.Exec("ALTER TABLE sources_config ADD COLUMN sort_order INTEGER DEFAULT 0")

    mux := http.NewServeMux()
    mux.HandleFunc("/api/login", handleLogin)
    mux.HandleFunc("/api/contributions", handleContributions)
    mux.HandleFunc("/api/stats", handleGetStats)
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
    fmt.Println("   Manage:    /manage.html")
    fmt.Println("   Login:     /login.html")
    fmt.Println("   API:       POST /api/contributions")
    fmt.Println("   Sources:   GET/POST/DELETE /api/sources")
    fmt.Println("   Reorder:   PUT /api/sources/reorder")
    log.Fatal(http.ListenAndServe(":"+port, handler))
}

func corsMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Access-Control-Allow-Origin", "*")
        w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
        w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
        w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

        if r.Method == http.MethodOptions {
            w.WriteHeader(http.StatusOK)
            return
        }
        next.ServeHTTP(w, r)
    })
}

func authMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if authPassword == "" {
            next.ServeHTTP(w, r)
            return
        }

        path := r.URL.Path

        if path == "/login.html" || path == "/api/login" || path == "/api/health" {
            next.ServeHTTP(w, r)
            return
        }

        cookie, err := r.Cookie("auth_session")
        isValid := (err == nil && cookie.Value == expectedToken)

        if !isValid {
            if strings.HasPrefix(path, "/api/") {
                w.Header().Set("Content-Type", "application/json")
                w.WriteHeader(http.StatusUnauthorized)
                json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
                return
            }
            http.Redirect(w, r, "/login.html", http.StatusSeeOther)
            return
        }

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

    if authPassword != "" {
        if req.Username != authUsername || req.Password != authPassword {
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusUnauthorized)
            json.NewEncoder(w).Encode(map[string]string{"error": "账号或密码错误"})
            return
        }
    }

    http.SetCookie(w, &http.Cookie{
        Name:     "auth_session",
        Value:    expectedToken,
        Path:     "/",
        MaxAge:   315360000,
        HttpOnly: true,
        SameSite: http.SameSiteLaxMode,
    })

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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

func handleSources(w http.ResponseWriter, r *http.Request) {
    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }

    switch r.Method {
    case http.MethodGet:
        rows, err := db.Query(`
            SELECT id, name, emoji, color, is_low_freq, COALESCE(sort_order, 0)
            FROM sources_config
            ORDER BY sort_order ASC, name ASC
        `)
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
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(configs)

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

        // 新建时若未指定 sort_order，排到末尾
        if c.SortOrder == 0 {
            var maxOrder sql.NullInt64
            _ = db.QueryRow("SELECT MAX(sort_order) FROM sources_config").Scan(&maxOrder)
            if maxOrder.Valid {
                c.SortOrder = int(maxOrder.Int64) + 1
            }
            // 若是更新已有记录，保留原 sort_order（除非客户端显式传了）
            var existingOrder sql.NullInt64
            errExist := db.QueryRow("SELECT sort_order FROM sources_config WHERE id = ?", c.ID).Scan(&existingOrder)
            if errExist == nil && existingOrder.Valid {
                // 更新时不因默认 0 覆盖；仅当客户端传来非 0 才用新值
                // 这里用：POST body 若 sort_order 为 0 且记录已存在，则不改 sort_order
            }
        }

        var existingID string
        errExist := db.QueryRow("SELECT id FROM sources_config WHERE id = ?", c.ID).Scan(&existingID)
        if errExist == nil {
            // 更新：不强制改 sort_order（除非 body 里 sort_order > 0 或显式需要）
            _, err := db.Exec(`
                UPDATE sources_config
                SET name = ?, emoji = ?, color = ?, is_low_freq = ?
                WHERE id = ?
            `, c.Name, c.Emoji, c.Color, isLowFreqInt, c.ID)
            if err != nil {
                http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
                return
            }
        } else {
            _, err := db.Exec(`
                INSERT INTO sources_config (id, name, emoji, color, is_low_freq, sort_order)
                VALUES (?, ?, ?, ?, ?, ?)
            `, c.ID, c.Name, c.Emoji, c.Color, isLowFreqInt, c.SortOrder)
            if err != nil {
                http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
                return
            }
        }

        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        json.NewEncoder(w).Encode(map[string]string{"status": "success"})

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

        resEvents, _ := tx.Exec(`DELETE FROM events WHERE source = ?`, source)
        resConfig, _ := tx.Exec(`DELETE FROM sources_config WHERE id = ?`, source)

        if err := tx.Commit(); err != nil {
            http.Error(w, "Database error", http.StatusInternalServerError)
            return
        }

        affectedEvents, _ := resEvents.RowsAffected()
        affectedConfig, _ := resConfig.RowsAffected()

        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]interface{}{
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

    stmt, err := tx.Prepare(`UPDATE sources_config SET sort_order = ? WHERE id = ?`)
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
        if _, err := stmt.Exec(it.SortOrder, it.ID); err != nil {
            tx.Rollback()
            http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
            return
        }
    }

    if err := tx.Commit(); err != nil {
        http.Error(w, "Database error", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "status": "success",
        "count":  len(items),
    })
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

// 避免未使用导入告警（utf8 用于可能的扩展校验）
var _ = utf8.RuneCountInString