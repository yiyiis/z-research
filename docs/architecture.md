# z-research 架构说明

本文描述 z-research 全栈应用的分层结构与数据流。

## 顶层架构

```
┌─────────────────────────────────────────────────────────┐
│  浏览器 (React SPA)                                       │
│  ┌───────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │ 历史列表   │  │ 进度日志(WS)  │  │ 报告渲染+来源     │  │
│  └───────────┘  └──────────────┘  └──────────────────┘  │
│  模式切换: single / multi / react / deep                  │
└──────────────┬──────────────────────────────────────────┘
               │ WebSocket (/ws) + HTTP REST (/api/*)
               ▼
┌─────────────────────────────────────────────────────────┐
│  backend (Gin)                                           │
│  internal/api   ← 路由、WebSocket handler、报告 CRUD、SPA 托管│
│  internal/researcher ← 单 Agent 引擎 (5 节点 Graph)        │
│  internal/multiagent ← 多 Agent 引擎 (10+ 节点状态图)       │
│  internal/deep   ← 深度递归引擎 (LambdaNode 内递归)         │
│  internal/agent  ← ReAct Agent 引擎 (ADK)                  │
│  internal/store ← SQLite 持久化                           │
│  internal/{config,llm,search,scraper,compress,prompts}    │
│  internal/{workerpool,collection} ← 并发与去重基础设施       │
└─────────────────────────────────────────────────────────┘
```

## 后端分层（按领域，internal 包不对外暴露）

| 包 | 职责 | 耦合 |
|---|---|---|
| `config` | 从 `.env`/环境变量加载配置 | 无（仅 godotenv） |
| `prompts` | 中文 prompt 模板（纯函数） | 无 |
| `workerpool` | ★统一并发池（`MAX_SCRAPER_WORKERS`） | 无 |
| `collection` | ★`visited_urls` Set + 来源去重/合并 | 无 |
| `compress` | 切片 + 内存 embedding 相似度压缩（用 workerpool 并发） | 仅依赖 `embedding.Embedder` 接口 |
| `scraper` | 抓取并清洗网页正文（Jina Reader + goquery） | 无 |
| `search` | DuckDuckGo 文本搜索 | 无 |
| `llm` | 对话模型（fast/smart/strategic 三档）+ embedding 封装 | 依赖 `config` |
| `researcher` | ★单 Agent 引擎：5 节点 Graph + `ConductWithVisited` | 聚合 config/llm/search/scraper/compress/collection/workerpool |
| `multiagent` | ★多 Agent 引擎：10+ 节点状态图 + 3 循环 + checkpoint | 依赖 researcher |
| `deep` | ★深度递归引擎：5 节点 Graph + LambdaNode 内递归 | 依赖 researcher/collection/workerpool |
| `agent` | ReAct Agent 引擎（ADK `react.NewAgent`） | 依赖 llm/search/scraper |
| `store` | ★SQLite CRUD（`modernc.org/sqlite` 纯 Go，无 CGO） | 无 |
| `api` | ★Gin HTTP 层：WebSocket 研究 + 报告 CRUD + 内嵌 SPA | 依赖 `researcher.EngineIface`（接口）+ `store.Store`（接口） |

**关键解耦**：`api` 包通过接口 `researcher.EngineIface` 依赖引擎（不依赖具体 `*Engine`），
通过接口 `store.Store` 依赖存储。测试时用假引擎 + 临时 SQLite 即可覆盖 HTTP 全流程，
无需真实 LLM。四种引擎都实现同一接口，由 `pickEngine(mode)` 路由。

## 四种研究引擎

所有引擎都实现 `researcher.EngineIface.Run(ctx, query, opts, onProgress, onReportChunk)`。

### 单 Agent（`single`，默认）

5 节点 Eino `compose.Graph` 线性编排：

```
START → choose_role → plan_search → parallel_research → compression → writer → END
        (fast档)      (strategic档)   (workerpool+       (占位)       (smart档
                                      visited_urls                    流式撰写)
                                      去重)
```

### 多 Agent（`multi`）

10+ 节点状态图，含 3 个循环（对标 gpt-researcher STORM）：

```
START → browser → planner → human_review ─[循环1]─→ researcher → writer → fact_checker ─[循环3]─┐
                    ▲           │                  (per section)              │                  │
                    └─[循环1]───┘                  reviewer↔reviser           │                  ▼
                                                   ─[循环2]─              revise           visualizer
                                                                            │                  │
                                                                            ▼                  ▼
                                                                         writer           publisher → END
```

- **循环 1**（人工反馈）：`human_review` accept/revise 路由回 planner
- **循环 2**（审稿修订）：per section 的 `reviewer ↔ reviser` 自校正
- **循环 3**（事实核查）：`fact_checker` 只看报告正文不看引用，pass→visualizer / fail→writer

