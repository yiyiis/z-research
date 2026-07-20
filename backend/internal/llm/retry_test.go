package llm

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{"nil", nil, ErrUnknown},
		{"timeout", errors.New("request timeout"), ErrTransient},
		{"deadline", context.DeadlineExceeded, ErrTransient},
		{"canceled", context.Canceled, ErrUnknown}, // 用户取消不重试
		{"429", errors.New("HTTP 429 Too Many Requests"), ErrTransient},
		{"rate_limit", errors.New("rate_limit exceeded"), ErrTransient},
		{"500", errors.New("500 Internal Server Error"), ErrTransient},
		{"503", errors.New("503 Service Unavailable"), ErrTransient},
		{"eof", errors.New("unexpected EOF"), ErrTransient},
		{"conn_reset", errors.New("connection reset by peer"), ErrTransient},
		{"401", errors.New("401 Unauthorized"), ErrAuth},
		{"api_key", errors.New("invalid API key"), ErrAuth},
		{"forbidden", errors.New("403 Forbidden"), ErrAuth},
		{"400", errors.New("400 Bad Request"), ErrClient},
		{"model_not_found", errors.New("model not found: foo"), ErrClient},
		{"context_length", errors.New("context length exceeded"), ErrClient},
		{"unknown", errors.New("something weird"), ErrUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyError(tc.err); got != tc.want {
				t.Errorf("ClassifyError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestWithRetry_SuccessOnFirstTry(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}
	var calls int32
	result, err := withRetry(context.Background(), cfg, nil, func() (string, error) {
		atomic.AddInt32(&calls, 1)
		return "ok", nil
	})
	if err != nil || result != "ok" {
		t.Fatalf("withRetry = %q, %v", result, err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestWithRetry_TransientThenSucceed(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}
	var calls int32
	result, err := withRetry(context.Background(), cfg, nil, func() (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return "", errors.New("500 Internal Server Error") // 瞬时
		}
		return "ok", nil
	})
	if err != nil || result != "ok" {
		t.Fatalf("withRetry = %q, %v", result, err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWithRetry_AuthErrorNoRetry(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}
	var calls int32
	_, err := withRetry(context.Background(), cfg, nil, func() (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", errors.New("401 Unauthorized")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("auth error should not retry, calls = %d, want 1", calls)
	}
}

func TestWithRetry_ExhaustedAllRetries(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond}
	var calls int32
	_, err := withRetry(context.Background(), cfg, nil, func() (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", errors.New("503 Service Unavailable")
	})
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if !strings.Contains(err.Error(), "重试 2 次后仍失败") {
		t.Errorf("error message = %q", err.Error())
	}
	// 首次 + 2 次重试 = 3 次调用
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (initial + 2 retries)", calls)
	}
}

func TestWithRetry_RespectsCanceledContext(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 5, BaseDelay: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	// 在第一次失败后立即取消，让重试前的 select 捕获到。
	var calls int32
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := withRetry(ctx, cfg, nil, func() (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", errors.New("503 error")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// 应该因 ctx 取消提前返回，不会跑满 5 次重试。
	if calls > 2 {
		t.Errorf("calls = %d, should be <= 2 due to cancellation", calls)
	}
}

func TestWithRetry_OnRetryCallback(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond}
	var retryReports []int
	withRetry(context.Background(), cfg, func(attempt int, err error) {
		retryReports = append(retryReports, attempt)
	}, func() (string, error) {
		return "", errors.New("503")
	})
	// 重试 2 次（attempt 1 和 2），最终失败。
	if len(retryReports) != 2 || retryReports[0] != 1 || retryReports[1] != 2 {
		t.Errorf("retry reports = %v, want [1 2]", retryReports)
	}
}
