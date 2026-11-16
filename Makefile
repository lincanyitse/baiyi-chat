# 白泽 (baiyi-chat) — 开发命令
#
# 本机没有 make 时，每个 target 下的注释行就是等价的手工命令。
#
# 常用：
#   make build     构建前端并编译
#   make build-go  只编译后端（跳过前端）
#   make serve     编译并启动 Web 服务
#   make test      测试
#   make check     vet + 格式 + 测试（提交前跑一次）
#   make repl      启动 REPL（调试用）

GO       ?= go
BINARY   ?= baiyi.exe
BUILDDIR ?= bin
DISTDIR  ?= dist
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)

LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# 发布目标：GOOS/GOARCH 组合。Windows 是主平台，其余为将来 Web/CLI 分发预留。
RELEASE_TARGETS := windows/amd64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: help web build build-go serve reset-password release run repl test cover race bench vet fmt fmt-check lint layering check tidy clean tools smoke

## help: 列出全部命令
help:
	@echo "白泽 baiyi-chat — 可用命令："
	@echo
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  make /'

## web: 构建前端到 internal/channel/web/dist（需要 Node 22，Vite 8 不支持 Node 18）
# npm --prefix web ci && npm --prefix web test && npm --prefix web run build
web:
	@command -v npm >/dev/null 2>&1 || { echo "需要 Node.js 与 npm 才能构建前端"; exit 1; }
	npm --prefix web ci --silent
	npm --prefix web test --silent
	npm --prefix web run build

## build: 构建前端并编译出 bin/baiyi.exe
# npm --prefix web run build && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/baiyi.exe ./cmd/baiyi
build: web
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/$(BINARY) ./cmd/baiyi

## build-go: 只编译 Go 后端（跳过前端，用于快速迭代）
# CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/baiyi.exe ./cmd/baiyi
build-go:
	@test -f internal/channel/web/dist/index.html || \
		echo "提示：internal/channel/web/dist 为空，Web 界面不可用；先跑 make web"
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/$(BINARY) ./cmd/baiyi

## serve: 编译并启动 Web 服务
# ./bin/baiyi.exe serve
serve: build-go
	./$(BUILDDIR)/$(BINARY) serve

## reset-password: 清除本地 Web 口令（忘记口令时用）
# ./bin/baiyi.exe reset-password
reset-password: build-go
	./$(BUILDDIR)/$(BINARY) reset-password

## release: 构建前端并交叉编译全部平台到 dist/
# 必须依赖 web：产物内嵌前端（go:embed），没有 dist 的 exe 只有 API 没有界面。
# 逐条执行，例如：
#   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
#     -o dist/baiyi-linux-amd64 ./cmd/baiyi
release: web
	@rm -rf $(DISTDIR)
	@mkdir -p $(DISTDIR)
	@for t in $(RELEASE_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  out=$(DISTDIR)/baiyi-$$os-$$arch; \
	  if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
	  echo "  $$os/$$arch -> $$out"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build -trimpath \
	    -ldflags "$(LDFLAGS)" -o $$out ./cmd/baiyi || exit 1; \
	done
	@echo "产物："; ls -lh $(DISTDIR)

## run: 启动 Web 服务（无参数即 Web）
# go run ./cmd/baiyi
run:
	$(GO) run ./cmd/baiyi

## repl: 启动 REPL（调试用，不是产品入口）
# go run ./cmd/baiyi repl
repl:
	$(GO) run ./cmd/baiyi repl

## test: 运行全部测试
# go test ./...
test:
	$(GO) test ./...

## cover: 跨包统计覆盖率，按包打印函数平均（单包数字会漏掉被别的包测到的代码）
# go test -coverpkg=./internal/... -coverprofile=coverage.txt ./internal/...
cover:
	$(GO) test -coverpkg=./internal/... -coverprofile=coverage.txt ./internal/...
	@$(GO) tool cover -func=coverage.txt | awk ' \
		$$1 ~ /\.go:[0-9]+:$$/ { \
			line = $$1; sub(/:[0-9]+:$$/, "", line); \
			n = split(line, p, "/"); pkg = p[1]; \
			for (i = 2; i < n; i++) pkg = pkg "/" p[i]; \
			pct = $$NF; gsub(/%/, "", pct); \
			sum[pkg] += pct; cnt[pkg]++; \
		} \
		END { \
			for (pkg in sum) printf "%6.1f%%  %s  (%d)\n", sum[pkg] / cnt[pkg], pkg, cnt[pkg]; \
		}' | sort -k2

## race: 带竞态检测跑测试（需要 cgo 与 C 编译器）
# CGO_ENABLED=1 go test -race ./...
# 本机若装了 w64devkit：先把它加进 PATH
race:
	@test -n "$$CC_BIN" || true; \
	if ! command -v gcc >/dev/null 2>&1 && [ -d "$(HOME)/Softwares/w64devkit/bin" ]; then \
	  echo "(使用 $(HOME)/Softwares/w64devkit/bin 作为 C 工具链)"; \
	  PATH="$(HOME)/Softwares/w64devkit/bin:$$PATH" CGO_ENABLED=1 $(GO) test -race ./... ; \
	else \
	  CGO_ENABLED=1 $(GO) test -race ./... ; \
	fi

## bench: 运行基准测试
# go test -bench=. -benchmem ./internal/...
bench:
	$(GO) test -bench=. -benchmem -run=^$$ ./internal/...

## vet: 静态检查
# go vet ./...
vet:
	$(GO) vet ./...

## fmt: 格式化
# gofmt -w .
fmt:
	gofmt -w .

## fmt-check: 只检查不修改
# gofmt -l .    （有输出说明未格式化）
fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "以下文件未格式化："; echo "$$out"; exit 1; fi

## lint: golangci-lint 静态检查
# golangci-lint run
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "未安装。执行 make tools"; exit 1; }
	golangci-lint run

## layering: 检查分层依赖方向（架构纲要 §2.3）
# bash scripts/check-layering.sh
layering:
	bash scripts/check-layering.sh

## check: 提交前完整检查
# go vet ./... && gofmt -l . && go test ./... && bash scripts/check-layering.sh
check: vet fmt-check test layering

## tidy: 整理依赖
# go mod tidy
tidy:
	$(GO) mod tidy

## clean: 清理构建产物
# rm -rf bin
clean:
	rm -rf $(BUILDDIR)

## tools: 安装开发工具
# go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

## smoke: 跑 FTS5 中文检索冒烟（验证 ADR-0007）
# go run ./scratch/fts5-smoke
smoke:
	CGO_ENABLED=0 $(GO) run ./scratch/fts5-smoke
