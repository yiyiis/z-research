// Package deep — ctx.go 提供 per-run 初始状态的 context 注入。
package deep

import "context"

type initialStateKey struct{}

// WithInitialState 返回携带初始 DeepState 的子 context。
func WithInitialState(ctx context.Context, s *DeepState) context.Context {
	return context.WithValue(ctx, initialStateKey{}, s)
}

// InitialStateFromContext 取出 WithInitialState 存入的状态。
func InitialStateFromContext(ctx context.Context) *DeepState {
	if s, ok := ctx.Value(initialStateKey{}).(*DeepState); ok {
		return s
	}
	return nil
}
