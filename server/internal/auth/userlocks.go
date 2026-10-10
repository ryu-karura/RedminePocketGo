package auth

import (
	"hash/fnv"
	"sync"
)

// UserLocks は利用者単位の排他。OAuth トークンの組は利用者ごとに 1 つで端末間
// 共有のため、「ログイン（組の保存 + セッション発行）」と「ログアウト後始末
// （残セッション数の確認 + 組の取り出し・削除）」が交差すると、新しいログインの
// 組を失効・削除してしまう。両者を同じ利用者のロックで直列化する。
//
// 単一プロセス前提（SQLite を 1 プロセスで使う構成。Design.md §5）。固定数の
// ミューテックスへ利用者 ID をハッシュで割り当てる（異なる利用者が同じ枠に
// 当たっても待つだけで、正しさは変わらない）。nil は排他なし。
type UserLocks struct {
	stripes [64]sync.Mutex
}

func NewUserLocks() *UserLocks { return &UserLocks{} }

// Lock は userID のロックを取り、解除関数を返す。
func (l *UserLocks) Lock(userID string) (unlock func()) {
	if l == nil {
		return func() {}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	m := &l.stripes[h.Sum32()%uint32(len(l.stripes))]
	m.Lock()
	return m.Unlock
}
