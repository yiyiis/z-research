// Package llm 封装一个对话模型 (ChatModel) 和一个 embedding 模型 (Embedder)，
// 并提供 Chat / ChatJSON 两个便捷调用。
//
// 底层均使用 eino-ext 的 OpenAI 兼容实现，因此可指向
// GLM / DeepSeek / OpenAI / 任意 OpenAI 兼容服务（包括自建 Ollama 的 /v1）。
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	openaiembed "github.com/cloudwego/eino-ext/components/embedding/openai"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"z-research/backend/internal/config"
)

// LLM 封装对话模型与 embedding 模型。
type LLM struct {
	chat  model.ToolCallingChatModel
	embed embedding.Embedder
}

// NewLLM 根据配置创建对话模型与 embedding 模型。
//
// 关键：必须给底层 HTTP client 设超时。GLM 等服务偶发挂起（请求发出后不响应），
// 若无超时，Go 默认 http.Client 会无限等待，导致整个研究流程死等、表面"卡住无反应"。
func NewLLM(ctx context.Context, cfg *config.Config) (*LLM, error) {
	temperature := float32(cfg.Temperature)
	// LLM 单次调用超时（思考模型写报告可能较慢，设 3 分钟）。
	chatTimeout := cfg.LLMTimeout
	chatHTTPClient := &http.Client{Timeout: chatTimeout}

	chatModel, err := openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
		BaseURL:     cfg.LLMBase,
		APIKey:      cfg.APIKey,
		Model:       cfg.LLMModel,
		Temperature: &temperature,
		Timeout:     chatTimeout,
		HTTPClient:  chatHTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("创建对话模型失败: %w", err)
	}

	// Embedding 的 API Key 优先用 EMBED_API_KEY，未设则回退到 LLM 的 APIKey。
	embedKey := cfg.EmbedAPIKey
	if embedKey == "" {
		embedKey = cfg.APIKey
	}
	// Embedding 单次超时（通常较快，设 60s）。
	embedHTTPClient := &http.Client{Timeout: cfg.EmbedTimeout}
	embedder, err := openaiembed.NewEmbedder(ctx, &openaiembed.EmbeddingConfig{
		BaseURL:    cfg.EmbedBase,
		APIKey:     embedKey,
		Model:      cfg.EmbedModel,
		Timeout:    cfg.EmbedTimeout,
		HTTPClient: embedHTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 embedding 模型失败: %w", err)
	}

	return &LLM{chat: chatModel, embed: embedder}, nil
}

// Embedder 返回底层 embedding 模型，供压缩器使用。
func (l *LLM) Embedder() embedding.Embedder { return l.embed }

// Chat 进行一次普通对话，返回助手消息的纯文本内容。
// system 为空时只发送 user 消息。
func (l *LLM) Chat(ctx context.Context, system, user string) (string, error) {
	msgs := make([]*schema.Message, 0, 2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, schema.SystemMessage(system))
	}
	msgs = append(msgs, schema.UserMessage(user))

	resp, err := l.chat.Generate(ctx, msgs)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// ChatStream 以流式方式生成回复。
//
// 它返回一个只读 channel，逐块推送生成中的文本（每块是一段 token），
// channel 关闭表示生成结束（正常 EOF）或出错（通过 errPtr 返回）。
//
// 流式的核心价值（对齐 gpt-researcher 的 stream=True）：
//   - LLM 边生成边吐，连接持续有数据流动，不会因"等完整大响应"被判 idle 超时；
//   - 调用方（如 WebSocket handler）可把每个块实时推给前端，用户看到报告逐字生成。
//
// 用法：
//
//	ch, err := l.ChatStream(ctx, sys, user)
//	for chunk := range ch { ... }
func (l *LLM) ChatStream(ctx context.Context, system, user string) (<-chan string, error) {
	msgs := make([]*schema.Message, 0, 2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, schema.SystemMessage(system))
	}
	msgs = append(msgs, schema.UserMessage(user))

	reader, err := l.chat.Stream(ctx, msgs)
	if err != nil {
		return nil, err
	}

	ch := make(chan string, 16)
	go func() {
		defer reader.Close()
		defer close(ch)
		for {
			msg, err := reader.Recv()
			if err != nil {
				// io.EOF 是正常结束；其他错误无法通过 channel 传递，
				// 这里直接结束（调用方通过 chunk 拼接得到部分结果）。
				return
			}
			if msg.Content != "" {
				select {
				case ch <- msg.Content:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

// ChatJSON 进行一次对话并要求模型返回合法 JSON。
// 它在 system 提示中追加"仅输出 JSON"的指令，并尽力从模型回复中提取首个
// JSON 数组/对象（容忍模型偶尔输出的多余文字）。
func (l *LLM) ChatJSON(ctx context.Context, system, user string, out any) error {
	if strings.TrimSpace(system) != "" {
		system = system + "\n\n重要：只输出合法的 JSON，不要包含任何解释性文字或 markdown 代码块标记。"
	} else {
		system = "只输出合法的 JSON，不要包含任何解释性文字或 markdown 代码块标记。"
	}

	content, err := l.Chat(ctx, system, user)
	if err != nil {
		return fmt.Errorf("ChatJSON 调用失败: %w", err)
	}

	cleaned := ExtractJSON(content)
	if err := json.Unmarshal([]byte(cleaned), out); err != nil {
		return fmt.Errorf("ChatJSON 解析失败: %w (原始输出前200字符: %s)", err, Truncate(content, 200))
	}
	return nil
}

// ExtractJSON 从可能包含多余文字的字符串里截取第一个 JSON 值（数组或对象）。
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	// 去掉可能的 ```json ... ``` 代码块包裹。
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	startArr := strings.Index(s, "[")
	startObj := strings.Index(s, "{")
	start := -1
	switch {
	case startArr == -1 && startObj == -1:
		return s
	case startArr == -1:
		start = startObj
	case startObj == -1:
		start = startArr
	default:
		if startArr < startObj {
			start = startArr
		} else {
			start = startObj
		}
	}

	// 从 start 开始按括号配对截取到对应闭合位置。
	depth := 0
	inStr := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' {
			escape = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

// Truncate 截断字符串到 n 字符。
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
