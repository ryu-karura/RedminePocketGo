// Package webfs は SPA の静的アセット配信を担う（テンプレート踏襲）。
// フラグメントは fetch で読まれるため、SPA は必ず本サーバー経由で配信する。
package webfs

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Handler は webroot を配信するハンドラを返す。
//   - baseURL: サブパス配信時の接頭辞（例 "/rmapp"）。空ならルート配信。
//   - noCache: true なら全レスポンスに Cache-Control: no-store を付与する
//     （テンプレートの noCache 設定の踏襲。開発時のキャッシュ事故防止）。
func Handler(webroot, baseURL string, noCache bool) http.Handler {
	var h http.Handler = http.FileServer(noListingDir{http.Dir(webroot)})

	if baseURL != "" {
		prefix := strings.TrimSuffix(baseURL, "/")
		inner := http.StripPrefix(prefix, h)
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == prefix {
				http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
				return
			}
			if !strings.HasPrefix(r.URL.Path, prefix+"/") {
				http.NotFound(w, r)
				return
			}
			inner.ServeHTTP(w, r)
		})
	}

	if noCache {
		inner := h
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			inner.ServeHTTP(w, r)
		})
	}
	return h
}

// noListingDir は index.html のないディレクトリへのアクセスを 404 にする。
// http.FileServer の自動ディレクトリ一覧はアセット構成の露出になるため。
type noListingDir struct {
	base http.Dir
}

func (d noListingDir) Open(name string) (http.File, error) {
	f, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		idx, err := d.base.Open(path.Join(name, "index.html"))
		if err != nil {
			f.Close()
			return nil, fs.ErrNotExist
		}
		idx.Close()
	}
	return f, nil
}
