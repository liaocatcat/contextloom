package modelgateway

import (
	"errors"
	"strings"
)

// ErrPoolTripped 整池所有实例均已熔断（池熔断）或空池不可用。
var ErrPoolTripped = errors.New("modelgateway: 目标池已整体熔断")

// errSemTimeout 等待池信号量超时。
var errSemTimeout = errors.New("modelgateway: timeout: 等待池信号量超时")

// retryable 判断错误是否可重试：对 429/限流/超时/5xx/网络类错误
// 用小写字符串包含匹配实现，不依赖任何外部包。
func retryable(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, p := range []string{
		"429", "rate limit", "rate_limit", "too many requests",
		"timeout", "timed out", "deadline exceeded", "context deadline",
		"temporarily unavailable", "service unavailable", "busy",
		"internal server error", "server error",
		"500", "502", "503", "504",
		"connection reset", "connection refused", "connection closed",
		"eof", "network", "unreachable",
	} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
