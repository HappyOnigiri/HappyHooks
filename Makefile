GO ?= go
INSTALL_DIR ?= $(HOME)/.local/bin
RELEASE_DIR ?= artifacts/release
# バージョンの真実源はリリースタグ（vX.Y.Z）である。タグを取得していない checkout ではコミットへ退避する。
VERSION ?= $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/HappyOnigiri/hhx/internal/version.Version=$(VERSION) -X github.com/HappyOnigiri/hhx/internal/version.BuildMeta=dev
# カバレッジの閾値と対象。hook を移植するときは、そのパッケージを一覧へ足す。
GO_COVERAGE_MIN ?= 85.0
GO_COVERAGE_PACKAGES := ./cmd/hhx ./internal/config ./internal/hookrt ./internal/install ./internal/registry \
	./internal/update ./internal/version ./internal/pycompat \
	./internal/hooks/prmergeguard ./internal/hooks/idlewaitguard ./internal/hooks/forbiddentermguard \
	./internal/hooks/githookspathguard ./internal/hooks/irreversibleguard ./internal/hooks/dangerousrmguard \
	./internal/hooks/discardguard ./internal/hooks/exitplansubagentguard ./internal/waitci \
	./internal/hooks/pushcicontext ./internal/hooks/prbodystaleness ./internal/hooks/prcontext \
	./internal/hooks/agentslocalcontext ./internal/hooks/generatededitguard ./internal/hookexec ./internal/hookcache ./internal/toolresponse ./internal/i18n
GOLANGCI_LINT_VERSION := $(shell awk '$$1 == "golangci-lint" { print $$2 }' .tool-versions)
GOLANGCI_LINT := bin/golangci-lint
# CI は CITEST に citest のパスを渡し、落ちたテストだけを 1 回再実行して報告を CI_TEST_ARTIFACT_DIR に残す。
# 空なら go test をそのまま走らせる。
CITEST ?=
CI_TEST_ARTIFACT_DIR ?= artifacts/ci-tests
# `make ci` は CPU 数だけジョブを並列に走らせる。`make ci CI_JOBS=4` で上書きできる。
CI_JOBS ?= $(shell nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)
# GNU make 4 は並列ジョブの出力をまとめて表示するが、GNU make 3.81 (macOS) は混ざる。
CI_MAKEFLAGS := -j$(CI_JOBS) --keep-going $(if $(filter output-sync,$(.FEATURES)),--output-sync=target)

.PHONY: build install fmt lint go-lint go-deadcode mod-tidy-check markdown-lint reporter-check test test-race-coverage \
	version-check release release-check install-test uninstall-test test-ja ci ci-checks $(GOLANGCI_LINT)

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/hhx ./cmd/hhx

# バイナリを置くだけで、agent の設定には触れない。登録は `hhx install` で行う。
install: build
	install -d "$(INSTALL_DIR)"
	install -m 0755 bin/hhx "$(INSTALL_DIR)/hhx"

fmt: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) fmt ./...

lint: go-lint go-deadcode markdown-lint

# golangci-lint が govet と gofmt の検査を行うので、別に `go vet` を走らせる必要はない。
go-lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

go-deadcode:
	@output="$$($(GO) tool deadcode -test ./...)"; \
	if [ -n "$$output" ]; then printf '%s\n' "$$output"; echo "deadcode: unreachable functions found"; exit 1; fi

mod-tidy-check:
	$(GO) mod tidy -diff

markdown-lint:
	$(GO) run ./tools/checkmarkdownlines

$(GOLANGCI_LINT):
	@if [ ! -x "$@" ] || ! "$@" version 2>/dev/null | grep -Fq "$(GOLANGCI_LINT_VERSION)"; then \
		mkdir -p "$(dir $@)"; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/v$(GOLANGCI_LINT_VERSION)/install.sh | sh -s -- -b bin v$(GOLANGCI_LINT_VERSION); \
	fi

# 起票の reporter と、ワークフロー・Makefile・citest の間の名前の契約を確かめる。
reporter-check:
	node --test .github/scripts/report-flaky-tests.test.cjs

