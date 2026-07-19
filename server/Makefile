# rmapp のビルド・テスト用 Makefile
#
#   make build      サーバーバイナリを bin/rmapp に生成する
#   make test-unit  ユニットテスト（internal/httpapi 以外すべて）
#   make test-api   API テスト（internal/httpapi。CLAUDE.md §4.7 の分割）
#   make clean      ビルド成果物を削除する

BIN := bin/rmapp

.PHONY: build test-unit test-api clean

build:
	go build -o $(BIN) ./cmd/rmapp

test-unit:
	go test $$(go list ./... | grep -v /internal/httpapi)

test-api:
	@if [ -d internal/httpapi ]; then \
		go test ./internal/httpapi/...; \
	else \
		echo "internal/httpapi は未実装のためスキップ（docs/plan.md フェーズ1）"; \
	fi

clean:
	rm -rf bin
