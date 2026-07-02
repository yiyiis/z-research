package main

import "fmt"

// logf 输出带前缀的进度日志到标准错误，避免污染标准输出（标准输出留给最终报告）。
func logf(format string, args ...any) {
	fmt.Printf("🔍 "+format+"\n", args...)
}
