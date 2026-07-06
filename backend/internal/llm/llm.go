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
	"regexp"
	"strings"

	openaiembed "github.com/cloudwego/eino-ext/components/embedding/openai"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"z-research/backend/internal/config"
)

// LLM 封装三档对话模型（fast/smart/strategic）与 embedding 模型。
//
// 对齐 gpt-researcher 的三档策略：
//   - fast：选角色、JSON 结构化输出（planner/reviser/reviewer）等小任务，要求快。
//   - smart：主报告撰写（写正文/章节），质量优先。
//   - strategic：深度规划、拆子主题（生成大纲/子查询），决定研究方向的杠杆点。
//
// 三档共享同一个 BaseURL/APIKey，只是模型名不同（见 config 的 FastLLMModel 等）。
type LLM struct {
	fast      model.ToolCallingChatModel
	smart     model.ToolCallingChatModel
	strategic model.ToolCallingChatModel
	embed     embedding.Embedder
}

// newChatModel 构建一个 OpenAI 兼容的对话模型（指定模型名）。
// maxTokens<=0 时不设该参数（走 API 默认）。
func newChatModel(ctx context.Context, cfg *config.Config, modelName string, maxTokens int) (model.ToolCallingChatModel, error) {
	temperature := float32(cfg.Temperature)
	chatTimeout := cfg.LLMTimeout
	chatHTTPClient := &http.Client{Timeout: chatTimeout}
	c := &openaimodel.ChatModelConfig{
		BaseURL:     cfg.LLMBase,
		APIKey:      cfg.APIKey,
		Model:       modelName,
		Temperature: &temperature,
		Timeout:     chatTimeout,
		HTTPClient:  chatHTTPClient,
	}
	if maxTokens > 0 {
		c.MaxTokens = &maxTokens
	}
	return openaimodel.NewChatModel(ctx, c)
}

// NewLLM 根据配置创建三档对话模型与 embedding 模型。
//
// 关键：必须给底层 HTTP client 设超时。GLM 等服务偶发挂起（请求发出后不响应），
// 若无超时，Go 默认 http.Client 会无限等待，导致整个研究流程死等、表面"卡住无反应"。
func NewLLM(ctx context.Context, cfg *config.Config) (*LLM, error) {
	// fast/strategic 用于短输出（选角色、JSON、规划），max_tokens 给小值足够。
	// smart 用于写报告（长文），用 SmartTokenLimit（默认 8192）防截断。
	fast, err := newChatModel(ctx, cfg, cfg.FastLLMModel, 4096)
	if err != nil {
		return nil, fmt.Errorf("创建 fast 模型失败: %w", err)
	}
	smart, err := newChatModel(ctx, cfg, cfg.SmartLLMModel, cfg.SmartTokenLimit)
	if err != nil {
		return nil, fmt.Errorf("创建 smart 模型失败: %w", err)
	}
	strategic, err := newChatModel(ctx, cfg, cfg.StrategicLLMModel, 4096)
	if err != nil {
		return nil, fmt.Errorf("创建 strategic 模型失败: %w", err)
	}

	// Embedding 的 API Key 优先用 EMBED_API_KEY，未设则回退到 LLM 的 APIKey。
	embedKey := cfg.EmbedAPIKey
	if embedKey == "" {
		embedKey = cfg.APIKey
	}
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

	return &LLM{fast: fast, smart: smart, strategic: strategic, embed: embedder}, nil
}

// Embedder 返回底层 embedding 模型，供压缩器使用。
func (l *LLM) Embedder() embedding.Embedder { return l.embed }

// chatWith 用指定档位的模型进行一次普通对话。
func (l *LLM) chatWith(ctx context.Context, m model.ToolCallingChatModel, system, user string) (string, error) {
	msgs := make([]*schema.Message, 0, 2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, schema.SystemMessage(system))
	}
	msgs = append(msgs, schema.UserMessage(user))
	resp, err := m.Generate(ctx, msgs)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// Chat 用 smart 档位进行一次普通对话（默认主写作模型）。
func (l *LLM) Chat(ctx context.Context, system, user string) (string, error) {
	return l.chatWith(ctx, l.smart, system, user)
}

// FastChat 用 fast 档位对话（选角色、JSON 输出等小任务）。
func (l *LLM) FastChat(ctx context.Context, system, user string) (string, error) {
	return l.chatWith(ctx, l.fast, system, user)
}

// StrategicChat 用 strategic 档位对话（规划、拆子主题）。
func (l *LLM) StrategicChat(ctx context.Context, system, user string) (string, error) {
	return l.chatWith(ctx, l.strategic, system, user)
}

