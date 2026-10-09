package main

import "testing"

func TestTruncateStringRuneSafe(t *testing.T) {
	// 中文等多字节字符按字符截断，不得产生乱码（非法 UTF-8 序列）
	s := "这是一个非常长的提交信息用于测试截断功能是否安全"
	got := truncateString(s, 10)
	if len([]rune(got)) > 10 {
		t.Fatalf("truncated string has %d runes, want <= 10", len([]rune(got)))
	}
	if !validUTF8(got) {
		t.Fatalf("truncated string is not valid UTF-8: %q", got)
	}
	// 短字符串原样返回
	if truncateString("abc", 10) != "abc" {
		t.Fatal("short string should be returned unchanged")
	}
	// 边界：恰好等于长度时不加省略号
	if truncateString("1234567890", 10) != "1234567890" {
		t.Fatal("exact-length string should be returned unchanged")
	}
}

func validUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}