test:
	$(GO) test -shuffle=on -count=1 ./...

# 同じテストを日本語の表示（language: ja）でも流す。判定は言語で変わらず、文面はカタログどおりであることを確かめる。
test-ja:
	HHX_TEST_LANGUAGE=ja $(GO) test -shuffle=on -count=1 ./...

# race 有効の 1 回の実行で、テストの成否とカバレッジの閾値の両方を確かめる。
test-race-coverage:
	@profile="$$(mktemp)" || exit $$?; \
	trap 'rm -f "$$profile"' EXIT; \
	if [ -n "$(CITEST)" ]; then \
	  "$(CITEST)" -profile race-coverage -report-dir "$(CI_TEST_ARTIFACT_DIR)/race-coverage" -coverprofile "$$profile" -- \
	    $(GO) test -race -shuffle=on -count=1 -coverprofile="$$profile" ./... || exit $$?; \
	else \
	  $(GO) test -race -shuffle=on -count=1 -coverprofile="$$profile" ./... || exit $$?; \
	fi; \
	GO="$(GO)" scripts/check-go-coverage.sh "$$profile" "$(GO_COVERAGE_MIN)" $(GO_COVERAGE_PACKAGES)

# 表示が ldflags の埋め込みまで通っていることを確かめる。
# 接頭辞だけの確認では、-X のパスが変わって Version が undefined のままでも気付けない。
version-check: build
	@actual="$$(./bin/hhx version)"; expected="hhx version $(VERSION)-dev"; \
	test "$$actual" = "$$expected" || { echo "hhx version is '$$actual', want '$$expected'"; exit 1; }

# 配布物は明示したタグでだけ作り、開発用の build / install が付ける -dev をそのまま残す。
release:
	GO="$(GO)" RELEASE_VERSION="$(RELEASE_VERSION)" RELEASE_DIR="$(RELEASE_DIR)" bash scripts/build-release.sh

# 対象の OS・CPU・CGO とチェックサム、インストーラーへのタグの差し込みを検査する。
# Go の `go version -m` は -ldflags を表示しないので、版の表示は macOS arm64 の手元でだけ確かめる。
release-check:
	@directory="$$(mktemp -d)" || exit $$?; \
	trap 'rm -rf "$$directory"' EXIT; \
	GO="$(GO)" RELEASE_VERSION=v0.0.0 RELEASE_DIR="$$directory" bash scripts/build-release.sh || exit $$?; \
	$(GO) version -m "$$directory/hhx-darwin-arm64" > "$$directory/build-info" || exit $$?; \
	grep -Fq 'CGO_ENABLED=0' "$$directory/build-info" || exit $$?; \
	grep -Fq 'GOOS=darwin' "$$directory/build-info" || exit $$?; \
	grep -Fq 'GOARCH=arm64' "$$directory/build-info" || exit $$?; \
	grep -Fq "release_version='v0.0.0'" "$$directory/install.sh" || exit $$?; \
	bash -n "$$directory/install.sh" || exit $$?; \
	cmp scripts/uninstall.sh "$$directory/uninstall.sh" || exit $$?; \
	(cd "$$directory" && shasum -a 256 -c checksums.txt) || exit $$?; \
	if [ "$$(uname -sm)" = 'Darwin arm64' ]; then \
	  test "$$("$$directory/hhx-darwin-arm64" version)" = 'hhx version v0.0.0' || exit $$?; \
	fi

install-test:
	bash -n scripts/install.sh
	bash -n scripts/test-install.sh
	bash scripts/test-install.sh

uninstall-test:
	bash -n scripts/uninstall.sh
	bash -n scripts/test-uninstall.sh
	bash scripts/test-uninstall.sh

ci:
	$(MAKE) $(CI_MAKEFLAGS) ci-checks

# どのチェックも読み取り専用か、自分の出力先（bin/ と一時ディレクトリ）にしか書かないので、並行して実行できる。
ci-checks: version-check release-check install-test uninstall-test lint reporter-check test-race-coverage test-ja mod-tidy-check
