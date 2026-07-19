// Package config 定义 z-research 的配置结构与环境变量加载。
//
// 默认值对齐 gpt-researcher 的默认配置：
//   - 子查询数 (MAX_ITERATIONS) = 3
//   - 每查询搜索条数 = 5，实际抓取网页数 = 3
//   - embedding 相似度阈值 ≈ 0.42
//   - 小文档快路径阈值 = 8000 字符
//   - 报告目标字数 = 1200
//
// 默认指向智谱 GLM 的 OpenAI 兼容端点；可通过环境变量改为任意 OpenAI 兼容服务。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config 是 z-research 的全部可配置项。
type Config struct {
	APIKey  string // LLM 凭证（GLM/OpenAI/DeepSeek 等 OpenAI 兼容服务）
	LLMBase string

	// --- 三档模型（对齐 gpt-researcher 的 fast/smart/strategic）---
	// fast：选角色、JSON 结构化输出（planner/reviser/reviewer）等小任务，要求快。
	// smart：主报告撰写（写正文、写章节），质量优先。
	// strategic：深度规划、拆子主题（生成大纲/子查询），决定研究方向的杠杆点。
	// 三档共享同一个 LLMBase 和 APIKey，只是模型名不同。
	FastLLMModel      string // 默认 = LLMModel
	SmartLLMModel     string // 默认 = LLMModel
	StrategicLLMModel string // 默认 = LLMModel
	// LLMModel 保留为兜底默认值（三档任一为空时回退到它）。
	LLMModel string

	// --- Embedding 配置（可与 LLM 不同来源，例如 LLM 用 GLM、Embedding 用自建 Ollama）---
	// 统一走 OpenAI 兼容接口：GLM / DeepSeek / OpenAI / 自建 Ollama 的 /v1/embeddings 都接同一套代码。
	EmbedBase   string
	EmbedModel  string
	EmbedAPIKey string // 留空则回退到 LLM 的 APIKey（Ollama 等 keyless 服务可留空）

	MaxIterations        int     // 子查询数量
	MaxResultsPerQuery   int     // 每子查询搜索结果条数
	MaxScrapePerQuery    int     // 每子查询实际抓取的网页数
	SimilarityThreshold  float64 // embedding 相似度过滤阈值
	CompressionThreshold int     // 总字符低于此值则跳过压缩
	TotalWords           int     // 报告目标字数（简报）
	Language             string  // 报告语言
	// SmartTokenLimit 是 smart 档（写报告）的 max_tokens 上限。
	// 思考模型（MiniMax-M3 等）若不设，走 API 默认值（常只有 4096），
	// 思考过程吃掉一大半后正文被截断。设大值（如 8192）避免报告写到一半断掉。
	SmartTokenLimit int
	Temperature     float64 // LLM 温度
	Concurrency     int     // 子查询并发数

	// --- 全局并发上限（面试话术关键词）---
	// MaxScraperWorkers 是网页抓取的统一并发上限，对应 env MAX_SCRAPER_WORKERS。
	// 所有引擎（single/multi/deep）的抓取都走 workerpool.New(MaxScraperWorkers)，
	// 替代原本散落在 researcher.go 里的裸 channel 信号量。
	// 默认 15，对齐面试话术里的 MAX_SCRAPER_WORKERS = 15。
	MaxScraperWorkers int
	// MaxEmbedWorkers 是 embedding 计算的并发上限（compress 包内部 workerpool）。
	// 默认 4，对齐原硬编码 embedConcurrency。
	MaxEmbedWorkers int

	// --- 深度递归模式（deep）参数 ---
	// DeepBreadth 是递归起始的子查询扇出数；每层按 max(2, breadth//2) 衰减。
	// DeepDepth 是递归层数（depth=0 时退化为叶子研究，即普通 Conduct）。
	// 成本随 breadth^depth 增长，默认 (4, 2) 较为温和。
	DeepBreadth int
	DeepDepth   int

	// --- 详细报告（多轮拆分）参数 ---
	// MaxSections 复用多智能体的同名字段（默认 4）。
	WordsPerSection int // 详细报告每章目标字数（默认 800）

	// --- 抓取策略 ---
	// ScraperStrategy: "auto"（默认，先 Jina 后 goquery）/ "jina" / "direct"。
	// Jina 宕机或被墙时改 "direct" 可避免每个 URL 等待超时。
	ScraperStrategy string

	// --- 超时（防 LLM/embedding 服务端挂起导致死等）---
	LLMTimeout   time.Duration // LLM 单次调用超时（思考模型写长报告较慢，默认 10 分钟）
	EmbedTimeout time.Duration // Embedding 单次调用超时（默认 60s）

	// --- 服务端配置 ---
	HTTPAddr   string // HTTP 监听地址，如 ":8080"
	DBPath     string // SQLite 文件路径
	HTTPProxy  string // HTTP_PROXY，主进程启动时同步到 os.Setenv 让 http.Transport 走代理
	HTTPSProxy string // HTTPS_PROXY，同上

	// --- 多智能体配置（参考 gpt-researcher 的 STORM 架构）---
	// 这些字段只在 Mode=ModeMulti 时生效；单 Agent 流程忽略。

	// Mode 是 Engine 选择的入口："single"（默认，单 Agent）
	// 或 "multi"（多智能体状态图编排）。可在请求级用
	// researcher.Options.Mode 覆盖。
	Mode string

	// EnableHITL 开启 Human-in-the-loop 大纲审核。开启后
	// Planner 完成后会阻塞等待用户在 WebSocket 上提交接受
	// 或修改意见（直到 MaxPlanRevisions 次或 accept）。
	// 关闭时自动 accept，简化调试。
	EnableHITL bool

	// MaxSections 限制 Planner 输出的分节数（对应 gpt-researcher
	// 的 max_sections）。多智能体流程每个 section 都会触发一次
	// 子图（reviewer↔reviser），值越大跑得越慢。
	MaxSections int

	// MaxPlanRevisions 限制 Human 重规划次数。超过后强制
	// accept（避免死循环，对应 gpt-researcher 的
	// DEFAULT_MAX_PLAN_REVISIONS=3）。
	MaxPlanRevisions int

	// MaxDraftRevisions 限制单 section 的 Reviewer/Reviser
	// 自校正轮数。超过后强制接受当前草稿（避免 reviewer
	// 永远不满意）。
	MaxDraftRevisions int

	// --- 多智能体：第三个循环（事实核查 + 可视化）---
	// EnableFactCheck 开启后，writer 之后会插入 fact_checker 节点，
	// 对报告正文（intro+data+conclusion，不看 URL/引用）做事实性校验。
	// 不通过则路由回 writer 重写（最多 MaxFactCheckRevisions 轮）。
	EnableFactCheck bool
	// EnableVisualize 开启后，核查通过的报告会经过 visualizer 节点，
	// 生成报告元数据（分节数/来源数/字数/核查摘要）+ 可选 mermaid 概览。
	EnableVisualize bool
	// MaxFactCheckRevisions 限制 fact_checker → writer 的重写轮数。
	MaxFactCheckRevisions int

	// MaxRunSteps Eino graph 的总超步上限（Pregel 引擎
	// 兜底）。建议设为 (MaxSections+2) * (MaxDraftRevisions+1)
	// + 一些常数 headroom。
	MaxRunSteps int
}

