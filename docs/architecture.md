# z-research 架构说明

本文描述 z-research 全栈应用的分层结构与数据流。

## 顶层架构

```
┌─────────────────────────────────────────────────────────┐
│  浏览器 (React SPA)                                       │
│  ┌───────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │ 历史列表   │  │ 进度日志(WS)  │  │ 报告渲染+来源     │  │
│  └───────────┘  └──────────────┘  └──────────────────┘  │
└──────────────┬──────────────────────────────────────────┘
               │ WebSocket (/ws) + HTTP REST (/api/*)
               ▼
┌─────────────────────────────────────────────────────────┐
│  backend (Gin)                                           │
│  internal/api   ← 路由、SSE handler、报告 CRUD、SPA 托管   │
│  internal/researcher ← 研究引擎 (Run + progress 回调)      │
│  internal/store ← SQLite 持久化                           │
│  internal/{config,llm,search,scraper,compress,prompts}    │
└─────────────────────────────────────────────────────────┘
```

## 后端分层（按领域，internal 包不对外暴露）

| 包 | 职责 | 耦合 |
|---|---|---|
| `config` | 从 `.env`/环境变量加载配置 | 无（仅 godotenv） |
| `prompts` | 中文 prompt 模板（纯函数） | 无 |
| `compress` | 切片 + 内存 embedding 相似度压缩 | 仅依赖 `embedding.Embedder` 接口 |
| `scraper` | 抓取并清洗网页正文（goquery） | 无 |
| `search` | DuckDuckGo 文本搜索 | 无 |
| `llm` | 对话模型 + embedding 封装，`Chat`/`ChatJSON` | 依赖 `config` |
| `researcher` | ★研究编排引擎：`Researcher.Conduct` + 顶层 `Engine.Run` | 聚合 config/llm/search/scraper/compress/prompts |
| `store` | ★SQLite CRUD（`modernc.org/sqlite` 纯 Go，无 CGO） | 无 |
| `api` | ★Gin HTTP 层：SSE 研究 + 报告 CRUD + 内嵌 SPA | 依赖 `researcher.EngineIface`（接口）+ `store.Store`（接口） |

**关键解耦**：`api` 包通过接口 `researcher.EngineIface` 依赖引擎（不依赖具体 `*Engine`），
通过接口 `store.Store` 依赖存储。测试时用假引擎 + 临时 SQLite 即可覆盖 HTTP 全流程，
无需真实 LLM。

## 研究引擎数据流（`researcher.Engine.Run`）

```
Run(query, opts, onProgress)
  │
  ├─[阶段1 选角色] ChooseRole(LLM.Chat) ──→ onProgress(StageRole)
  │
  ├─[阶段2 收集资料] Researcher.Conduct
  │     ├─ planSubQueries(LLM.ChatJSON) → N 个子查询 ──→ onProgress(StagePlanning)
  │     └─ errgroup 并发(限 cfg.Concurrency) 每个子查询:
  │           search.Search → registerSource(去重) → 并发 FetchURL
  │           → compress.Compress(embedding 相似度过滤) ──→ onProgress(StageSearching/Fetching/Compressing)
  │     → 合并带来源编号的上下文
  │
  └─[阶段3 撰写报告] LLM.Chat(reportPrompt) ──→ onProgress(StageWriting)
        → 若缺"参考资料"段则补来源清单
        → 返回 FinalReport{Markdown, Sources}
```

`onProgress` 回调是 **CLI 与 HTTP/SSE 复用引擎** 的关键：
- CLI 入口（`cmd/server --cli`）：把进度打印到 stderr。
- HTTP handler（`/api/research`）：把进度转发为 SSE `progress` 事件。

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

## 关键设计原则（来自设计文档）

- **动态网页用 embedding 提纯，不存向量库**：资料"即用即抛"，请求结束即释放。
- **工具层硬截断**：搜索/抓取条数由配置严格限定，成本可控。
- **规划与执行分离**：确定性固定工作流，不做自主 ReAct 循环（保证稳定、可控）。
- **网络连通性由运行环境负责**：应用代码不内置代理（DDG 在墙内需系统代理/TUN/境外部署）。
