// Package migrations は SQL マイグレーションファイルを埋め込みで提供する。
// 適用は internal/store が行う。ファイル名は連番（NNNN_name.sql）で、
// 名前順に適用される。適用済みのものは変更せず、常に新しい番号を足すこと。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
