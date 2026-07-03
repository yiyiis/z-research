package config

import (
	"os"
	"testing"
)

// TestLoadConfig_RequiresAPIKey 缺少必需的 ZHIPU_API_KEY 应报错。
func TestLoadConfig_RequiresAPIKey(t *testing.T) {
	t.Setenv("ZHIPU_API_KEY", "")
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("缺少 API Key 应返回错误")
	}
}

// TestLoadConfig_Defaults 验证默认值填充。
func TestLoadConfig_Defaults(t *testing.T) {
	t.Setenv("ZHIPU_API_KEY", "dummy")
	// 清空可能被外部 .env 污染的字段。
	t.Setenv("MAX_ITERATIONS", "")
	t.Setenv("LANGUAGE", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MaxIterations != 3 {
		t.Errorf("默认 MaxIterations 应为 3，得到 %d", cfg.MaxIterations)
	}
	if cfg.Language != "zh" {
		t.Errorf("默认 Language 应为 zh，得到 %q", cfg.Language)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("默认 HTTPAddr 应为 :8080，得到 %q", cfg.HTTPAddr)
	}
}

// TestLoadConfig_EnvOverride 验证环境变量覆盖默认值。
func TestLoadConfig_EnvOverride(t *testing.T) {
	t.Setenv("ZHIPU_API_KEY", "dummy")
	t.Setenv("MAX_ITERATIONS", "5")
	t.Setenv("TOTAL_WORDS", "2000")
	t.Setenv("LANGUAGE", "english")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MaxIterations != 5 {
		t.Errorf("MAX_ITERATIONS=5 未生效，得到 %d", cfg.MaxIterations)
	}
	if cfg.TotalWords != 2000 {
		t.Errorf("TOTAL_WORDS=2000 未生效，得到 %d", cfg.TotalWords)
	}
	if cfg.Language != "english" {
		t.Errorf("LANGUAGE=english 未生效，得到 %q", cfg.Language)
	}
}

// TestLoadConfig_ConcurrencyClamp 并发数小于 1 时应被钳制为 1。
func TestLoadConfig_ConcurrencyClamp(t *testing.T) {
	t.Setenv("ZHIPU_API_KEY", "dummy")
	t.Setenv("CONCURRENCY", "0")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Concurrency != 1 {
		t.Errorf("CONCURRENCY=0 应被钳制为 1，得到 %d", cfg.Concurrency)
	}
}

// TestGetenvInt_Invalid 非法整数回退到默认值。
func TestGetenvInt_Invalid(t *testing.T) {
	t.Setenv("Z_TEST_INT", "abc")
	if got := getenvInt("Z_TEST_INT", 42); got != 42 {
		t.Errorf("非法整数应回退默认值 42，得到 %d", got)
	}
	_ = os.Unsetenv("Z_TEST_INT")
}

// TestGetenvFloat_Invalid 非法浮点回退到默认值。
func TestGetenvFloat_Invalid(t *testing.T) {
	t.Setenv("Z_TEST_FLOAT", "xyz")
	if got := getenvFloat("Z_TEST_FLOAT", 1.5); got != 1.5 {
		t.Errorf("非法浮点应回退默认值 1.5，得到 %f", got)
	}
	_ = os.Unsetenv("Z_TEST_FLOAT")
}
