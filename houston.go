// Package houston provides an ergonomic application error system.
// Declare problems clearly from any layer:
//
//	houston.Unauthorized(ctx, "token expired")
//	houston.ResourceNotFound(ctx, "user", id)
//	houston.StorageError(ctx, "postgres", "insert", msg, err)
//
// Configure at startup (call before serving requests):
//
//	houston.SetTraceExtractor(func(ctx context.Context) string {
//	    return ctx.Value(middleware.CorrelationIDKey).(string)
//	})
//	houston.SetMapper(houston.NewDefaultMapper(map[string]int32{...}))
package houston

import (
	"context"
	"strings"
	"sync"
)

// Problem is the single error type the application is aware of.
// Once created, it flows as-is through the call stack.
// Use WithContext to add call-site information without changing semantics.
type Problem interface {
	error                             // Error() string — full debug string (includes TraceID)
	Message() string                  // user-facing message (goes into JSON response body)
	HTTPStatus() int                  // HTTP status code — fixed by constructor
	IsBusiness() bool                 // true = business (4xx), false = technical (5xx/502-504)
	Kind() string                     // semantic kind constant, e.g. KindBadInput
	TraceID() string                  // trace/correlation ID extracted at creation time; empty if not configured
	Tags() []Tag                      // injected tags for cross-cutting behavior
	Tag(t Tag) Problem                // fluent tag injection — returns new copy
	WithContext(ctx string) Problem   // append call-site context note — returns new copy
	Details() map[string]string       // structured key-value details attached to the error
	Unwrap() error                    // stdlib error chain support
}

// TraceExtractor extracts a trace or correlation ID from a context.
// Register one at startup via SetTraceExtractor so houston does not need to know
// which context key your application uses.
type TraceExtractor func(ctx context.Context) string

var (
	traceExtractor TraceExtractor
	extractorMu    sync.RWMutex

	secureMode   bool
	secureModeMu sync.RWMutex
)

// SetTraceExtractor registers the global trace extractor. Call once in main() before serving.
//
//	houston.SetTraceExtractor(func(ctx context.Context) string {
//	    id, _ := ctx.Value(middleware.CorrelationIDKey).(string)
//	    return id
//	})
func SetTraceExtractor(fn TraceExtractor) {
	extractorMu.Lock()
	traceExtractor = fn
	extractorMu.Unlock()
}

// EnableSecureMode activates secure error handling globally.
// Call once at startup (before serving requests) alongside other Set* configuration.
//
// In secure mode:
//   - Message() on technical errors returns a generic safe string instead of the
//     caller-provided upstream/storage message that may contain internal details.
//   - Details() on technical errors returns only an allowlisted subset of keys,
//     stripping caller-provided content (upstream_message, message, url, reason, etc.).
//   - TraceID(), Kind(), HTTPStatus(), IsBusiness(), Tags(), Unwrap(), and Error()
//     are unaffected — Error() remains the full debug string for structured logging.
//
// Use DisableSecureMode() to turn it off (primarily for tests).
func EnableSecureMode() {
	secureModeMu.Lock()
	secureMode = true
	secureModeMu.Unlock()
}

// DisableSecureMode deactivates secure mode. Intended for tests only.
func DisableSecureMode() {
	secureModeMu.Lock()
	secureMode = false
	secureModeMu.Unlock()
}

func isSecureMode() bool {
	secureModeMu.RLock()
	defer secureModeMu.RUnlock()
	return secureMode
}

// ExtractTrace runs the registered extractor against ctx.
// Returns empty string if no extractor is set or ctx is nil.
// Called internally by constructors.
func ExtractTrace(ctx context.Context) string {
	extractorMu.RLock()
	fn := traceExtractor
	extractorMu.RUnlock()
	if fn == nil || ctx == nil {
		return ""
	}
	return fn(ctx)
}

// Tag is a typed marker attached to a Problem for cross-cutting concerns.
// Use type switches to read tags at the response or logging layer.
type Tag interface{ isTag() }

// ExternalCode carries a numeric error code from an upstream API response.
// Useful when passing through an external service's error code verbatim.
type ExternalCode struct{ Code int32 }

// RetryAfter signals how many seconds the caller should wait before retrying.
// Typically used with RateLimited.
type RetryAfter struct{ Seconds int }

// SuppressLog suppresses log emission for this error.
// Use for expected errors (e.g. 404 lookups) that would otherwise spam logs.
type SuppressLog struct{}

// AlertOncall signals that this error should trigger an oncall alert.
type AlertOncall struct{}

// BubbleStatus overrides the HTTP status code sent to the caller with a specific value.
// Use on UnexpectedResponse when an upstream status (e.g. 429, 409) must be
// forwarded as-is to the frontend rather than collapsed into a generic 502.
//
//	UnexpectedResponse(ctx, "identity-svc", 429, "rate limit exceeded").
//	    Tag(houston.BubbleStatus{Status: 429})
//	// → caller receives HTTP 429, not 502
type BubbleStatus struct{ Status int }

