package houston

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

// UnexpectedResponse signals that an upstream service returned an unexpected status or body.
// The upstream HTTP status is bubbled directly to the caller as-is.
//
//	houston.UnexpectedResponse(ctx, "identity-svc", 503, "gateway timeout")
func UnexpectedResponse(ctx context.Context, serviceName string, upstreamStatus int, upstreamMessage string) Problem {
	return newErr(
		ctx,
		KindUnexpectedResponse,
		fmt.Sprintf("unexpected response from %s [upstream=%d]: %s", serviceName, upstreamStatus, upstreamMessage),
		upstreamStatus,
		false, nil,
	).
		addDetail("service_name", serviceName).
		addDetail("upstream_status", strconv.Itoa(upstreamStatus)).
		addDetail("upstream_message", upstreamMessage)
}

// NetworkError signals a transport-layer failure when calling an upstream service —
// the call failed before receiving any HTTP response (e.g. connection refused, TLS
// handshake failure, DNS resolution failure).
//
// Use Timeout for deadline-exceeded failures.
// Use UnexpectedResponse when an HTTP response was received but was unexpected.
//
//	houston.NetworkError(ctx, "identity-svc", "POST", "/v1/token", "ExchangeToken", err)
//	→ "network error calling identity-svc [POST /v1/token] during ExchangeToken"
func NetworkError(ctx context.Context, serviceName, method, url, operation string, cause error) Problem {
	return newErr(
		ctx,
		KindNetworkError,
		fmt.Sprintf("network error calling %s [%s %s] during %s", serviceName, method, url, operation),
		http.StatusBadGateway,
		false, cause,
	).
		addDetail("service_name", serviceName).
		addDetail("method", method).
		addDetail("url", url).
		addDetail("operation", operation)
}

// StorageError signals that a storage operation (DB, cache, object store) failed unexpectedly.
// cause is the original error from the storage layer — stored in the error chain (Unwrap).
//
//	houston.StorageError(ctx, "postgres", "insert", "duplicate key on users.email", err)
//	→ "storage error on postgres during insert: duplicate key on users.email"
func StorageError(ctx context.Context, datasource, operation, message string, cause error) Problem {
	return newErr(
		ctx,
		KindStorageError,
		fmt.Sprintf("storage error on %s during %s: %s", datasource, operation, message),
		http.StatusInternalServerError,
		false, cause,
	).
		addDetail("data_source", datasource).
		addDetail("operation", operation).
		addDetail("message", message)
}

// CircuitOpen signals that the circuit breaker for a service is open and requests are blocked.
//
//	houston.CircuitOpen(ctx, "profile-svc")
//	→ "service unavailable: profile-svc circuit is open"
func CircuitOpen(ctx context.Context, serviceName string) Problem {
	return newErr(
		ctx,
		KindCircuitOpen,
		fmt.Sprintf("service unavailable: %s circuit is open", serviceName),
		http.StatusServiceUnavailable,
		false, nil,
	).
		addDetail("service_name", serviceName)
}

// Timeout signals that a call to a service or operation exceeded the deadline.
//
//	houston.Timeout(ctx, "cms-svc", "GetProducts")
//	→ "timeout calling cms-svc during GetProducts"
func Timeout(ctx context.Context, serviceName, operation string) Problem {
	return newErr(
		ctx,
		KindTimeout,
		fmt.Sprintf("timeout calling %s during %s", serviceName, operation),
		http.StatusGatewayTimeout,
		false, nil,
	).
		addDetail("service_name", serviceName).
		addDetail("operation", operation)
}

// ConfigMissing signals that a required configuration key is absent at startup or runtime.
//
//	houston.ConfigMissing(ctx, "REDIS_ADDR")
//	→ "missing required configuration: REDIS_ADDR"
func ConfigMissing(ctx context.Context, key string) Problem {
	return newErr(
		ctx,
		KindConfigMissing,
		fmt.Sprintf("missing required configuration: %s", key),
		http.StatusInternalServerError,
		false, nil,
	).
		addDetail("configuration_key", key)
}

// Internal signals an unexpected internal error with no more specific classification.
// Use when no other constructor fits.
//
//	houston.Internal(ctx, "mapper returned nil for known key")
//	→ "internal error: mapper returned nil for known key"
func Internal(ctx context.Context, reason string) Problem {
	return newErr(
		ctx,
		KindInternal,
		fmt.Sprintf("internal error: %s", reason),
		http.StatusInternalServerError,
		false, nil,
	).
		addDetail("reason", reason)
}
