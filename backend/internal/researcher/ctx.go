// Package researcher — ctx.go 提供 per-run 初始状态的 context 注入。
//
// Graph 的 GenLocalState 在编译期固定，每轮 Invoke 创建一个零值
// *ResearchState。要把 query、回调等 per-run 值传进 graph，我们把这些
// 值 stash 进 context，由 choose_role 节点的 StatePreHandler 读出
// 写入 graph 状态。
package researcher

import "context"

type initialStateKey struct{}

// WithInitialState 返回携带初始 ResearchState 的子 context。
func WithInitialState(ctx context.Context, s *ResearchState) context.Context {
	return context.WithValue(ctx, initialStateKey{}, s)
}

// InitialStateFromContext 取出 WithInitialState 存入的状态，没有则返回 nil。
func InitialStateFromContext(ctx context.Context) *ResearchState {
	if s, ok := ctx.Value(initialStateKey{}).(*ResearchState); ok {
		return s
	}
	return nil
}