// streamWith 用指定档位的模型以流式方式生成回复。
func (l *LLM) streamWith(ctx context.Context, m model.ToolCallingChatModel, system, user string) (<-chan string, error) {
	msgs := make([]*schema.Message, 0, 2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, schema.SystemMessage(system))
	}
	msgs = append(msgs, schema.UserMessage(user))

	reader, err := m.Stream(ctx, msgs)
	if err != nil {
		return nil, err
	}
	ch := make(chan string, 16)
	go func() {
		defer reader.Close()
		defer close(ch)
		// 流式剥离 <think>...</think>：思考模型（MiniMax-M3 等）会在正文前
		// 先输出 <think>推理过程</think>。这些 think 内容是噪音，不该进报告。
		// 但流式是逐块到达的，<think>/</think> 标签可能跨块，需要状态机处理。
		var buf strings.Builder // 累积未决文本（可能含未闭合标签的片段）
		inThink := false        // 是否正在 think 块内
		flushOut := func(s string) {
			if s == "" {
				return
			}
			select {
			case ch <- s:
			case <-ctx.Done():
			}
		}
		for {
			msg, err := reader.Recv()
			if err != nil {
				// 流结束：把缓冲里剩余的非 think 内容冲出去。
				if !inThink {
					flushOut(buf.String())
				}
				return
			}
			if msg.Content == "" {
				continue
			}
			buf.WriteString(msg.Content)
			// 处理缓冲：逐个找 <think>/</think> 标签边界。
			for {
				s := buf.String()
				if !inThink {
					// 寻找 <think> 开始。
					idx := strings.Index(s, "<think>")
					if idx < 0 {
						// 没有开始标签：为防标签跨块，保留末尾 7 字符（<think> 长度），
						// 其余安全输出。
						if len(s) > 7 {
							flushOut(s[:len(s)-7])
							buf.Reset()
							buf.WriteString(s[len(s)-7:])
						}
						break
					}
					// 输出 <think> 之前的内容。
					flushOut(s[:idx])
					buf.Reset()
					buf.WriteString(s[idx+len("<think>"):])
					inThink = true
				} else {
					// 在 think 内，寻找 </think> 结束。
					idx := strings.Index(s, "</think>")
					if idx < 0 {
						// 保留末尾 8 字符（</think> 长度），丢弃前面的 think 内容。
						if len(s) > 8 {
							buf.Reset()
							buf.WriteString(s[len(s)-8:])
						}
						break
					}
					// 丢弃 </think> 及之前的 think 内容。
					buf.Reset()
					buf.WriteString(s[idx+len("</think>"):])
					inThink = false
				}
			}
		}
	}()
	return ch, nil
}

// ChatStream 以流式方式生成回复（用 smart 档位，默认主写作模型）。
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
	return l.streamWith(ctx, l.smart, system, user)
}

// StrategicChatStream 以流式方式生成回复（用 strategic 档位）。
func (l *LLM) StrategicChatStream(ctx context.Context, system, user string) (<-chan string, error) {
	return l.streamWith(ctx, l.strategic, system, user)
}

// FastChatStream 以流式方式生成回复（用 fast 档位）。
func (l *LLM) FastChatStream(ctx context.Context, system, user string) (<-chan string, error) {
	return l.streamWith(ctx, l.fast, system, user)
}

// chatJSONWith 用指定档位的模型进行一次对话并要求返回合法 JSON。
func (l *LLM) chatJSONWith(ctx context.Context, m model.ToolCallingChatModel, system, user string, out any) error {
	if strings.TrimSpace(system) != "" {
		system = system + "\n\n重要：只输出合法的 JSON，不要包含任何解释性文字或 markdown 代码块标记。"
	} else {
		system = "只输出合法的 JSON，不要包含任何解释性文字或 markdown 代码块标记。"
	}
	content, err := l.chatWith(ctx, m, system, user)
	if err != nil {
		return fmt.Errorf("ChatJSON 调用失败: %w", err)
	}
	cleaned := ExtractJSON(content)
	if err := json.Unmarshal([]byte(cleaned), out); err != nil {
		return fmt.Errorf("ChatJSON 解析失败: %w (原始输出前200字符: %s)", err, Truncate(content, 200))
	}
	return nil
}

// ChatJSON 用 smart 档位进行对话并要求返回合法 JSON（默认）。
func (l *LLM) ChatJSON(ctx context.Context, system, user string, out any) error {
	return l.chatJSONWith(ctx, l.smart, system, user, out)
}

// FastChatJSON 用 fast 档位进行对话并要求返回合法 JSON（选角色、reviser/reviewer 等）。
func (l *LLM) FastChatJSON(ctx context.Context, system, user string, out any) error {
	return l.chatJSONWith(ctx, l.fast, system, user, out)
}

// StrategicChatJSON 用 strategic 档位进行对话并要求返回合法 JSON（规划、拆子主题）。
func (l *LLM) StrategicChatJSON(ctx context.Context, system, user string, out any) error {
	return l.chatJSONWith(ctx, l.strategic, system, user, out)
}

// ExtractJSON 从可能包含多余文字的字符串里截取第一个 JSON 值（数组或对象）。
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	// 去掉思考模型的 <think>...</think> 块。
	// MiniMax-M3、DeepSeek-R1 等推理模型会在回答前先输出
	// 一段 <think> reasoning </think>，里面常含 [、{ 等字符，
	// 会干扰下面的"找第一个 JSON 起始符"逻辑。
	// 用正则去掉所有已闭合的 <think>...</think>。
	thinkRe := regexp.MustCompile(`(?s)<think>.*?</think>`)
	s = thinkRe.ReplaceAllString(s, "")
	// 去掉 </think> 残留标签（有些模型只输出闭合标签）。
	s = strings.ReplaceAll(s, "</think>", "")
	// 未闭合的 <think>（模型截断，思考太长没输出 JSON）：
	// 这是个常见失败模式——思考模型 max_tokens 用完，只输出了 think 推理过程。
	// 此时剥离标签后剩下的是纯自然语言（无 JSON 结构），
	// 应返回空，让上层据此判断"模型没产出 JSON"并降级处理，
	// 而不是误把 think 里的 {/[ 当 JSON 起始符提取出乱七八糟的内容。
	if strings.Contains(s, "<think>") && !strings.Contains(s, "</think>") {
		// 截断到 <think> 之前的内容（<think> 之后全是未完成的思考）。
		if idx := strings.Index(s, "<think>"); idx >= 0 {
			s = s[:idx]
		}
	}
	s = strings.ReplaceAll(s, "<think>", "")
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
