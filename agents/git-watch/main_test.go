package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Tomer-Barak/contribution-graph/agents/lib"
)

func TestTruncateRuneSafe(t *testing.T) {
	// 中文等多字节字符按字符截断，不得产生乱码（非法 UTF-8 序列）
	s := "这是一个非常长的提交信息用于测试截断功能是否安全"
	got := lib.Truncate(s, 10)
	if len([]rune(got)) > 10 {
		t.Fatalf("truncated string has %d runes, want <= 10", len([]rune(got)))
	}
	if !validUTF8(got) {
		t.Fatalf("truncated string is not valid UTF-8: %q", got)
	}
	// 短字符串原样返回
	if lib.Truncate("abc", 10) != "abc" {
		t.Fatal("short string should be returned unchanged")
	}
	// 边界：恰好等于长度时不加省略号
	if lib.Truncate("1234567890", 10) != "1234567890" {
		t.Fatal("exact-length string should be returned unchanged")
	}
	// 防御：maxRunes 过小不 panic
	if lib.Truncate("abc", 1) != "..." {
		t.Fatal("tiny max should return ellipsis only")
	}
}

func TestSpreadSecondsNoOverflow(t *testing.T) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// 单日 2000 条（超过旧实现 1440 上限）：全部落在当天
	ts := lib.SpreadSeconds(base, 2000)
	if len(ts) != 2000 {
		t.Fatalf("expected 2000 stamps, got %d", len(ts))
	}
	for _, v := range ts {
		if v.Day() != 1 || v.Month() != 3 || v.Year() != 2026 {
			t.Fatalf("timestamp %v overflowed to another day", v)
		}
	}
	// 极端超限截断到 86400 且不跨天
	ts2 := lib.SpreadSeconds(base, 200000)
	if len(ts2) != 86400 {
		t.Fatalf("expected capped 86400 stamps, got %d", len(ts2))
	}
	for _, v := range ts2 {
		if v.Day() != 1 {
			t.Fatalf("timestamp %v overflowed", v)
		}
	}
	// 空输入
	if lib.SpreadSeconds(base, 0) != nil {
		t.Fatal("zero count should return nil")
	}
}

func validUTF8(s string) bool {
	return !strings.ContainsRune(s, '\uFFFD')
}
