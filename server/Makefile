# rmapp のビルド・テスト用 Makefile
#
#   make build      サーバーバイナリを bin/rmapp に生成する
#   make test-unit  ユニットテスト（internal/httpapi 以外すべて）
#   make test-api   API テスト（internal/httpapi。CLAUDE.md §4.7 の分割）
#   make test-e2e   ブラウザ E2E（chromedp + 同梱 Chromium。build tag e2e）
#   make clean      ビルド成果物を削除する

BIN := bin/rmapp

.PHONY: build test-unit test-api test-e2e clean

build:
	go build -o $(BIN) ./cmd/rmapp

test-unit:
	go test $$(go list ./... | grep -v /internal/httpapi | grep -v /e2e)

# E2E は build tag e2e を付けたときだけビルド・実行される（通常のユニット/
# API テストや CI の go build には含まれない）。証跡の保存先は
# E2E_ARTIFACT_DIR で上書きできる。
test-e2e:
	go test -tags e2e -count=1 -timeout 180s ./e2e/...

test-api:
	@if [ -d internal/httpapi ]; then \
		go test ./internal/httpapi/...; \
	else \
		echo "internal/httpapi は未実装のためスキップ（docs/plan.md フェーズ1）"; \
	fi

clean:
	rm -rf bin
