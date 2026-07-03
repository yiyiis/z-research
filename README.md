# z-research

一个基于 [Eino](https://github.com/cloudwego/eino) 的 **AI 研究 Agent 全栈应用**（Go 后端 + React 前端）。
输入一个研究问题，它会自动联网检索、抓取网页、压缩提炼资料，并生成一份**带来源引用的中文 Markdown 报告**，
全程通过 **WebSocket** 实时推送研究进度，历史报告持久化到 SQLite。

> 实时推送用 WebSocket（非 SSE）：研究过程的 LLM 推理有较长静默期，SSE 会因 HTTP idle 超时断连，WebSocket 是全双工长连接，天然保持。与 [gpt-researcher](https://github.com/assafelovic/gpt-researcher) 的 `/ws` 设计一致。

> 参考设计见 [`docs/Golang-爬虫脚本-Eino-集成.md`](docs/Golang-爬虫脚本-Eino-集成.md) 与 [gpt-researcher](https://github.com/assafelovic/gpt-researcher) 的默认工作流。

---

## 一、环境要求

| 依赖 | 版本 | 用途 | 是否必需 |
|---|---|---|---|
| **Go** | ≥ 1.25 | 后端 | ✅ 必需 |
| **Node.js** | ≥ 18 | 前端开发与构建 | ⚠️ 仅前端需要 |
| **Make** | 任意 | 执行 Makefile 命令 | ✅ 必需（或参考下文手动命令） |
| **LLM API Key** | — | 对话模型 | ✅ 必需（默认智谱 GLM） |

> 没装 Node？后端 API 仍可独立运行，只是没有前端界面。
> 没装 Make？下文每条命令都附了等价的「手动命令」，可直接复制到终端执行。

---

## 二、首次配置

```bash
cd backend
cp .env.example .env
# 用编辑器打开 backend/.env，至少填入：
#   ZHIPU_API_KEY=你的智谱GLM密钥
```

其余配置（模型、端口、研究参数）都有内置默认值，可不动。完整说明见 [`backend/.env.example`](backend/.env.example)。

可选：用自建 Ollama 做 embedding（无需改代码，只改 `.env`）：
```bash
EMBED_BASE_URL=http://your-ollama-host:11434/v1
EMBED_MODEL=nomic-embed-text
EMBED_API_KEY=ollama
```

---

## 三、运行（开发模式）

**开发模式下前后端分离运行**：后端跑在 `:8080`，前端 Vite dev server 跑在 `:5173` 并自动把 `/api` 请求代理到后端。

**用 Make：**
```bash
make install    # 首次：安装依赖（go mod tidy + npm install）
make dev        # 启动后端 + 前端（并行）
```

**手动命令（等价）：**
```bash
# 终端 1 —— 后端
cd backend
go mod tidy
go run ./cmd/server --dev

# 终端 2 —— 前端
cd frontend
npm install
npm run dev
```

打开浏览器访问 **http://localhost:5173** 即可。

> 后端 API 也可单独访问：http://localhost:8080/api/reports

---

## 四、编译（生产模式，单二进制）

生产模式把前端构建产物内嵌进 Go 二进制，部署只需一个可执行文件。

**用 Make：**
```bash
make build      # 构建：前端 → 内嵌 → 编译 backend/z-research-server.exe
make run        # 运行（前端已内嵌）
```

`make build` 实际执行三步：
1. `npm run build` → 产出 `frontend/dist/`
2. 拷贝到 `backend/internal/api/web/`（供 `go:embed` 内嵌）
3. `go build -o z-research-server.exe ./cmd/server`

**手动命令（等价）：**
```bash
# 1. 构建前端
cd frontend
npm install
npm run build

# 2. 拷贝到 embed 目录
rm -rf ../backend/internal/api/web
mkdir -p ../backend/internal/api/web
cp -r dist/. ../backend/internal/api/web/

# 3. 编译二进制
cd ../backend
go build -o z-research-server.exe ./cmd/server

# 4. 运行
./z-research-server.exe --dev=false
```

运行后访问 **http://localhost:8080**（前后端同源，前端由后端托管）。

> 二进制可拷贝到任意机器运行，无需 Go/Node 环境（仅需 `.env` 配好 LLM 与端口）。

---

## 五、测试

测试都在 `backend/`，覆盖每个功能点（config / llm / search / compress / researcher / store / api）。
其中部分是**联网测试**（真实调用 DuckDuckGo、Ollama），无网络时会自动跳过而非失败。

**用 Make：**
```bash
make test       # 后端全部测试
```

**手动命令（等价）：**
```bash
cd backend
go test ./...                              # 全部测试（联网的会跳过）
go test -run 'TestStore|TestReport' ./...  # 只跑纯单元测试（无需网络）
go test -v ./internal/store/               # 单个包，详细输出
```

---

## 六、兼容旧 CLI 用法

仍可用命令行直接研究并打印报告到 stdout（不启动 HTTP 服务）：

```bash
cd backend
go run ./cmd/server --cli "2026 年固态电池降本的最新进展"
```

---

## 七、其他常用命令

```bash
make tidy     # go mod tidy
make clean    # 清理前端 dist / 二进制 / embed 产物
make help     # 列出所有命令
```

---

## 项目结构

```
z-research/
├── backend/                 # Go 后端 (Gin + Eino + SQLite)
│   ├── cmd/server/          # 入口（HTTP 服务 / 兼容 --cli）
│   └── internal/            # 按领域分层
│       ├── config/          # 配置（.env / 环境变量）
│       ├── llm/             # 对话模型 + embedding 封装
│       ├── search/          # DuckDuckGo 搜索
│       ├── scraper/         # 网页抓取（goquery）
│       ├── compress/        # 内存 embedding 相似度压缩
│       ├── prompts/         # 中文 prompt 模板
│       ├── researcher/      # ★研究编排引擎 + 顶层 Run() + progress 回调
│       ├── store/           # ★SQLite 持久化
│       └── api/             # ★HTTP 层（Gin + SSE + 报告 CRUD + 前端 embed）
├── frontend/                # React 前端 (Vite + TypeScript + Tailwind)
│   └── src/{api,hooks,components}
├── docs/                    # 设计文档 + architecture.md
└── Makefile                 # dev / build / run / test 等命令
```

**核心工作流**（gpt-researcher 默认固定工作流的 Go 移植）：

```
query → 选角色(LLM) → 规划子查询(LLM)
      → 并发{搜索 → 抓取 → 内存 embedding 压缩}
      → 撰写中文 Markdown 报告（带 [n] 引用）
全程 SSE 实时推送进度；报告存入 SQLite。
```

详细架构见 [`docs/architecture.md`](docs/architecture.md)。

## API 速览

| 方法 | 路径 | 说明 |
|---|---|---|
| `WS` | `/ws` | WebSocket 研究端点：发 `{"query":"..."}`，收 `progress`/`sources`/`done`/`error` 帧 |
| `GET` | `/api/reports` | 历史列表（不含正文） |
| `GET` | `/api/reports/:id` | 单篇报告全文 |
| `DELETE` | `/api/reports/:id` | 删除报告 |

## 主要配置项（`backend/.env`）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ZHIPU_API_KEY` | （必填） | LLM 凭证 |
| `LLM_BASE_URL` | GLM 端点 | LLM 的 OpenAI 兼容端点 |
| `LLM_MODEL` | `glm-4-plus` | 对话模型 |
| `EMBED_BASE_URL` | GLM 端点 | embedding 端点（可指向自建 Ollama `/v1`） |
| `EMBED_MODEL` | `embedding-3` | embedding 模型 |
| `MAX_ITERATIONS` | `3` | 子查询数量（研究广度） |
| `MAX_RESULTS` | `5` | 每子查询搜索条数 |
| `MAX_SCRAPE` | `3` | 每子查询实际抓取网页数 |
| `TOTAL_WORDS` | `1200` | 报告目标字数 |
| `HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `DB_PATH` | `data/z-research.db` | SQLite 路径 |

## 后续扩展方向

- **Deep Research**：递归树（breadth/depth），每层基于上轮 learnings 追问。
- **Multi-Agent**：增加 Reviewer 审核回路，对草稿挑刺打回重做。
- **本地知识库 RAG**：接入本地 PDF/Word，用轻量级向量库做持久化检索。
- **多用户/鉴权**：加 JWT。
