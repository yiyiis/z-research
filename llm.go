package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	openaiembed "github.com/cloudwego/eino-ext/components/embedding/openai"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// LLM 封装了一个对话模型 (ChatModel) 和一个 embedding 模型 (Embedder)，
// 并提供 Chat / ChatJSON 两个便捷调用。底层均使用 eino-ext 的 OpenAI 兼容实现，
// 因此可指向 GLM / DeepSeek / OpenAI / 任意 OpenAI 兼容服务。
type LLM struct {
	chat  model.ToolCallingChatModel
	embed embedding.Embedder
}

// NewLLM 根据配置创建对话模型与 embedding 模型。
func NewLLM(ctx context.Context, cfg *Config) (*LLM, error) {
	temperature := float32(cfg.Temperature)

	chatModel, err := openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
		BaseURL:     cfg.LLMBase,
		APIKey:      cfg.APIKey,
		Model:       cfg.LLMModel,
		Temperature: &temperature,
	})
	if err != nil {
		return nil, fmt.Errorf("创建对话模型失败: %w", err)
	}

	embedder, err := openaiembed.NewEmbedder(ctx, &openaiembed.EmbeddingConfig{
		BaseURL: cfg.EmbedBase,
		APIKey:  cfg.EmbedAPIKey,
		Model:   cfg.EmbedModel,
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

	cleaned := extractJSON(content)
	if err := json.Unmarshal([]byte(cleaned), out); err != nil {
		return fmt.Errorf("ChatJSON 解析失败: %w (原始输出前200字符: %s)", err, truncate(content, 200))
	}
	return nil
}

// extractJSON 从可能包含多余文字的字符串里截取第一个 JSON 值（数组或对象）。
func extractJSON(s string) string {
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
