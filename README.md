# z-research

一个基于 [Eino](https://github.com/cloudwego/eino) 的 **AI 研究 Agent 全栈应用**（Go 后端 + React 前端）。
输入一个研究问题，它会自动联网检索、抓取网页、压缩提炼资料，并生成一份**带来源引用的中文 Markdown 报告**，
全程通过 **WebSocket** 实时推送研究进度，历史报告持久化到 SQLite。

## 四种研究模式（均为 Eino Graph 编排）

| 模式 | 架构 | 适用 |
|---|---|---|
| **单 Agent** (`single`) | 5 节点线性 Graph：`choose_role → plan_search → parallel_research → compression → writer` | 快速简报（默认） |
| **多 Agent** (`multi`) | 10+ 节点状态图：`browser → planner → human_review → researcher(并行子图) → writer → fact_checker → visualizer → publisher`，含 3 个循环 | 高质量深度报告 + HITL |
| **ReAct Agent** (`react`) | ADK `react.NewAgent` + 工具（web_search / fetch_url），LLM 自主决定调用 | 真正的自主 Agent |
| **深度递归** (`deep`) | 5 节点 Graph：`choose_role → plan_search → deep_recurse(Lambda递归) → compress → writer`，breadth 逐层衰减 `max(2, b//2)` | 万字级综述（OpenAI Deep Research 风格） |

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

仍可用命令行直接研究并打印报告到 stdout（不启动 HTTP 服务）。`--mode` 选择引擎：

```bash
cd backend
# 单 Agent（默认）
go run ./cmd/server --cli "2026 年固态电池降本的最新进展"

# 深度递归（万字级综述）—— breadth/depth 可调，每层按 max(2, b//2) 衰减
go run ./cmd/server --cli "2026 年大模型推理优化最新进展" --mode deep --breadth 3 --depth 2

# 多智能体（带 HITL 时需启动 HTTP 服务走 WebSocket，CLI 模式自动 accept）
go run ./cmd/server --cli "MCP 模型上下文协议" --mode multi

# ReAct Agent
go run ./cmd/server --cli "对比 vLLM 和 TGI" --mode react
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
│   ├── cmd/server/          # 入口（HTTP 服务 / 兼容 --cli --mode）
│   └── internal/            # 按领域分层
│       ├── config/          # 配置（.env / 环境变量）
│       ├── llm/             # 对话模型（fast/smart/strategic 三档）+ embedding 封装
│       ├── search/          # DuckDuckGo 搜索
│       ├── scraper/         # 网页抓取（Jina Reader + goquery）
│       ├── compress/        # 内存 embedding 相似度压缩（workerpool 并发）
│       ├── workerpool/      # ★统一并发池（MAX_SCRAPER_WORKERS）
│       ├── collection/      # ★visited_urls Set + 来源去重/合并
│       ├── prompts/         # 中文 prompt 模板
│       ├── researcher/      # ★单 Agent 引擎（5 节点 Graph + ConductWithVisited）
│       ├── multiagent/      # ★多 Agent 引擎（10+ 节点状态图 + 3 循环 + checkpoint）
│       ├── deep/            # ★深度递归引擎（LambdaNode 内递归 + breadth 衰减）
│       ├── agent/           # ReAct Agent 引擎（ADK）
│       ├── store/           # ★SQLite 持久化
│       └── api/             # ★HTTP 层（Gin + WebSocket + 报告 CRUD + 前端 embed）
├── frontend/                # React 前端 (Vite + TypeScript + Tailwind)
│   └── src/{api,hooks,components}
├── docs/                    # 设计文档 + architecture.md
└── Makefile                 # dev / build / run / test 等命令
```

**核心工作流**（gpt-researcher 默认固定工作流的 Go 移植）：

```
query → 选角色(LLM) → 规划子查询(LLM)
      → 并发{搜索 → 抓取 → 内存 embedding 压缩}（WorkerPool + visited_urls 去重）
      → 撰写中文 Markdown 报告（带 [n] 引用）
全程 WebSocket 实时推送进度；报告存入 SQLite。
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
| `MAX_SCRAPER_WORKERS` | `15` | 全局抓取并发上限（WorkerPool） |
| `MAX_EMBED_WORKERS` | `4` | embedding 计算并发上限 |
| `DEEP_BREADTH` | `4` | 深度递归起始扇出数 |
| `DEEP_DEPTH` | `2` | 深度递归层数 |
| `HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `DB_PATH` | `data/z-research.db` | SQLite 路径 |

完整配置项见 [`backend/.env.example`](backend/.env.example)。

## 多智能体模式 (Multi-Agent)

新版支持多智能体状态图编排，对标 gpt-researcher 的 STORM 论文式架构。开启方法：

```bash
# .env
ENGINE_MODE=multi
ENABLE_HITL=true        # 可选：开启大纲审核回路
ENABLE_FACT_CHECK=true  # 可选：开启事实核查循环（第三个循环）
ENABLE_VISUALIZE=true   # 可选：开启可视化节点
```

或者在 WebSocket 请求中指定 `mode: "multi"`，可与其它模式动态切换。

### 工作流（含 3 个循环）

```
              ┌── revise ──┐  (循环 1: 人工反馈)
              ▼             │
START → Browser → Planner → HumanReview ── accept ──→ Researcher → Writer → FactChecker ──┐
                  ▲                                          │             │             │
                  │                                  (per section)       revise          │ accept
                  │                                  Reviewer↔Reviser      │             ▼
                  └─────────────────────────────────────────────────  ←──  ┘        Visualizer
                                                  (循环 2: 审稿修订)                   │
                                                                                       ▼
                                                          (循环 3: 事实核查)        Publisher → END
```

- **Browser**：先跑一遍单 Agent 研究，产出 initial_research 摘要供 Planner 参考
- **Planner**：LLM 出大纲（标题 + 分节列表）
- **HumanReview**（循环 1，可选）：阻塞等用户在 WebSocket 上接受/修改大纲
- **Researcher**：对每个分节并行做 (search → fetch → compress → 写初稿)
- **Reviewer ↔ Reviser**（循环 2，per section）：分节自校正（最多 `MAX_DRAFT_REVISIONS` 轮）
- **Writer**：把分节草稿 + 全局来源汇编成最终 Markdown 报告
- **FactChecker**（循环 3，可选）：**只看报告正文（intro+data+conclusion），不看 URL 不看引用**；不通过回 Writer 重写（最多 `MAX_FACT_CHECK_REVISIONS` 轮）
- **Visualizer**（可选）：生成报告元数据 + mermaid 概览
- **Publisher**：组装最终输出（报告 + 核查摘要 + 可视化 + 来源列表）

### 配置项

| 变量 | 默认 | 说明 |
|---|---|---|
| `ENGINE_MODE` | `single` | `single` / `multi` / `react` / `deep` |
| `ENABLE_HITL` | `false` | 是否需要人工审核大纲 |
| `MAX_SECTIONS` | `4` | Planner 输出的最多分节数 |
| `MAX_PLAN_REVISIONS` | `3` | 大纲最多重规划次数（循环 1 上限） |
| `MAX_DRAFT_REVISIONS` | `3` | 每个分节最多自校正轮数（循环 2 上限） |
| `ENABLE_FACT_CHECK` | `false` | 开启事实核查循环（循环 3） |
| `ENABLE_VISUALIZE` | `false` | 开启可视化节点 |
| `MAX_FACT_CHECK_REVISIONS` | `2` | 事实核查最多重写轮数（循环 3 上限） |
| `MAX_RUN_STEPS` | `80` | Eino graph 超步上限（兜底） |

### WebSocket 协议扩展

服务端在多智能体模式下会发送额外的 `human_feedback` 帧：

```json
{ "type": "human_feedback", "title": "...", "sections": ["..."], "revision": 0 }
```

客户端必须用 `human_feedback_response` 帧回复：

```json
{ "type": "human_feedback_response", "accept": true }
// 或
{ "type": "human_feedback_response", "accept": false, "notes": "请增加一节'未来展望'" }
```

### Checkpoint 持久化

多智能体模式自动启用 SQLite checkpoint 持久化（`multiagent_checkpoints` 表），用 `WithCheckPointID(taskID)` 可在中断后从断点续跑。这是 gpt-researcher 的 `MemorySaver` 没真正实现（`orchestrator.py:115` 调用 `compile()` 时没传 checkpointer）我们补上的关键差异点。

## 深度递归模式 (Deep Research)

对标 OpenAI Deep Research / gpt-researcher 的递归树，用 Eino Graph 的 **LambdaNode 内递归**实现（不开新 Graph）。

```
START → choose_role → plan_search → deep_recurse(Lambda递归) → compress → writer → END
```

`deep_recurse` 节点内部用普通 Go 函数递归：

- **breadth 逐层衰减**：每层 `breadth = max(2, parentBreadth // 2)`，避免树爆炸
- **跨层共享 `visited_urls`**：`collection.VisitedSet` 保证同一 URL 不被重复抓取
- **基于 learnings 追问**：每层用 LLM 从上层资料提炼 learnings，再生成下一层追问查询
- **叶子层**（`depth=0`）：直接调 `ConductWithVisited` 收集资料

递归结构示例（`breadth=4, depth=2`）：

```
layer 0: query (breadth=4)
  ├─ layer 1: followup_1 (breadth=2)
  │   ├─ layer 2: followup_1_1 (breadth=2, leaf)
  │   └─ layer 2: followup_1_2 (breadth=2, leaf)
  ├─ layer 1: followup_2 (breadth=2)
  ...
```

> 成本随 `breadth^depth` 增长，默认 `(4, 2)` 较温和。

### 配置项

| 变量 | 默认 | 说明 |
|---|---|---|
| `DEEP_BREADTH` | `4` | 递归起始扇出数 |
| `DEEP_DEPTH` | `2` | 递归层数（0 = 退化为叶子研究） |

前端选择「深度递归」模式时会显示 breadth/depth 配置面板，可 per-request 调整。CLI 用 `--mode deep --breadth N --depth M`。

## 全局并发控制

所有引擎共享统一的并发原语：

- **`WorkerPool`**（`internal/workerpool`）：替代原本散落的 errgroup.SetLimit / channel 信号量，修复了一个"信号量容量=抓取数导致等于不限流"的 bug
- **`MAX_SCRAPER_WORKERS=15`**：网页抓取的全局并发上限
- **`MAX_EMBED_WORKERS=4`**：embedding 计算的并发上限
- **`visited_urls Set`**（`internal/collection`）：跨子查询/章节/递归层共享的 URL 去重集合

## 后续扩展方向

- **本地知识库 RAG**：接入本地 PDF/Word，用轻量级向量库做持久化检索。
- **多用户/鉴权**：加 JWT。
