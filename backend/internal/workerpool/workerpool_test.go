package workerpool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPool_LimitsConcurrency(t *testing.T) {
	const workers = 3
	p := New(workers)
	if got := p.Cap(); got != workers {
		t.Fatalf("Cap = %d, want %d", got, workers)
	}

	var (
		cur      int64       // 当前并发数
		maxCur   int64       // 观察到的最大并发数
		mu       sync.Mutex
		totalRun atomic.Int64
	)

	const tasks = 20
	for i := 0; i < tasks; i++ {
		i := i
		p.Go(context.Background(), func() error {
			c := atomic.AddInt64(&cur, 1)
			mu.Lock()
			if c > maxCur {
				maxCur = c
			}
			mu.Unlock()
			time.Sleep(5 * time.Millisecond) // 模拟工作
			atomic.AddInt64(&cur, -1)
			totalRun.Add(1)
			_ = i
			return nil
		})
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait err: %v", err)
	}

	if got := totalRun.Load(); got != tasks {
		t.Errorf("totalRun = %d, want %d", got, tasks)
	}
	if maxCur > workers {
		t.Errorf("observed max concurrency = %d, exceeded limit %d", maxCur, workers)
	}
}

func TestPool_RecordsFirstError(t *testing.T) {
	p := New(2)
	wantErr := errors.New("boom")
	p.Go(context.Background(), func() error { return wantErr })
	p.Go(context.Background(), func() error {
		time.Sleep(10 * time.Millisecond)
		return errors.New("second")
	})
	if err := p.Wait(); err != wantErr {
		t.Errorf("Wait err = %v, want %v", err, wantErr)
	}
}

func TestPool_RespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	p := New(2)
	p.Go(ctx, func() error { return nil })
	if err := p.Wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait err = %v, want context.Canceled", err)
	}
}

func TestPool_DefaultCapacity(t *testing.T) {
	p := New(0)
	if p.Cap() <= 0 {
		t.Errorf("New(0).Cap() = %d, want > 0", p.Cap())
	}
}
