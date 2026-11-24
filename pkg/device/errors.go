package device

import (
	"errors"
	"fmt"
)

// Result 控制结果分类，用于日志的 result 字段
type Result string

const (
	ResultSuccess         Result = "success"
	ResultValidationError Result = "validation_error"
	ResultOffline         Result = "offline"
	ResultTimeout         Result = "timeout"
	ResultDeviceFailed    Result = "device_failed"
	ResultNotConnected    Result = "not_connected"
	ResultCanceled        Result = "canceled"
)

// 哨兵错误，可用 errors.Is 判断类别
var (
	ErrValidation   = errors.New("validation error")
	ErrOffline      = errors.New("device offline")
	ErrTimeout      = errors.New("device response timeout")
	ErrDeviceFailed = errors.New("device execution failed")
	ErrNotConnected = errors.New("mqtt not connected")
)

// Error 业务错误：携带类别与可直接展示给 LLM / 用户的中文原因
type Error struct {
	Kind   error
	Reason string
}

func (e *Error) Error() string { return e.Reason }

// Unwrap 使 errors.Is(err, ErrXxx) 生效
func (e *Error) Unwrap() error { return e.Kind }

func newErr(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, Reason: fmt.Sprintf(format, args...)}
}

func validationErr(format string, args ...any) *Error {
	return newErr(ErrValidation, format, args...)
}

// resultOf 把错误映射为日志 result
func resultOf(err error) Result {
	switch {
	case err == nil:
		return ResultSuccess
	case errors.Is(err, ErrValidation):
		return ResultValidationError
	case errors.Is(err, ErrOffline):
		return ResultOffline
	case errors.Is(err, ErrTimeout):
		return ResultTimeout
	case errors.Is(err, ErrDeviceFailed):
		return ResultDeviceFailed
	case errors.Is(err, ErrNotConnected):
		return ResultNotConnected
	default:
		return ResultCanceled
	}
}
