package auth

import (
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Now()
	l := NewRateLimiter(5, time.Minute)
	l.now = func() time.Time { return now }

	// 4 回失敗まではロックされない
	for i := 0; i < 4; i++ {
		l.Fail("alice")
	}
	if !l.Allow("alice") {
		t.Fatal("locked before 5th consecutive failure")
	}

	// 5 回目でロック
	l.Fail("alice")
	if l.Allow("alice") {
		t.Fatal("not locked after 5 consecutive failures")
	}
	// 他のキーは影響を受けない
	if !l.Allow("bob") {
		t.Fatal("unrelated key locked")
	}

	// 60 秒経過で解除
	now = now.Add(61 * time.Second)
	if !l.Allow("alice") {
		t.Fatal("still locked after lock window elapsed")
	}

	// 成功でカウントが消える: 4 失敗 → 成功 → 4 失敗 でもロックされない
	for i := 0; i < 4; i++ {
		l.Fail("carol")
	}
	l.Succeed("carol")
	for i := 0; i < 4; i++ {
		l.Fail("carol")
	}
	if !l.Allow("carol") {
		t.Fatal("success did not reset the consecutive-failure count")
	}
}
