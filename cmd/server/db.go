package main

import (
	_ "github.com/jackc/pgx/v5/stdlib"
	"log"
	"os"
	"time"
)

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
func ensureSchema() {

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

	if _, err := db.Exec(createTableSQL); err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	for _, stmt := range []string{
		"ALTER TABLE sources_config ADD COLUMN IF NOT EXISTS is_low_freq INTEGER DEFAULT 0",
		"ALTER TABLE sources_config ADD COLUMN IF NOT EXISTS sort_order INTEGER DEFAULT 0",
		"ALTER TABLE sources_config ADD COLUMN IF NOT EXISTS user_id BIGINT",
	} {
		if _, err := db.Exec(stmt); err != nil {
			log.Printf("⚠️  Failed to apply schema migration: %v", err)
		}
	}

	// 确保管理员账号存在（docker 环境变量创建的用户默认为管理员）
	var uid int64
	var err error
	err = db.QueryRow("SELECT id FROM users WHERE username = $1", authUsername).Scan(&uid)
	if err != nil {
		seedPwd := authPassword
		if seedPwd == "" {
			seedPwd = "admin"
		}

		err = db.QueryRow(`
            INSERT INTO users (username, password_hash, role)
            VALUES ($1, $2, 'admin')
            RETURNING id
        `, authUsername, mustHash(seedPwd)).Scan(&uid)
		if err != nil {
			log.Fatalf("Failed to seed admin user: %v", err)
		}
		log.Printf("👑 Seeded admin user: %s", authUsername)
	} else if authPassword != "" {
		// 多用户模式下以 AUTH_PASSWORD 为权威密码：若库中哈希与当前配置不一致则同步重置，
		// 修复"先以单用户模式启动、后开启密码登录导致管理员无法登录"的问题。
		var hash string
		if err := db.QueryRow(`SELECT password_hash FROM users WHERE username = $1`, authUsername).Scan(&hash); err == nil {
			if !verifyPassword(hash, authUsername, authPassword) {
				if _, err := db.Exec(`UPDATE users SET password_hash = $1 WHERE username = $2`, mustHash(authPassword), authUsername); err != nil {
					log.Fatalf("Failed to sync admin password: %v", err)
				}
				log.Printf("🔑 Admin password synced to AUTH_PASSWORD for %s", authUsername)
			}
		}
	}
	adminUserID = uid

	if _, err := db.Exec("UPDATE events SET user_id = $1 WHERE user_id IS NULL", uid); err != nil {
		log.Printf("⚠️  Failed to backfill user_id: %v", err)
	}

	if _, err := db.Exec("UPDATE sources_config SET user_id = $1 WHERE user_id IS NULL", uid); err != nil {
		log.Printf("⚠️  Failed to backfill sources_config.user_id: %v", err)
	}
}
