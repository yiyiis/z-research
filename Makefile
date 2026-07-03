# z-research Makefile
# 提供开发/构建/运行的便捷命令。
# 详见 README.md 的「运行」「编译」「测试」章节。

BACKEND_DIR := backend
FRONTEND_DIR := frontend
EMBED_DIR := $(BACKEND_DIR)/internal/api/web
BIN := $(BACKEND_DIR)/z-research-server.exe

# 检测 node/npm 是否可用（前端相关命令需依赖）。
NPM := $(shell command -v npm 2>/dev/null)

.PHONY: help dev backend frontend install build run test test-go tidy clean

help: ## 显示所有命令
	@echo "z-research 常用命令："
	@echo "  make install   首次安装依赖（go mod tidy + npm install）"
	@echo "  make dev       开发模式：后端(:8080) + 前端(:5173) 并行"
	@echo "  make backend   仅启动后端"
	@echo "  make frontend  仅启动前端 dev server（需先 make install）"
	@echo "  make build     生产编译：构建前端 → 内嵌 → 编译单二进制"
	@echo "  make run       运行生产二进制（前端已内嵌）"
	@echo "  make test      后端全部测试"
	@echo "  make test-go   仅 Go 测试（同 test）"
	@echo "  make tidy      go mod tidy"
	@echo "  make clean     清理构建产物"

## 首次安装依赖
install:
	@cd $(BACKEND_DIR) && go mod tidy
ifdef NPM
	@cd $(FRONTEND_DIR) && npm install
else
	@echo "⚠️  未检测到 npm，跳过前端依赖安装（后端仍可用）"
endif

## 开发模式：后端 + 前端 dev server 并行
dev:
	@echo "🚀 启动开发模式（后端 :8080，前端 :5173）"
	@echo "    浏览器打开 http://localhost:5173"
	@$(MAKE) -j backend frontend

## 仅后端（开发）
backend:
	@cd $(BACKEND_DIR) && go run ./cmd/server --dev

## 仅前端 dev server（开发）
frontend:
ifndef NPM
	@echo "❌ 未检测到 npm。请先安装 Node.js 并运行: make install"; exit 1
else
	@cd $(FRONTEND_DIR) && npm run dev
endif

## 生产编译：前端 → embed → Go 二进制（产物 backend/z-research-server.exe）
build:
	@echo "==> 步骤 1/3：构建前端..."
ifdef NPM
	@cd $(FRONTEND_DIR) && npm install && npm run build
	@echo "==> 步骤 2/3：拷贝前端产物到 $(EMBED_DIR)"
	@rm -rf $(EMBED_DIR) && mkdir -p $(EMBED_DIR)
	@cp -r $(FRONTEND_DIR)/dist/. $(EMBED_DIR)/
else
	@echo "⚠️  未检测到 npm，跳过前端构建（二进制将不含前端 SPA，仅保留 API）"
endif
	@echo "==> 步骤 3/3：编译 Go 二进制..."
	@cd $(BACKEND_DIR) && go build -o z-research-server.exe ./cmd/server
	@echo "✅ 完成！二进制：$(BIN)"
	@echo "   运行：make run"

## 运行生产二进制（前端已内嵌）
run:
	@if [ ! -f $(BIN) ]; then echo "❌ 二进制不存在，请先运行: make build"; exit 1; fi
	@cd $(BACKEND_DIR) && ./z-research-server.exe --dev=false

## 后端全部测试（含联网测试，无网络会自动跳过）
test: test-go

test-go:
	@cd $(BACKEND_DIR) && go test ./...

## go mod tidy
tidy:
	@cd $(BACKEND_DIR) && go mod tidy

## 清理构建产物
clean:
	@rm -rf $(FRONTEND_DIR)/dist
	@rm -f $(BIN)
	@rm -rf $(EMBED_DIR)/* && touch $(EMBED_DIR)/.gitkeep
	@echo "✅ 已清理"
