package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	var buf bytes.Buffer
	if err := run(&buf, nil); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(buf.String(), "rmapp") {
		t.Errorf("run() の出力にアプリ名が含まれない: %q", buf.String())
	}
}
