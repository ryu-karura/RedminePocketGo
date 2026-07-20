package auth

import (
	"sync"
	"time"
)

// RateLimiter は連続失敗によるロックを担う（テンプレートの方式:
// 連続 5 回失敗で 60 秒ロック）。キーはログイン名やクライアント IP など
// 呼び出し側が決める。状態はインメモリ（テンプレート踏襲）。
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rlEntry
	limit   int
	lock    time.Duration
	now     func() time.Time
}

type rlEntry struct {
	fails       int
	lockedUntil time.Time
}

func NewRateLimiter(limit int, lock time.Duration) *RateLimiter {
	return &RateLimiter{
		entries: map[string]*rlEntry{},
		limit:   limit,
		lock:    lock,
		now:     time.Now,
	}
}

// Allow はキーが現在ロック中でなければ true を返す。
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return true
	}
	if !e.lockedUntil.IsZero() {
		if l.now().Before(e.lockedUntil) {
			return false
		}
		// ロック明けは白紙に戻す
		delete(l.entries, key)
	}
	return true
}

// Fail は失敗を 1 回記録する。連続 limit 回でロックする。
func (l *RateLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		e = &rlEntry{}
		l.entries[key] = e
	}
	e.fails++
	if e.fails >= l.limit {
		e.lockedUntil = l.now().Add(l.lock)
		e.fails = 0
	}
}

// Succeed は成功時に連続失敗カウントを消す。
func (l *RateLimiter) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}
