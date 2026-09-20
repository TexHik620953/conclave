package provider

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// APIError is a non-2xx response from an OpenAI-compatible endpoint.
type APIError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// Retryable reports whether the error is worth retrying.
func (e *APIError) Retryable() bool {
	return e.StatusCode == 408 || e.StatusCode == 409 || e.StatusCode == 429 || e.StatusCode >= 500
}

// IsRetryable reports whether err is a transient provider error.
func IsRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	return false
}

// RetryAfterOf returns the server-requested delay, if any.
func RetryAfterOf(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfter
	}
	return 0
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := time.Parse(time.RFC1123, header); err == nil {
		return time.Until(t)
	}
	return 0
}
