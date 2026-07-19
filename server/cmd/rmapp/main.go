// rmapp のエントリポイント。依存の組み立てと起動のみを行い、
// 業務ロジックは internal 配下のパッケージに置く（CLAUDE.md §4.1）。
package main

import (
	"fmt"
	"io"
	"os"
)

var version = "dev"

func main() {
	if err := run(os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rmapp:", err)
		os.Exit(1)
	}
}

// run は起動処理の本体。設定読み込みとサーバー起動の配線は
// docs/plan.md フェーズ1以降で実装する。
func run(out io.Writer, _ []string) error {
	fmt.Fprintf(out, "rmapp %s\n", version)
	return nil
}
