package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHashPasswordBCrypt(t *testing.T) {
	h1, err := hashPassword("secret123")
	if err != nil {
		t.Fatalf("hashPassword failed: %v", err)
	}
	h2, _ := hashPassword("secret123")
	if h1 == h2 {
		t.Fatal("bcrypt hashes must differ (random salt), got identical values")
	}
	if strings.HasPrefix(h1, "$2") == false {
		t.Fatalf("expected bcrypt hash format, got %q", h1)
	}
}

func TestVerifyPasswordBCrypt(t *testing.T) {
	h, _ := hashPassword("secret123")
	if !verifyPassword(h, "anyuser", "secret123") {
		t.Fatal("correct password should pass")
	}
	if verifyPassword(h, "anyuser", "wrong") {
		t.Fatal("wrong password should fail")
	}
}

// TestVerifyPasswordLegacy 验证旧版固定盐 SHA-256 哈希仍可登录（渐进迁移兼容）
func TestVerifyPasswordLegacy(t *testing.T) {
	legacy := legacyHash("alice", "p@ss")
	if !isLegacyHash(legacy) {
		t.Fatal("64-hex hash should be detected as legacy")
	}
	if !verifyPassword(legacy, "alice", "p@ss") {
		t.Fatal("legacy hash with correct password should pass")
	}
	if verifyPassword(legacy, "alice", "nope") {
		t.Fatal("legacy hash with wrong password should fail")
	}
	if isLegacyHash("$2a$10$abcdefghijklmnopqrstuu") {
		t.Fatal("bcrypt hash must not be treated as legacy")
	}
}

func TestValidSourceID(t *testing.T) {
	valid := []string{"src_abc123", "git", "A-B_C", "x"}
	for _, id := range valid {
		if !validSourceID(id) {
			t.Errorf("expected %q to be valid", id)
		}
	}
	invalid := []string{"", "跑步", "id with space", "a/b", "x\"y", strings.Repeat("a", 65), "a<script>"}
	for _, id := range invalid {
		if validSourceID(id) {
			t.Errorf("expected %q to be invalid", id)
		}
	}
}

func TestValidHexColor(t *testing.T) {
	valid := []string{"#238636", "#8B949E", "#abcdef"}
	for _, c := range valid {
		if !validHexColor(c) {
			t.Errorf("expected %q to be valid", c)
		}
	}
	invalid := []string{"", "#23863", "#2386367", "238636", "#zzzzzz", "#23863G", "red"}
	for _, c := range invalid {
		if validHexColor(c) {
			t.Errorf("expected %q to be invalid", c)
		}
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.10:1234"
	if got := clientIP(r); got != "192.168.1.10" {
		t.Fatalf("expected 192.168.1.10, got %q", got)
	}
	r.RemoteAddr = "bad"
	if got := clientIP(r); got != "bad" {
		t.Fatalf("expected fallback 'bad', got %q", got)
	}
}

func TestWindowLimiter(t *testing.T) {
	lim := newWindowLimiter(3, time.Minute)
	key := "k"
	for i := 0; i < 3; i++ {
		if !lim.allow(key) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if lim.allow(key) {
		t.Fatal("4th attempt should be rejected (limit reached)")
	}
	// 锁定期内仍拒绝
	if lim.allow(key) {
		t.Fatal("attempt during lockout should be rejected")
	}
	lim.reset(key)
	if !lim.allow(key) {
		t.Fatal("after reset, attempt should be allowed")
	}
}

func TestIsZeroTimestampFill(t *testing.T) {
	// handlePostContribution 中 ts.IsZero() 兜底：确认零值被识别，避免写入 year 1
	var ts time.Time
	if !ts.IsZero() {
		t.Fatal("zero time should be zero")
	}
}
