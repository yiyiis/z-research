// Package llm — retry.go 实现 LLM 调用的错误分类 + 指数退避重试。
//
// 为什么需要：LLM API（尤其是国内代理/自建服务）常见的瞬时故障——
//   - 网络抖动（EOF / connection reset / context deadline）
//   - 限流（429 Too Many Requests）
//   - 服务端临时错误（500/502/503/504）
//
// 这些重试一次往往就成功。但鉴权失败（401）、参数错误（400）重试无意义，
// 必须立即失败并把清晰错误抛给调用方。
//
// 分类策略：字符串匹配（OpenAI 兼容 API 的错误都带状态码或 HTTP 语义）。
// 不依赖具体错误类型——不同 SDK 错误类型不同，字符串是最稳的通用判据。
package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// ErrorKind 是 LLM 错误的分类。
type ErrorKind int

const (
	// ErrUnknown 未知错误，保守起见不重试（避免放大不可预期的故障）。
	ErrUnknown ErrorKind = iota
	// ErrTransient 瞬时错误：超时、429、5xx、连接重置。应该重试。
	ErrTransient
	// ErrAuth 鉴权错误：401/403（API key 错误或无权限）。重试无意义。
	ErrAuth
	// ErrClient 客户端错误：400（请求参数错）、模型不存在等。重试无意义。
	ErrClient
)

// ClassifyError 把一个 error 分类。
//
// 优先用 errors.Is 识别标准库的 context 错误；
// 再用字符串匹配识别 OpenAI 兼容 API 的常见错误（状态码、HTTP 语义）。
// 分类不全的部分保守归到 ErrUnknown（不重试）。
func ClassifyError(err error) ErrorKind {
	if err == nil {
		return ErrUnknown
	}
	// context 超时/取消：可重试（往往是上游临时慢）。
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTransient
	}
	// context 取消（用户主动断开）：不重试，直接返回。
	// 单独判断不走 transient，避免取消后还傻等重试。
	if errors.Is(err, context.Canceled) {
		return ErrUnknown
	}

	s := strings.ToLower(err.Error())

	// 鉴权类：不重试。
	if containsAny(s, "401", "403", "unauthorized", "forbidden", "invalid api key", "invalid_api_key", "authentication") {
		return ErrAuth
	}

	// 客户端错误类（4xx，排除 401/403/429）：不重试。
	if containsAny(s, "400", "bad request", "model not found", "invalid model", "context length") {
		return ErrClient
	}

	// 瞬时错误：超时 / 429 / 5xx / 连接重置 / EOF。
	// 包含各家服务商特有的过载状态码：
	//   - 429: OpenAI/通用限流
	//   - 500/502/503/504: 通用服务端错误
	//   - 529: MiniMax 特有的"服务集群负载较高"(overloaded)
	//   - 530: Cloudflare 特有
	if containsAny(s,
		"timeout", "timed out", "context deadline exceeded",
		"429", "rate limit", "rate_limit", "too many requests",
		"500", "502", "503", "504", "529", "530",
		"internal server error", "bad gateway", "service unavailable", "gateway timeout",
		"overload", "overloaded", "负载较高", "集群", "稍后重试",
		"connection reset", "connection refused", "eof", "broken pipe", "temporary failure",
	) {
		return ErrTransient
	}

	return ErrUnknown
}

// containsAny 判断 s 是否包含任意子串（用于简洁的错误关键字匹配）。
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// RetryConfig 控制重试行为。
type RetryConfig struct {
	MaxRetries int           // 最大重试次数（不含首次调用），0 表示不重试
	BaseDelay  time.Duration // 首次退避基准（实际延迟 = BaseDelay * 2^attempt + jitter）
}

// DefaultRetryConfig 是合理默认值：3 次重试，基准 1s。
// 总耗时上限约 1+2+4 = 7 秒（不含 jitter），可接受。
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{MaxRetries: 3, BaseDelay: time.Second}
}

// withRetry 执行 fn，对瞬时错误按指数退避重试。
//
// 泛型版本，适用于返回 (T, error) 的任何调用。
// 不可重试错误（Auth/Client/Unknown）立即返回。
// 每次重试前检查 ctx，被取消则提前返回。
// onRetry 在每次重试前被调用（attempt 从 1 开始），用于上报进度。
func withRetry[T any](ctx context.Context, cfg RetryConfig, onRetry func(attempt int, err error), fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	maxAttempts := cfg.MaxRetries + 1 // 含首次
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// 检查 ctx（重试前避免无意义请求）。
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err

		kind := ClassifyError(err)
		if kind != ErrTransient {
			// 非瞬时错误：立即返回，不重试。
			return zero, err
		}
		// 瞬时错误：若已是最后一次尝试，返回。
		if attempt >= maxAttempts {
			break
		}
		// 上报即将重试。
		if onRetry != nil {
			onRetry(attempt, err)
		}
		// 指数退避 + jitter（避免雪崩：多个客户端同步重试）。
		delay := cfg.BaseDelay * (1 << (attempt - 1)) // 1s, 2s, 4s ...
		jitter := time.Duration(rand.Int63n(int64(cfg.BaseDelay)))
		select {
		case <-time.After(delay + jitter):
		case <-ctx.Done():
			return zero, ctx.Err()
		}
	}
	return zero, fmt.Errorf("重试 %d 次后仍失败: %w", cfg.MaxRetries, lastErr)
}