// Engine mode constants. Used in both Config.Mode and
// Options.Mode. Single-mode is the default and matches the
// behavior of z-research before the multi-agent refactor.
const (
	ModeSingle = "single"
	ModeMulti  = "multi"
	ModeReact  = "react"
	ModeDeep   = "deep"
)

// 默认端点与模型（OpenAI 兼容）。
const (
	defaultLLMBase    = "https://open.bigmodel.cn/api/paas/v4"
	defaultLLMModel   = "glm-4-plus"
	defaultEmbedBase  = "https://open.bigmodel.cn/api/paas/v4"
	defaultEmbedModel = "embedding-3"
)

// LoadConfig 从 .env 文件（若存在）和环境变量加载配置，缺失项使用默认值。
// 如果缺少必需的 LLM API Key 会返回错误。
func LoadConfig() (*Config, error) {
	// godotenv 会静默忽略不存在的 .env，不覆盖已设置的环境变量。
	_ = godotenv.Load()

	cfg := &Config{
		APIKey:   os.Getenv("ZHIPU_API_KEY"),
		LLMBase:  getenvDefault("LLM_BASE_URL", defaultLLMBase),
		LLMModel: getenvDefault("LLM_MODEL", defaultLLMModel),
		// 三档模型：优先读各自的 env，为空则回退到 LLMModel（单档兼容）。
		FastLLMModel:      getenvDefault("FAST_LLM_MODEL", ""),
		SmartLLMModel:     getenvDefault("SMART_LLM_MODEL", ""),
		StrategicLLMModel: getenvDefault("STRATEGIC_LLM_MODEL", ""),

		EmbedBase:   getenvDefault("EMBED_BASE_URL", defaultEmbedBase),
		EmbedModel:  getenvDefault("EMBED_MODEL", defaultEmbedModel),
		EmbedAPIKey: getenvDefault("EMBED_API_KEY", ""), // 默认回退到 LLM 的 APIKey

		MaxIterations:        getenvInt("MAX_ITERATIONS", 3),
		MaxResultsPerQuery:   getenvInt("MAX_RESULTS", 5),
		MaxScrapePerQuery:    getenvInt("MAX_SCRAPE", 3),
		SimilarityThreshold:  getenvFloat("SIMILARITY_THRESHOLD", 0.42),
		CompressionThreshold: getenvInt("COMPRESSION_THRESHOLD", 8000),
		TotalWords:           getenvInt("TOTAL_WORDS", 1200),
		SmartTokenLimit:      getenvInt("SMART_TOKEN_LIMIT", 8192),
		WordsPerSection:      getenvInt("WORDS_PER_SECTION", 800),
		ScraperStrategy:      getenvDefault("SCRAPER_STRATEGY", "auto"),
		Language:             getenvDefault("LANGUAGE", "zh"),
		Temperature:          getenvFloat("TEMPERATURE", 0.35),
		Concurrency:          getenvInt("CONCURRENCY", 3),

		// 全局并发上限（面试话术 MAX_SCRAPER_WORKERS = 15）。
		MaxScraperWorkers: getenvInt("MAX_SCRAPER_WORKERS", 15),
		MaxEmbedWorkers:   getenvInt("MAX_EMBED_WORKERS", 4),

		// 深度递归模式默认参数（温和：breadth=4, depth=2）。
		DeepBreadth: getenvInt("DEEP_BREADTH", 4),
		DeepDepth:   getenvInt("DEEP_DEPTH", 2),

		// 超时：LLM 默认 10 分钟（思考模型如 glm-5.1 写长报告可能要数分钟），Embedding 默认 60s。
		// 用秒数配置（LLM_TIMEOUT_SECONDS / EMBED_TIMEOUT_SECONDS）。
		LLMTimeout:   time.Duration(getenvInt("LLM_TIMEOUT_SECONDS", 600)) * time.Second,
		EmbedTimeout: time.Duration(getenvInt("EMBED_TIMEOUT_SECONDS", 60)) * time.Second,

		HTTPAddr:   getenvDefault("HTTP_ADDR", ":8080"),
		DBPath:     getenvDefault("DB_PATH", "data/z-research.db"),
		HTTPProxy:  getenvDefault("HTTP_PROXY", ""),
		HTTPSProxy: getenvDefault("HTTPS_PROXY", ""),

		// --- 多智能体默认 ---
		Mode: getenvDefault("ENGINE_MODE", ModeSingle),

		EnableHITL: getenvBool("ENABLE_HITL", false),

		MaxSections:       getenvInt("MAX_SECTIONS", 4),
		MaxPlanRevisions:  getenvInt("MAX_PLAN_REVISIONS", 3),
		MaxDraftRevisions: getenvInt("MAX_DRAFT_REVISIONS", 3),

		// 多智能体：第三个循环（fact_checker + visualizer）。
		EnableFactCheck:       getenvBool("ENABLE_FACT_CHECK", false),
		EnableVisualize:       getenvBool("ENABLE_VISUALIZE", false),
		MaxFactCheckRevisions: getenvInt("MAX_FACT_CHECK_REVISIONS", 2),

		// Eino MaxRunSteps 默认 = node 数 + 10。
		// 多智能体外层图 ~6 节点 + 2 hitl 节点，内层 ~3 节点
		// × MaxSections 个并行子图。粗估 80 步足够且能兜底。
		MaxRunSteps: getenvInt("MAX_RUN_STEPS", 80),
	}

	// ENGINE_MODE 放宽：允许 single/multi/react/deep。
	// react/deep 在默认配置层不做强制校验，由 api.pickEngine 在运行时
	// 根据是否成功构造决定可用性（构造失败会降级到 single）。
	switch cfg.Mode {
	case ModeSingle, ModeMulti, ModeReact, ModeDeep:
		// ok
	default:
		return nil, fmt.Errorf("ENGINE_MODE 必须是 %q/%q/%q/%q，got %q",
			ModeSingle, ModeMulti, ModeReact, ModeDeep, cfg.Mode)
	}

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("缺少必需的环境变量 ZHIPU_API_KEY（请在 .env 中设置智谱 GLM 的 API Key）")
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.MaxScraperWorkers < 1 {
		cfg.MaxScraperWorkers = 15
	}
	if cfg.MaxEmbedWorkers < 1 {
		cfg.MaxEmbedWorkers = 4
	}
	if cfg.DeepBreadth < 1 {
		cfg.DeepBreadth = 4
	}
	if cfg.DeepDepth < 0 {
		cfg.DeepDepth = 2
	}
	// 三档模型回退：任一为空则用 LLMModel（保持单档配置的兼容性）。
	if cfg.FastLLMModel == "" {
		cfg.FastLLMModel = cfg.LLMModel
	}
	if cfg.SmartLLMModel == "" {
		cfg.SmartLLMModel = cfg.LLMModel
	}
	if cfg.StrategicLLMModel == "" {
		cfg.StrategicLLMModel = cfg.LLMModel
	}
	return cfg, nil
}

func getenvDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvFloat(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getenvBool(key string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
