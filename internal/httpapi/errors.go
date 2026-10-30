// Package httpapi 实现 v2 动态 API 与管理接口。
//
// v2 动态接口统一返回计划 3.6 定义的错误信封：
//
//	{"schema_version": 2, "error": {"code": "...", "message": "...", "details": {}}}
package httpapi

import (
	"encoding/json"
	"net/http"
)

const schemaVersion = 2

// apiError 是一个可渲染的 API 错误。
type apiError struct {
	status  int
	code    string
	message string
	details map[string]any
}

// 预定义错误（对照计划 3.6 的状态码与错误码表）。
func errInvalidJSON() apiError {
	return apiError{http.StatusBadRequest, "INVALID_JSON", "请求体不是合法的 JSON", map[string]any{}}
}

func errValidation(message string, field string) apiError {
	return apiError{http.StatusBadRequest, "VALIDATION_ERROR", message, map[string]any{"field": field}}
}

func errInvalidDeviceID() apiError {
	return apiError{http.StatusBadRequest, "INVALID_DEVICE_ID", "缺少或非法的设备 ID 请求头", map[string]any{"field": "X-AzurPilot-ID"}}
}

func errDeviceIDMismatch() apiError {
	return apiError{http.StatusBadRequest, "DEVICE_ID_MISMATCH", "请求头与正文中的设备 ID 不一致", map[string]any{"field": "device_id"}}
}

func errServerNotFound() apiError {
	return apiError{http.StatusNotFound, "SERVER_NOT_FOUND", "服务器不存在", map[string]any{}}
}

func errMethodNotAllowed(allow string) apiError {
	return apiError{http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法", map[string]any{"allow": allow}}
}

func errRequestConflict(message string) apiError {
	return apiError{http.StatusConflict, "REQUEST_CONFLICT", message, map[string]any{}}
}

func errStaleSnapshot() apiError {
	return apiError{http.StatusConflict, "STALE_SNAPSHOT", "旧快照不能覆盖新快照", map[string]any{}}
}

func errPayloadTooLarge() apiError {
	return apiError{http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "请求体超过 64 KiB 上限", map[string]any{}}
}

func errUnsupportedMediaType() apiError {
	return apiError{http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type 必须为 application/json", map[string]any{}}
}

func errRateLimited(retryAfter string) apiError {
	return apiError{http.StatusTooManyRequests, "RATE_LIMITED", "请求过于频繁", map[string]any{"retry_after": retryAfter}}
}

func errSourceUnavailable() apiError {
	return apiError{http.StatusServiceUnavailable, "SOURCE_UNAVAILABLE", "数据源暂不可用", map[string]any{}}
}

func errServiceUnavailable() apiError {
	return apiError{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "服务暂不可用", map[string]any{}}
}

func errInternal() apiError {
	// 不向客户端返回任何内部细节或堆栈。
	return apiError{http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误", map[string]any{}}
}

func errUnauthorized() apiError {
	return apiError{http.StatusUnauthorized, "UNAUTHORIZED", "缺少管理凭据", map[string]any{}}
}

// errorEnvelope 是计划 3.6 的错误响应结构。
type errorEnvelope struct {
	SchemaVersion int       `json:"schema_version"`
	Error         errorBody `json:"error"`
}

type errorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// writeError 输出统一错误信封。
func writeError(w http.ResponseWriter, r *http.Request, e apiError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if e.status == http.StatusMethodNotAllowed {
		if allow, ok := e.details["allow"].(string); ok && allow != "" {
			w.Header().Set("Allow", allow)
		}
	}
	if e.status == http.StatusTooManyRequests {
		if retry, ok := e.details["retry_after"].(string); ok && retry != "" {
			w.Header().Set("Retry-After", retry)
		}
	}
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		SchemaVersion: schemaVersion,
		Error: errorBody{
			Code:    e.code,
			Message: e.message,
			Details: e.details,
		},
	})
}

// writeJSON 输出成功响应。
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