func (ExternalCode) isTag() {}
func (RetryAfter) isTag()   {}
func (SuppressLog) isTag()  {}
func (AlertOncall) isTag()  {}
func (BubbleStatus) isTag() {}

// Kind constants — use as keys in CodeMapper and for branching on error behavior.
const (
	// Business kinds (4xx)
	KindBadInput         = "bad_input"
	KindResourceNotFound = "resource_not_found"
	KindUnauthorized     = "unauthorized"
	KindForbidden        = "forbidden"
	KindConflict         = "conflict"
	KindRateLimited      = "rate_limited"
	KindUnexpectedValue  = "unexpected_value"

	// Technical kinds (5xx / 502–504)
	KindUnexpectedResponse = "unexpected_response"
	KindNetworkError       = "network_error"
	KindStorageError       = "storage_error"
	KindCircuitOpen        = "circuit_open"
	KindTimeout            = "timeout"
	KindConfigMissing      = "config_missing"
	KindInternal           = "internal"
)

// --- concrete implementation (unexported) ---

type kvPair struct {
	key   string
	value string
}

// secureModeDetailAllowlist is the set of detail keys retained when secure mode is active
// for technical (non-business) errors. Keys not in this list may contain caller-provided
// content from upstream services or storage layers and are stripped to prevent leakage.
var secureModeDetailAllowlist = map[string]bool{
	"service_name":    true,
	"operation":       true,
	"data_source":     true,
	"upstream_status": true,
}

type appErr struct {
	kind        string
	message     string
	safeMessage string // returned by Message() in secure mode for tech errors
	httpStatus  int
	isBiz       bool
	traceID     string
	tags        []Tag
	contexts    []string
	details     []kvPair
	cause       error
}

func newErr(ctx context.Context, kind, message string, httpStatus int, isBiz bool, cause error) *appErr {
	return &appErr{
		kind:       kind,
		message:    message,
		httpStatus: httpStatus,
		isBiz:      isBiz,
		traceID:    ExtractTrace(ctx),
		cause:      cause,
	}
}

func (e *appErr) addDetail(key, value string) *appErr {
	e.details = append(e.details, kvPair{key: key, value: value})
	return e
}

func (e *appErr) withSafeMessage(msg string) *appErr {
	e.safeMessage = msg
	return e
}

func (e *appErr) Message() string {
	if isSecureMode() && !e.isBiz && e.safeMessage != "" {
		return e.safeMessage
	}
	return e.message
}
func (e *appErr) HTTPStatus() int    { return e.httpStatus }
func (e *appErr) IsBusiness() bool   { return e.isBiz }
func (e *appErr) Kind() string       { return e.kind }
func (e *appErr) TraceID() string    { return e.traceID }
func (e *appErr) Tags() []Tag        { return e.tags }
func (e *appErr) Unwrap() error      { return e.cause }

func (e *appErr) Details() map[string]string {
	if isSecureMode() && !e.isBiz {
		m := make(map[string]string)
		for _, kv := range e.details {
			if secureModeDetailAllowlist[kv.key] {
				m[kv.key] = kv.value
			}
		}
		return m
	}
	m := make(map[string]string, len(e.details))
	for _, kv := range e.details {
		m[kv.key] = kv.value
	}
	return m
}

func (e *appErr) Error() string {
	var sb strings.Builder
	sb.WriteString("[" + e.kind + "] " + e.message)
	if e.traceID != "" {
		sb.WriteString(" trace=" + e.traceID)
	}
	for _, kv := range e.details {
		sb.WriteString(" " + kv.key + "=" + kv.value)
	}
	for _, ctx := range e.contexts {
		sb.WriteString(" (" + ctx + ")")
	}
	if e.cause != nil {
		sb.WriteString(": " + e.cause.Error())
	}
	return sb.String()
}

func (e *appErr) Tag(t Tag) Problem {
	cp := e.clone()
	cp.tags = append(cp.tags, t)
	return cp
}

func (e *appErr) WithContext(ctx string) Problem {
	if ctx == "" {
		return e
	}
	cp := e.clone()
	cp.contexts = append(cp.contexts, ctx)
	return cp
}

func (e *appErr) clone() *appErr {
	tags := make([]Tag, len(e.tags))
	copy(tags, e.tags)
	ctxs := make([]string, len(e.contexts))
	copy(ctxs, e.contexts)
	details := make([]kvPair, len(e.details))
	copy(details, e.details)
	return &appErr{
		kind:        e.kind,
		message:     e.message,
		safeMessage: e.safeMessage,
		httpStatus:  e.httpStatus,
		isBiz:       e.isBiz,
		traceID:     e.traceID,
		tags:        tags,
		contexts:    ctxs,
		details:     details,
		cause:       e.cause,
	}
}
