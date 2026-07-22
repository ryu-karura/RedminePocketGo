package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := run(&buf, []string{"-version"}); err != nil {
		t.Fatalf("run(-version) error = %v", err)
	}
	if !strings.Contains(buf.String(), "rmapp") {
		t.Errorf("run(-version) の出力にアプリ名が含まれない: %q", buf.String())
	}
}

func TestRunMissingConfig(t *testing.T) {
	var buf bytes.Buffer
	err := run(&buf, []string{"-config", filepath.Join(t.TempDir(), "nope.yaml")})
	if err == nil {
		t.Fatal("存在しない設定ファイルでエラーにならない")
	}
}

func TestNewLoggerLevels(t *testing.T) {
	tests := []struct {
		level     string
		wantDebug bool // debug レベルの出力が記録されるか
	}{
		{"debug", true},
		{"info", false},
		{"warn", false},
		{"error", false},
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			var buf bytes.Buffer
			logger := newLogger(tt.level, &buf)
			logger.Debug("debug message")
			if got := buf.Len() > 0; got != tt.wantDebug {
				t.Errorf("level %s: debug logged = %v; want %v", tt.level, got, tt.wantDebug)
			}
		})
	}
}

func TestNewLoggerIsStructuredJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := newLogger("info", &buf)
	logger.Info("hello", slog.String("k", "v"))
	line := buf.String()
	for _, want := range []string{`"msg":"hello"`, `"k":"v"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q lacks %q (JSON 構造化になっていない)", line, want)
		}
	}
}
