package api

import "fmt"

// Standard typed error codes
const (
	ErrCodeUnauthenticated = "unauthenticated"
	ErrCodePermissionDenied = "permission_denied"
	ErrCodeNotFound         = "not_found"
	ErrCodeQuotaExceeded    = "quota_exceeded"
	ErrCodeConflict         = "conflict"
	ErrCodeBadRequest       = "bad_request"
	ErrCodeRateLimited      = "rate_limited"
	ErrCodeInternalError    = "internal_error"
)

// Error represents a structured error returned in WS responses or HTTP APIs.
type Error struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
	TraceID string                 `json:"trace_id,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func NewError(code, message string, traceID string) *Error {
	return &Error{
		Code:    code,
		Message: message,
		TraceID: traceID,
	}
}

func NewQuotaError(resource string, limit, used, requested int, traceID string) *Error {
	return &Error{
		Code:    ErrCodeQuotaExceeded,
		Message: fmt.Sprintf("quota exceeded for resource %s: limit %d, used %d, requested %d", resource, limit, used, requested),
		Details: map[string]interface{}{
			"resource":  resource,
			"limit":     limit,
			"used":      used,
			"requested": requested,
		},
		TraceID: traceID,
	}
}
