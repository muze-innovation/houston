# Houston

> ["Houston, we have a problem."](https://en.wikipedia.org/wiki/Houston,_we_have_a_problem)

Application-aware error system for Go. The ergonomic way to declare that something went wrong.

```go
import "github.com/muze-innovation/houston"

// business errors
houston.Unauthorized(ctx, "token expired")
houston.ResourceNotFound(ctx, "user", uid)
houston.BadInput(ctx, houston.Field{Name: "email", Detail: "must be valid"}, houston.Field{Name: "amount", Detail: "must be greater than 100"})

// technical errors
houston.StorageError(ctx, "postgres", "insert", msg, err)
houston.Timeout(ctx, "identity-svc", "ExchangeToken")
```

## What this is

Single import. Named constructors. Every error carries its HTTP status, a semantic kind, and an optional trace ID — no numeric codes scattered at call sites.

The package does **NOT** own how errors are written to HTTP responses. It provides the data; your response layer decides how to render it.

---

## Error categories

| Category | HTTP range | When to use |
|---|---|---|
| Business | 4xx | Expected failure — bad input, not found, unauthorized. Caller can handle or surface to user. |
| Technical | 5xx / 502–504 | Unexpected failure — infra down, storage error, internal bug. |

`Problem.IsBusiness()` tells them apart.

---

## Which layer raises which error

No compile-time enforcement — heuristic only.

### Guideline

In which layer it should return which errors? this is merely a suggestion, there is no constraints how would you apply it.

**Infrastructure (repos, API clients):** prefer technical errors. Infrastructure knows *what happened*, rarely *what it means* to the business.

**Usecases:** interpret infra results. A `StorageError` or `UnexpectedResponse` from a repo becomes `ResourceNotFound` or `Unauthorized` only once the usecase knows which resource was being looked up.

**Middleware:** `Unauthorized`, `Forbidden`, `RateLimited` — business meaning is unambiguous at the boundary.

**Adapters/handlers:** `BadInput` for request validation.

---

## Core interface

```go
type Problem interface {
    error                            // Error() — full debug string (includes TraceID)
    Message() string                 // user-facing message
    HTTPStatus() int                 // HTTP status — fixed by constructor
    IsBusiness() bool                // true = 4xx, false = 5xx/502-504
    Kind() string                    // e.g. "unauthorized", "storage_error"
    TraceID() string                 // correlation ID captured at creation
    Tags() []Tag                     // cross-cutting metadata
    Tag(t Tag) Problem              // fluent tag injection — returns new copy
    WithContext(ctx string) Problem // add call-site note — returns new copy
    Unwrap() error                   // stdlib error chain
}
```

### Pass errors through unchanged

```go
// correct — pass as-is
if err != nil {
    return nil, err
}

// correct — add context when useful
if err != nil {
    return nil, err.WithContext("GetUserProfile step 2")
}

// wrong — re-wrapping destroys original kind, message, and HTTP status
if err != nil {
    return nil, houston.Internal(ctx, "something went wrong") // ❌
}
```

---

## Business error constructors

All accept `ctx context.Context` as first argument for automatic TraceID capture.

| Constructor | Standard HTTP Status Map | Kind |
|---|---|---|
| `BadInput(ctx, ...Field)` | 400 | `bad_input` |
| `ResourceNotFound(ctx, resourceType, id)` | 404 | `resource_not_found` |
| `Unauthorized(ctx, reason)` | 401 | `unauthorized` |
| `Forbidden(ctx, action, resource)` | 403 | `forbidden` |
| `Conflict(ctx, resourceType, collisionIdentity, detail)` | 409 | `conflict` |
| `RateLimited(ctx, retryAfterSeconds)` | 429 | `rate_limited` |
| `UnexpectedValue(ctx, valueName, expected, butGot)` | 422 | `unexpected_value` |

```go
houston.BadInput(ctx,
    houston.Field{Name: "email", Detail: "must be a valid email"},
    houston.Field{Name: "phone", Detail: "must be 10 digits"},
)
// → "invalid input: email — must be a valid email; phone — must be 10 digits"

houston.ResourceNotFound(ctx, "user", "uid-abc123")
// → "user not found: uid-abc123"

houston.Conflict(ctx, "vote", "vote-round-42", "already submitted for this round")
// → "vote conflict [collision=vote-round-42]: already submitted for this round"
```

---

## Technical error constructors

| Constructor | Standard HTTP Status Map | Kind |
|---|---|---|
| `UnexpectedResponse(ctx, serviceName, upstreamStatus, upstreamMessage)` | bubbles upstream status | `unexpected_response` |
| `NetworkError(ctx, serviceName, method, url, operation, cause)` | 502 | `network_error` |
| `StorageError(ctx, datasource, operation, message, cause)` | 500 | `storage_error` |
| `CircuitOpen(ctx, serviceName)` | 503 | `circuit_open` |
| `Timeout(ctx, serviceName, operation)` | 504 | `timeout` |
| `ConfigMissing(ctx, key)` | 500 | `config_missing` |
| `Internal(ctx, reason)` | 500 | `internal` |

```go
houston.UnexpectedResponse(ctx, "identity-svc", 503, "gateway timeout")
// Problem.HTTPStatus() = 503 (upstream status bubbled directly)

houston.NetworkError(ctx, "identity-svc", "POST", "/v1/token", "ExchangeToken", err)
// cause stored in error chain — errors.Is/As traversal works

houston.Timeout(ctx, "cms-svc", "GetProducts")
// → "timeout calling cms-svc during GetProducts"
```

---

## TraceID — automatic correlation ID capture

Register an extractor once at startup. Houston calls it at error creation time.

```go
houston.SetTraceExtractor(func(ctx context.Context) string {
    id, _ := ctx.Value(middleware.CorrelationIDKey).(string)
    return id
})
```

- `err.TraceID()` — available for structured logging
- Included in `err.Error()` debug string as `trace=<id>`, never in `err.Message()`

```go
if appErr, ok := err.(houston.Problem); ok {
    log.Error(appErr.Message(),
        zap.String("trace_id", appErr.TraceID()),
        zap.String("kind", appErr.Kind()),
    )
}
```

---

## Tags — cross-cutting metadata

Typed structs, not strings. Use type switches at the response or logging layer.

```go
type ExternalCode struct{ Code int32 }  // numeric code from upstream API
type RetryAfter   struct{ Seconds int } // how long to wait before retry
type SuppressLog  struct{}              // skip log emission (expected 404s, etc.)
type AlertOncall  struct{}              // trigger oncall alert
type BubbleStatus struct{ Status int }  // override HTTP status sent to caller
```

```go
// pass through upstream numeric code
houston.UnexpectedResponse(ctx, "identity-svc", resp.StatusCode, resp.Message).
    Tag(houston.ExternalCode{Code: resp.ErrorCode})

// suppress log for expected not-found
houston.ResourceNotFound(ctx, "session", sessionID).
    Tag(houston.SuppressLog{})

// forward upstream 429 as-is instead of collapsing to 502
houston.UnexpectedResponse(ctx, "identity-svc", 429, "rate limit exceeded").
    Tag(houston.BubbleStatus{Status: 429})
```

Reading tags at the response layer:

```go
for _, t := range err.Tags() {
    switch tag := t.(type) {
    case houston.ExternalCode:
        // use tag.Code as numeric error code in response body
    case houston.RetryAfter:
        c.Set("Retry-After", strconv.Itoa(tag.Seconds))
    case houston.SuppressLog:
        // skip logging
    }
}
```

---

## CodeMapper — numeric API codes

HTTP statuses are fixed per constructor. Numeric codes in the JSON response body vary per app. Configure once at startup:

```go
houston.SetMapper(
    houston.NewDefaultMapper(map[string]int32{
        houston.KindUnauthorized:       10000,
        houston.KindResourceNotFound:   10005,
        houston.KindBadInput:           10004,
        houston.KindUnexpectedValue:    10004,
        houston.KindRateLimited:        10020,
        houston.KindTimeout:            10021,
        houston.KindCircuitOpen:        15004,
        houston.KindUnexpectedResponse: 15003,
        houston.KindStorageError:       40100,
        houston.KindInternal:           15002,
        houston.KindConfigMissing:      15002,
    }).WithFallback(10004, 15002),
)
```

Resolving at response time:

```go
code := houston.ResolveCode(err)   // 0 if no mapper set
status := houston.ResolveHTTPStatus(err)
```

Custom mapper implementation:

```go
type myMapper struct{}
func (m myMapper) Map(err houston.Problem) int32 {
    for _, t := range err.Tags() {
        if ext, ok := t.(houston.ExternalCode); ok {
            return ext.Code
        }
    }
    if err.IsBusiness() { return 10004 }
    return 15002
}
houston.SetMapper(myMapper{})
```

---

## Kind constants

```go
// Business
houston.KindBadInput
houston.KindResourceNotFound
houston.KindUnauthorized
houston.KindForbidden
houston.KindConflict
houston.KindRateLimited
houston.KindUnexpectedValue

// Technical
houston.KindUnexpectedResponse
houston.KindNetworkError
houston.KindStorageError
houston.KindCircuitOpen
houston.KindTimeout
houston.KindConfigMissing
houston.KindInternal
```

---

## Package layout

```
houston/
├── go.mod
├── houston.go   — Problem interface, Tag types, Kind constants, concrete implementation
├── mapper.go    — CodeMapper interface, DefaultMapper, SetMapper, ResolveCode
├── biz.go       — business error constructors (4xx)
└── tech.go      — technical error constructors (5xx / 502–504)
```
