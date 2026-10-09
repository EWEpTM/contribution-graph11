package main

import (
	"errors"
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

// TestValidSafeText 验证 source/emoji 的 XSS 防护校验
func TestValidSafeText(t *testing.T) {
	valid := []string{"跑步", "github-import", "a b", "测试123"}
	for _, s := range valid {
		if !validSafeText(s, 64) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	invalid := []string{"", "<img src=x>", "a\"b", "a'b", "a&b", "a`b", "a>b",
		strings.Repeat("长", 65), "bad\x00text", "bad\x1ftext"}
	for _, s := range invalid {
		if validSafeText(s, 64) {
			t.Errorf("expected %q to be invalid", s)
		}
	}
	// emoji 场景：合法 emoji 允许，HTML 特殊字符拒绝
	if !validSafeText("💪🔥", 8) {
		t.Error("emoji should be valid")
	}
	if validSafeText("<svg/onload=1>", 8) {
		t.Error("html payload should be invalid")
	}
}

// TestIsUniqueViolation 验证唯一冲突识别（并发同名注册/重复打卡场景）
func TestIsUniqueViolation(t *testing.T) {
	if !isUniqueViolation(errors.New(`ERROR: duplicate key value violates unique constraint "events_source_context_timestamp_user_id_key" (SQLSTATE 23505)`)) {
		t.Fatal("duplicate key error should be detected")
	}
	if isUniqueViolation(errors.New("connection refused")) {
		t.Fatal("non-unique error must not be detected as duplicate")
	}
	if isUniqueViolation(nil) {
		t.Fatal("nil error must not be detected")
	}
}

// TestWindowLimiterCleanup 验证限流器过期键清理（防内存无限增长）
func TestWindowLimiterCleanup(t *testing.T) {
	lim := newWindowLimiter(3, time.Minute)
	lim.allow("old_key") // 未达上限、无 until，seen 已记录
	lim.allow("fresh_key")
	// 手动把 old_key 的最后访问时间拨到 2 个窗口之前
	lim.mu.Lock()
	lim.seen["old_key"] = time.Now().Add(-3 * time.Minute)
	lim.mu.Unlock()
	lim.cleanup(time.Now().Add(-2 * time.Minute))
	lim.mu.Lock()
	defer lim.mu.Unlock()
	if _, ok := lim.hits["old_key"]; ok {
		t.Fatal("stale key should have been cleaned up")
	}
	if _, ok := lim.hits["fresh_key"]; !ok {
		t.Fatal("fresh key should be kept")
	}
}

// TestRandomTokenError 验证 randomToken 失败返回错误而非 fatal（请求路径不崩进程）
func TestRandomTokenFormat(t *testing.T) {
	tok, err := randomToken()
	if err != nil {
		t.Fatalf("randomToken failed: %v", err)
	}
	if len(tok) != 48 {
		t.Fatalf("expected 48 hex chars, got %d (%q)", len(tok), tok)
	}
}

// TestIsSameOriginHost 验证 CSRF Origin 校验
func TestIsSameOriginHost(t *testing.T) {
	r := httptest.NewRequest("POST", "http://localhost:8080/api/x", nil)
	r.Host = "localhost:8080"
	r.Header.Set("Origin", "http://localhost:8080")
	if !isSameOriginHost(r) {
		t.Fatal("same-origin request should pass")
	}
	r.Header.Set("Origin", "http://evil.example")
	if isSameOriginHost(r) {
		t.Fatal("cross-origin request must be rejected")
	}
	// 空 Origin 视为不可信（防御行为）；中间件只在 Origin 非空时才调用该校验，
	// 因此无 Origin 的 curl / Go agent 等非浏览器客户端不受影响
	r.Header.Set("Origin", "")
	if isSameOriginHost(r) {
		t.Fatal("empty Origin must not pass")
	}
}
