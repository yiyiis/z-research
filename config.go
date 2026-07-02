package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config 是 z-research 的全部可配置项。
//
// 默认值对齐 gpt-researcher 的默认配置：
//   - 子查询数 (MAX_ITERATIONS) = 3
//   - 每查询搜索条数 = 5，实际抓取网页数 = 3
//   - embedding 相似度阈值 ≈ 0.42
//   - 小文档快路径阈值 = 8000 字符
//   - 报告目标字数 = 1200
//
// 默认指向智谱 GLM 的 OpenAI 兼容端点；可通过环境变量改为任意 OpenAI 兼容服务。
type Config struct {
	APIKey   string // LLM 凭证（GLM/OpenAI/DeepSeek 等 OpenAI 兼容服务）
	LLMBase  string
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
	TotalWords           int     // 报告目标字数
	Language             string  // 报告语言
	Temperature          float64 // LLM 温度
	Concurrency          int     // 子查询并发数
}

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

		EmbedBase:   getenvDefault("EMBED_BASE_URL", defaultEmbedBase),
		EmbedModel:  getenvDefault("EMBED_MODEL", defaultEmbedModel),
		EmbedAPIKey: getenvDefault("EMBED_API_KEY", ""), // 默认回退到 LLM 的 APIKey

		MaxIterations:        getenvInt("MAX_ITERATIONS", 3),
		MaxResultsPerQuery:   getenvInt("MAX_RESULTS", 5),
		MaxScrapePerQuery:    getenvInt("MAX_SCRAPE", 3),
		SimilarityThreshold:  getenvFloat("SIMILARITY_THRESHOLD", 0.42),
		CompressionThreshold: getenvInt("COMPRESSION_THRESHOLD", 8000),
		TotalWords:           getenvInt("TOTAL_WORDS", 1200),
		Language:             getenvDefault("LANGUAGE", "zh"),
		Temperature:          getenvFloat("TEMPERATURE", 0.35),
		Concurrency:          getenvInt("CONCURRENCY", 3),
	}

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("缺少必需的环境变量 ZHIPU_API_KEY（请在 .env 中设置智谱 GLM 的 API Key）")
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
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
