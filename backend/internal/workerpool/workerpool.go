// Package workerpool 提供一个带全局并发上限的 goroutine 池，
// 统一替代项目里原本散落在多处的 errgroup.SetLimit 与 channel 信号量。
//
// 面试话术关键词：MAX_SCRAPER_WORKERS = 15。
//
// 设计要点：
//
//   - 一个 Pool 持有一个固定容量的信号量（chan struct{}），
//     Go() 调用先获取 token 再 spawn goroutine，从而把并发数
//     严格限制在容量以内。
//
//   - fail-fast：任意一个任务返回 error，Wait() 立即返回该错误，
//     其余任务仍会跑完（不强行取消，避免中断已发起的 HTTP 请求），
//     适合"任何单个网页失败都不应中断整体研究"的场景。
//
//   - context 由调用方传入，Pool 不替你 cancel；调用方可用
//     errgroup.WithContext + ctx.Done() 实现级联取消。
package workerpool

import (
	"context"
	"runtime"
	"sync"
)

// Pool 是一个有界并发 goroutine 池。
type Pool struct {
	sem  chan struct{}     // 容量 = 最大并发数
	wg   sync.WaitGroup    // 等待所有任务结束
	mu   sync.Mutex        // 保护 err
	err  error             // 第一个 error（fail-fast）
	once sync.Once         // err 只记录一次
}

// New 创建一个最大并发数为 maxWorkers 的 Pool。
//
// maxWorkers <= 0 时回退到 runtime.NumCPU()，避免无限制 spawn。
func New(maxWorkers int) *Pool {
	if maxWorkers <= 0 {
		maxWorkers = runtime.NumCPU()
	}
	return &Pool{sem: make(chan struct{}, maxWorkers)}
}

// Go 提交一个任务到池中。
//
// 它会阻塞直到拿到一个 token（即有空闲 worker），然后 spawn 一个
// goroutine 执行 fn。fn 返回的非 nil error 会被记录，Wait() 返回
// 第一个 error。fn 内部应尊重 ctx，以便调用方取消时能及时退出。
//
// 注意：Go 是同步阻塞调用（拿不到 token 就等），不会无限制 spawn
// goroutine，这是与裸 `go fn()` 的关键区别。
func (p *Pool) Go(ctx context.Context, fn func() error) {
	// ctx 已取消则不再提交，避免在 Wait 前堆积任务。
	if err := ctx.Err(); err != nil {
		p.recordErr(err)
		return
	}
	// 获取 token（阻塞）；ctx 取消时放弃提交。
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		p.recordErr(ctx.Err())
		return
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() { <-p.sem }() // 归还 token
		if err := fn(); err != nil {
			p.recordErr(err)
		}
	}()
}

// Wait 阻塞直到所有已提交的任务完成，返回第一个出现的 error（若有）。
//
// 多次调用 Wait 是安全的，但只有第一次会等到任务；后续调用立即返回
// 缓存的 error。
func (p *Pool) Wait() error {
	p.wg.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// recordErr 记录第一个出现的 error。
func (p *Pool) recordErr(err error) {
	if err == nil {
		return
	}
	p.once.Do(func() {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
	})
}

// Cap 返回池的容量（最大并发数）。
func (p *Pool) Cap() int { return cap(p.sem) }
