package types

import "fmt"

// ── 错误码 ──

// ErrorCode 是领域错误码。
type ErrorCode string

const (
	ErrUnknown         ErrorCode = "UNKNOWN"
	ErrConfig          ErrorCode = "CONFIG_ERROR"
	ErrModel           ErrorCode = "MODEL_ERROR"
	ErrModelTimeout    ErrorCode = "MODEL_TIMEOUT"
	ErrModelRateLimit  ErrorCode = "MODEL_RATE_LIMIT"
	ErrToolNotFound    ErrorCode = "TOOL_NOT_FOUND"
	ErrToolExec        ErrorCode = "TOOL_EXEC_ERROR"
	ErrToolTimeout     ErrorCode = "TOOL_TIMEOUT"
	ErrHarnessSearch   ErrorCode = "HARNESS_SEARCH_ERROR"
	ErrMemoryStore     ErrorCode = "MEMORY_STORE_ERROR"
	ErrMemoryLoad      ErrorCode = "MEMORY_LOAD_ERROR"
	ErrSessionNotFound ErrorCode = "SESSION_NOT_FOUND"
	ErrAgentNotFound   ErrorCode = "AGENT_NOT_FOUND"
	ErrAgentTimeout    ErrorCode = "AGENT_TIMEOUT"
	ErrOverseerCheat   ErrorCode = "OVERSEER_CHEAT_DETECTED"
	ErrOverseerFail    ErrorCode = "OVERSEER_CONSTRAINT_FAILED"
	ErrACPDisconnected ErrorCode = "ACP_DISCONNECTED"
	ErrACPSendFail     ErrorCode = "ACP_SEND_FAILED"
	ErrPluginLoad      ErrorCode = "PLUGIN_LOAD_ERROR"
	ErrSandbox         ErrorCode = "SANDBOX_ERROR"
	ErrValidation      ErrorCode = "VALIDATION_ERROR"
)

// ── 领域错误 ──

// DomainError 是 ContextLoom 统一的领域错误类型。
// 包含错误码、人类可读消息和可选的原始错误。
type DomainError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Cause   error     `json:"-"`
}

func (e *DomainError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *DomainError) Unwrap() error {
	return e.Cause
}

// NewDomainError 创建一个新的领域错误。
func NewDomainError(code ErrorCode, message string, cause error) *DomainError {
	return &DomainError{Code: code, Message: message, Cause: cause}
}

// ── Result 泛型 ──

// Result 是一个简单的泛型结果类型，用于替代 (T, error) 元组。
// 适用于 channel 和异步回调场景。
type Result[T any] struct {
	Value T     `json:"value,omitempty"`
	Error error `json:"error,omitempty"`
}

// Ok 创建一个成功的 Result。
func Ok[T any](value T) Result[T] {
	return Result[T]{Value: value}
}

// Err 创建一个失败的 Result。
func Err[T any](err error) Result[T] {
	return Result[T]{Error: err}
}

// IsOk 返回 Result 是否成功。
func (r Result[T]) IsOk() bool {
	return r.Error == nil
}

// IsErr 返回 Result 是否失败。
func (r Result[T]) IsErr() bool {
	return r.Error != nil
}

// Unwrap 返回值和错误（兼容标准 Go 模式）。
func (r Result[T]) Unwrap() (T, error) {
	return r.Value, r.Error
}
