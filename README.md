# Houston

> ["Houston, we have a problem."](https://en.wikipedia.org/wiki/Houston,_we_have_a_problem)

Application-aware error system for Go. The ergonomic way to declare that something went wrong.

```go
import "github.com/muze-innovation/houston"

// business errors
houston.Unauthorized(ctx, "token expired")
houston.ResourceNotFound(ctx, "user", uid)
houston.BadInput(ctx, houston.Field{Name: "email", Detail: "must be valid"})

// technical errors
houston.StorageError(ctx, "postgres", "insert", msg, err)
houston.Timeout(ctx, "identity-svc", "ExchangeToken")
```

---

## What this is

Single import. Named constructors. Every error carries its HTTP status, a semantic kind, and an optional trace ID — no numeric codes scattered at call sites.

The package does **not** own how errors are written to HTTP responses. It provides the data; your response layer decides how to render it.

---

## Installation

```sh
go get github.com/muze-innovation/houston
```

---

## Startup configuration

Call all `Set*` functions once in `main()` before serving requests. They are safe for concurrent use after that.

### Trace extractor

Registers a function houston calls at error creation time to capture the current correlation ID.

```go
type correlationKey struct{}

houston.SetTraceExtractor(func(ctx context.Context) string {
    id, _ := ctx.Value(correlationKey{}).(string)
    return id
})
```

### Code mapper

Maps semantic kind constants to numeric API codes in JSON response bodies.

```go
houston.SetMapper(
    houston.NewDefaultMapper(map[string]int32{
        houston.KindBadInput:           4001,
        houston.KindResourceNotFound:   4004,
        houston.KindUnauthorized:       4010,
        houston.KindForbidden:          4030,
        houston.KindConflict:           4090,
        houston.KindRateLimited:        4029,
        houston.KindUnexpectedValue:    4022,
        houston.KindUnexpectedResponse: 5003,
        houston.KindNetworkError:       5004,
        houston.KindStorageError:       5100,
        houston.KindCircuitOpen:        5003,
        houston.KindTimeout:            5021,
        houston.KindInternal:           5002,
    }).WithFallback(4000, 5000),
)
```

### Secure mode

Prevents internal details from leaking through `Message()` and `Details()` on technical errors.
`Error()` is unaffected — it stays full for structured logging.

```go
houston.EnableSecureMode()
```

In secure mode:

| Method | Business errors | Technical errors |
|---|---|---|
| `Message()` | unchanged | generic safe string (e.g. `"a storage error occurred"`) |
| `Details()` | unchanged | allowlisted keys only (`service_name`, `operation`, `data_source`, `upstream_status`) |
| `Error()` | unchanged | unchanged — full debug string for logs |

---

## Core interface

```go
type Problem interface {
    error                           // Error() — full debug string (includes TraceID, cause)
    Message() string                // user-facing message — safe to include in responses
    HTTPStatus() int                // HTTP status — fixed by constructor
    IsBusiness() bool               // true = 4xx, false = 5xx/502-504
    Kind() string                   // e.g. "unauthorized", "storage_error"
    TraceID() string                // correlation ID captured at creation
    Tags() []Tag                    // cross-cutting metadata
    Tag(t Tag) Problem              // fluent tag injection — returns new copy
    WithContext(ctx string) Problem // add call-site note — returns new copy
    Details() map[string]string     // structured key-value details (independent copy)
    Unwrap() error                  // stdlib error chain
}
```

---

## Error categories

| Category | HTTP range | When to use |
|---|---|---|
| Business | 4xx | Expected failure — bad input, not found, unauthorized. Caller can handle or surface to user. |
| Technical | 5xx / 502–504 | Unexpected failure — infra down, storage error, internal bug. |

`Problem.IsBusiness()` tells them apart.

---

## Business error constructors

All accept `ctx context.Context` as first argument for automatic TraceID capture.

| Constructor | HTTP status | Kind |
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
// Message: "invalid input: email — must be a valid email; phone — must be 10 digits"

houston.ResourceNotFound(ctx, "user", "uid-abc123")
// Message: "user not found: uid-abc123"

houston.Conflict(ctx, "vote", "vote-round-42", "already submitted for this round")
// Message: "vote conflict [collision=vote-round-42]: already submitted for this round"

houston.RateLimited(ctx, 60)
// Message:          "rate limited, retry after 60s"
// Auto-tags:        RetryAfter{Seconds: 60}
// Details:          {"after_seconds": "60"}

houston.RateLimited(ctx, 0)
// Message: "rate limited"   (no RetryAfter tag when window is unknown)
```

---

## Technical error constructors

| Constructor | HTTP status | Kind |
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
// HTTPStatus() = 503  (upstream status bubbled directly)

houston.NetworkError(ctx, "identity-svc", "POST", "/v1/token", "ExchangeToken", err)
// cause stored in error chain — errors.Is/As traversal works

houston.StorageError(ctx, "postgres", "insert", "duplicate email", err)
// Pass a sanitized message; raw driver error goes in cause (Unwrap)

houston.Timeout(ctx, "cms-svc", "GetProducts")
// Message: "timeout calling cms-svc during GetProducts"
```

---

## Passing errors through unchanged

Pass `Problem` as-is through the call stack. Re-wrapping in a new constructor destroys the original kind, message, and HTTP status.

```go
// correct — pass as-is
if err != nil {
    return nil, err
}

// correct — add call-site context
if err != nil {
    return nil, err.WithContext("UserRepository.FindByEmail")
}

// wrong — re-wrapping loses original kind and HTTP status
if err != nil {
    return nil, houston.Internal(ctx, "something went wrong") // ❌
}
```

---

## Tags — cross-cutting metadata

Typed structs, not strings. Attach at the call site; read at the response or logging layer using type switches.

```go
type ExternalCode struct{ Code int32 }  // numeric code from upstream API
type RetryAfter   struct{ Seconds int } // seconds before retry (auto-set by RateLimited)
type SuppressLog  struct{}              // skip log emission for expected errors
type AlertOncall  struct{}              // trigger oncall alert
type BubbleStatus struct{ Status int }  // override HTTP status sent to caller
```

```go
// suppress log noise for expected 404s
houston.ResourceNotFound(ctx, "session", sessionID).
    Tag(houston.SuppressLog{})

// forward upstream 429 as-is instead of collapsing to 502
houston.UnexpectedResponse(ctx, "identity-svc", 429, "rate limit exceeded").
    Tag(houston.BubbleStatus{Status: 429})

// pass through upstream numeric code
houston.UnexpectedResponse(ctx, "payment-svc", 400, resp.Message).
    Tag(houston.ExternalCode{Code: resp.ErrorCode})
```

Reading tags at the response layer:

```go
status := p.HTTPStatus()
for _, tag := range p.Tags() {
    switch t := tag.(type) {
    case houston.BubbleStatus:
        status = t.Status
    case houston.RetryAfter:
        w.Header().Set("Retry-After", strconv.Itoa(t.Seconds))
    case houston.SuppressLog:
        skipLog = true
    case houston.AlertOncall:
        alertOncall(p)
    }
}
```

---

## WithContext — call-site annotation

Annotates a `Problem` with a note as it propagates up. Does not change `Kind`, `HTTPStatus`, or `Message`. Returns a new copy — the original is unchanged.

```go
func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*User, error) {
    row, err := r.db.QueryRowContext(ctx, query, email)
    if err != nil {
        return nil, houston.StorageError(ctx, "postgres", "select", "query failed", err).
            WithContext("UserRepo.FindByEmail")
    }
    // ...
}
```

`Error()` output:
```
[storage_error] storage error on postgres during select: query failed
  trace=req-abc123 data_source=postgres operation=select (UserRepo.FindByEmail): <driver error>
```

`WithContext("")` is a no-op and returns the same instance.

---

## Details — structured key-value pairs

Each constructor attaches structured details for use in logs or response bodies.

```go
p := houston.ResourceNotFound(ctx, "order", "ord-999")
d := p.Details()
// d["resource_type"]       = "order"
// d["resource_identifier"] = "ord-999"

p := houston.NetworkError(ctx, "identity-svc", "POST", "/v1/token", "ExchangeToken", err)
d := p.Details()
// d["service_name"] = "identity-svc"
// d["method"]       = "POST"
// d["url"]          = "/v1/token"
// d["operation"]    = "ExchangeToken"
```

`Details()` always returns an independent copy — mutating it does not affect the `Problem`.

In secure mode, technical error details are filtered to an allowlist (`service_name`, `operation`, `data_source`, `upstream_status`) to prevent internal detail leakage.

---

## CodeMapper — numeric API codes

HTTP statuses are fixed per constructor. Numeric codes for JSON response bodies are app-specific — configure once at startup.

```go
houston.SetMapper(
    houston.NewDefaultMapper(map[string]int32{
        houston.KindBadInput:    4001,
        houston.KindInternal:    5002,
    }).
        WithFallback(4000, 5000). // bizFallback, techFallback
        Override(houston.KindBadInput, 4099), // update single entry after construction
)
```

Resolving at response time:

```go
code   := houston.ResolveCode(p)      // 0 if no mapper set
status := houston.ResolveHTTPStatus(p)
```

Custom mapper implementation:

```go
type myMapper struct{}

func (m myMapper) Map(p houston.Problem) int32 {
    for _, t := range p.Tags() {
        if ext, ok := t.(houston.ExternalCode); ok {
            return ext.Code
        }
    }
    if p.IsBusiness() {
        return 4000
    }
    return 5000
}

houston.SetMapper(myMapper{})
```

---

## Which layer raises which error

No compile-time enforcement — heuristic only.

**Infrastructure (repos, API clients):** technical errors. Infra knows *what happened*, not *what it means* to the business.

**Use cases:** interpret infra results. A `StorageError` or `UnexpectedResponse` from a repo may become `ResourceNotFound` or `Unauthorized` once the use case knows which resource was being looked up.

**Middleware:** `Unauthorized`, `Forbidden`, `RateLimited` — business meaning is unambiguous at the boundary.

**Adapters / handlers:** `BadInput` for request validation.

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
├── houston.go   — Problem interface, Tag types, Kind constants, secure mode, concrete implementation
├── mapper.go    — CodeMapper interface, DefaultMapper, SetMapper, ResolveCode
├── biz.go       — business error constructors (4xx)
├── tech.go      — technical error constructors (5xx / 502–504)
└── examples/
    ├── basic/          — constructors, tags, WithContext, logging pattern
    └── http-handler/   — full HTTP service integration (infra → usecase → handler)
```