### 深度递归（`deep`）

5 节点 Graph，`deep_recurse` 是 LambdaNode，内部用普通 Go 函数递归（不开新 Graph）：

```
START → choose_role → plan_search → deep_recurse(Lambda递归) → compress → writer → END
```

- breadth 逐层衰减 `max(2, b//2)`
- 跨层共享 `collection.VisitedSet`（不重复抓取）
- 基于上轮 learnings 用 LLM 生成下一层追问
- 成本随 `breadth^depth` 增长

### ReAct Agent（`react`）

ADK `react.NewAgent` + 两个工具（`web_search` / `fetch_url`），LLM 自主决定调用顺序与终止。
`MaxStep=25` 兜底。

## 研究引擎数据流（以单 Agent 为例）

```
Run(query, opts, onProgress)
  │
  ├─[graph 节点1 choose_role] ChooseRole(LLM.FastChat) ──→ onProgress(StageRole)
  │
  ├─[graph 节点2 plan_search] PlanSubQueries(LLM.StrategicChatJSON) → N 子查询
  │
  ├─[graph 节点3 parallel_research] RunSubQueries
  │     ├─ errgroup 并发(限 cfg.Concurrency) 每个子查询:
  │     │    search.Search → VisitedSet.Register(去重) → workerpool 并发 FetchURL
  │     │    → compress.Compress(embedding 相似度过滤, workerpool 并发)
  │     └─ 合并带来源编号的上下文
  │
  ├─[graph 节点4 compression] 占位（每个网页已在 processSubQuery 内做 embedding 压缩）
  │
  └─[graph 节点5 writer] WriteReport(LLM.ChatStream 流式) ──→ onProgress(StageWriting)
        → 若缺"参考资料"段则补来源清单
        → 返回 FinalReport{Markdown, Sources}
```

`onProgress` 回调是 **CLI 与 HTTP 复用引擎** 的关键：
- CLI 入口（`cmd/server --cli`）：把进度打印到 stderr。
- HTTP handler（`/ws`）：把进度转发为 WebSocket `progress` 帧。

## WebSocket 协议（实时进度）

研究进度走 **WebSocket**（`GET /ws` 升级为全双工长连接），而非 SSE。

为什么不用 SSE：研究过程的 LLM 推理有几十秒静默期，SSE 建立在 HTTP 上，静默期会触发各层 idle 超时断连；
WebSocket 升级后是裸 TCP 长连接，无 HTTP idle 概念，天然保持。与 gpt-researcher 的 `/ws` 设计一致。

消息流（JSON 文本帧，用 `type` 区分）：
```
客户端 → 服务端: {"query":"研究问题"}
服务端 → 客户端: {type:"progress", stage, message}        ×N
服务端 → 客户端: {type:"sources", sources:[{n,url,title}]}
服务端 → 客户端: {type:"done", report, sources, report_id}  # 成功
服务端 → 客户端: {type:"error", message}                     # 失败
```

报告 CRUD 仍走普通 REST：`GET /api/reports`、`GET /api/reports/:id`、`DELETE /api/reports/:id`。

## 部署形态

- **开发**：后端 `:8080` + 前端 Vite `:5173`（代理 `/api`）。
- **生产**：`make build` 把 `frontend/dist` 拷贝到 `backend/internal/api/web`，
  通过 `go:embed` 编译进二进制；Gin 用 `r.NoRoute` 做 SPA fallback（未知路径回退 `index.html`），
  单二进制即可部署。

## 关键设计原则

- **动态网页用 embedding 提纯，不存向量库**：资料"即用即抛"，请求结束即释放。
- **工具层硬截断**：搜索/抓取条数由配置严格限定，成本可控。
- **规划与执行分离**：单 Agent 是确定性固定工作流；多 Agent 用状态图 + 受限循环；深度递归是有界递归（depth 严格上限）。ReAct 是唯一真正的自主循环（`MaxStep=25` 兜底）。
- **统一并发控制**：所有引擎共用 `workerpool.Pool`（`MAX_SCRAPER_WORKERS=15`），替代原本散落的 errgroup/channel 信号量。
- **跨层 URL 去重**：`collection.VisitedSet` 跨子查询/章节/递归层共享，避免重复抓取（单 Agent 内的 `ConductWithVisited` + 深度递归的跨层共享都依赖它）。
- **网络连通性由运行环境负责**：应用代码不内置代理（DDG 在墙内需系统代理/TUN/境外部署，配 `HTTP_PROXY`/`HTTPS_PROXY` 环境变量）。
