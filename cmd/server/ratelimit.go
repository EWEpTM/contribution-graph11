package main

import (
	"time"
)

func newWindowLimiter(limit int, win time.Duration) *windowLimiter {
	return &windowLimiter{
		hits:  map[string]int{},
		until: map[string]time.Time{},
		seen:  map[string]time.Time{},
		limit: limit,
		win:   win,
	}
}

// allow 每次尝试 +1；已锁定或达到上限则拒绝
func (w *windowLimiter) allow(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.seen[key] = now

	if len(w.hits) > 4096 {
		w.cleanupLocked(now.Add(-2 * w.win))
	}
	if until, ok := w.until[key]; ok && until.After(now) {
		return false
	}
	if until, ok := w.until[key]; ok && !until.After(now) {
		delete(w.until, key)
		w.hits[key] = 0
	}
	if w.hits[key] >= w.limit {
		w.until[key] = now.Add(w.win)
		return false
	}
	w.hits[key]++
	return true
}

// reset 成功时清零计数
func (w *windowLimiter) reset(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.hits, key)
	delete(w.until, key)
	delete(w.seen, key)
}

// cleanup 删除 before 时间之前未再访问的键（限流窗口的 2 倍以上）
func (w *windowLimiter) cleanup(before time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cleanupLocked(before)
}
func (w *windowLimiter) cleanupLocked(before time.Time) {
	for k, last := range w.seen {
		if last.Before(before) {
			delete(w.hits, k)
			delete(w.until, k)
			delete(w.seen, k)
		}
	}
}
