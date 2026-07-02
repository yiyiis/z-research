# z-research

一个基于 [Eino](https://github.com/cloudwego/eino) 的**简易研究 Agent**（Go 版 gpt-researcher）。
给定一个研究问题，它会自动联网检索、抓取网页、压缩提炼资料，并生成一份**带来源引用的中文 Markdown 报告**。

> 本项目是参考 `Golang-爬虫脚本-Eino-集成.md` 的设计理念与 [`gpt-researcher`](https://github.com/assafelovic/gpt-researcher) 的默认执行流，用 Go + Eino 实现的**最简易版本（固定工作流 Fixed Workflow）**。

## 架构

忠实移植 gpt-researcher 的**默认模式**（固定工作流，非 ReAct）：

```
用户 query
  → [1] choose_agent (LLM)        选择领域专家角色
  → [2] plan_sub_queries (LLM)    拆成 N 个子查询 (默认 N=3)
  → [3] 对每个子查询并发 (errgroup):
           DuckDuckGo 搜索 → 取 Top-K URL → 并发抓取 (goquery)
           → 内存 embedding 相似度压缩 (不存向量库)
  → [4] 合并所有压缩后的上下文 (带来源编号)
  → [5] write_report (LLM)        生成中文 Markdown 报告，正文 [n] 引用 + 参考资料
```

关键设计（来自 .md 文档）：
- **动态网页用 embedding 提纯，不存向量库** —— 资料"即用即抛"，请求结束即释放。
- **工具层硬截断** —— 搜索/抓取条数由配置严格限定，成本可控、不会失控。
- **规划与执行分离** —— 确定性工作流保证稳定，不做自主 ReAct 循环。

## 技术栈

| 能力 | 组件 |
|---|---|
| 对话模型 | `eino-ext/components/model/openai`（OpenAI 兼容，默认指向智谱 GLM） |
| Embedding | OpenAI 兼容（默认 GLM `embedding-3`）**或自建 Ollama**（`nomic-embed-text` 等） |
| 搜索 | `eino-ext/components/tool/duckduckgo/v2`（免费，无需 API Key） |
| 网页抓取 | `net/http` + `PuerkitoBio/goquery` |
| 编排 | 标准库 `errgroup`（并发子查询） |

### Embedding 后端

统一走 **OpenAI 兼容接口**（`/v1/embeddings`）：GLM / DeepSeek / OpenAI / 自建 Ollama 都接同一套代码，无需为不同服务写适配器。LLM 与 Embedding 可**解耦部署**——例如 LLM 用云端 GLM，Embedding 用自建 Ollama。


## 快速开始

### 1. 配置

```bash
cp .env.example .env
# 编辑 .env，填入智谱 GLM 的 API Key（必填）
```

默认指向智谱 GLM 的 OpenAI 兼容端点；也可改 `LLM_BASE_URL` / `EMBED_BASE_URL` 接入 DeepSeek / OpenAI / 任意代理。

### 用自建 Ollama 做 Embedding（LLM 仍用 GLM）

如果你的 Ollama 部署在公网服务器（已验证可用，如 `http://43.138.247.132:11434`，模型 `nomic-embed-text`，768 维），只需把 `EMBED_BASE_URL` 指向它的 OpenAI 兼容层，**不改任何代码**：

```bash
# LLM 继续用云端 GLM
ZHIPU_API_KEY=你的GLM密钥
LLM_BASE_URL=https://open.bigmodel.cn/api/coding/paas/v4
LLM_MODEL=glm-4-plus

# Embedding 改用自建 Ollama（OpenAI 兼容层 /v1/embeddings）
EMBED_BASE_URL=http://43.138.247.132:11434/v1
EMBED_MODEL=nomic-embed-text
EMBED_API_KEY=ollama   # Ollama 不校验 key，填任意非空值即可（OpenAI 客户端要求非空）
```

Ollama 自带 OpenAI 兼容层，与 GLM/DeepSeek 走同一套代码路径，所以无需为它写专用适配器。

### 2. 运行

```bash
go mod tidy
go run . "2026 年固态电池降本的最新进展"
```

报告会打印到终端，并落盘到 `outputs/report-<时间戳>.md`。

### 示例输出结构

```markdown
# 固态电池降本进展

...正文，引用处标注 [1] [2]...

## 参考资料
1. 标题 — https://...
2. 标题 — https://...
```

## 可调参数（环境变量）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ZHIPU_API_KEY` | （必填） | LLM 凭证（GLM/OpenAI/DeepSeek 等） |
| `LLM_BASE_URL` | `https://open.bigmodel.cn/api/paas/v4` | LLM 的 OpenAI 兼容端点 |
| `LLM_MODEL` | `glm-4-plus` | 对话模型 |
| `EMBED_BASE_URL` | `https://open.bigmodel.cn/api/paas/v4` | embedding 的 OpenAI 兼容端点（可指向自建 Ollama `/v1`） |
| `EMBED_MODEL` | `embedding-3` | embedding 模型（Ollama 用 `nomic-embed-text` 等） |
| `EMBED_API_KEY` | （空） | embedding 的 key；留空则回退到 `ZHIPU_API_KEY`（Ollama 填任意非空值） |
| `MAX_ITERATIONS` | `3` | 子查询数量（研究广度） |
| `MAX_RESULTS` | `5` | 每子查询搜索条数 |
| `MAX_SCRAPE` | `3` | 每子查询实际抓取网页数 |
| `SIMILARITY_THRESHOLD` | `0.42` | embedding 过滤阈值（越高越严格） |
| `COMPRESSION_THRESHOLD` | `8000` | 总字符低于此值则跳过压缩（快路径） |
| `TOTAL_WORDS` | `1200` | 报告目标字数 |
| `LANGUAGE` | `zh` | 报告语言 |
| `CONCURRENCY` | `3` | 子查询并发数 |

## 目录结构

```
z-research/
├── main.go          # CLI 入口：query → 角色 → Conduct → 撰写报告 → 落盘
├── config.go        # 配置加载（.env / 环境变量）
├── llm.go           # 对话模型 + embedding 封装；Chat / ChatJSON
├── search.go        # DuckDuckGo 搜索 → []SearchResult
├── scraper.go       # 抓取网页，goquery 清洗正文
├── compress.go      # 切片 + 内存 embedding 相似度压缩 + 余弦相似度
├── researcher.go    # 研究编排器：plan → 并发{search,fetch,compress} → 合并
├── prompts.go       # 全部中文 Prompt 模板
├── log.go           # 进度日志
└── *_test.go        # 单元测试 + DuckDuckGo 联网测试
```

## 测试

```bash
go test ./...                 # 含联网搜索测试（需网络，无网络会自动跳过）
go test -run 'TestSplit|TestCosine|TestExtractJSON' -v   # 纯单元测试，无需网络
```

## 后续扩展方向

当前是**第一档：固定工作流**。参考 .md 文档的"三挡"设计，后续可在此基础上扩展：

- **第二档 · 深度研究 (Deep Research)**：递归树（breadth/depth），每层基于上轮 learnings 生成更刁钻的追问。
- **第三档 · 多智能体 (Multi-Agent)**：增加 Reviewer 审核回路，对报告草稿挑刺打回重做。
- **本地知识库 RAG**：接入本地 PDF/Word，用切片 + 轻量级向量库（如 sqlite-vec）做持久化检索。
- **ReAct Agent**：把固定工作流的"搜索"节点替换为带工具的 ReAct 循环，赋予动态纠错能力。

> 区别说明：**动态网页**用 embedding/LLM 提纯（即用即抛）；**本地知识库**才用传统 RAG（切片+向量库，一次入库长期检索）。两者在工具层对 Agent 暴露统一接口。
