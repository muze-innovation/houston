package houston_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/muze-innovation/houston"
)

// helpers

type traceKey struct{}

func ctxWithTrace(id string) context.Context {
	return context.WithValue(context.Background(), traceKey{}, id)
}

func setupTraceExtractor(t *testing.T) {
	t.Helper()
	houston.SetTraceExtractor(func(ctx context.Context) string {
		id, _ := ctx.Value(traceKey{}).(string)
		return id
	})
	t.Cleanup(func() { houston.SetTraceExtractor(nil) })
}

func findTag[T houston.Tag](p houston.Problem) (T, bool) {
	for _, tag := range p.Tags() {
		if v, ok := tag.(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// --- business constructors ---

func TestBadInput(t *testing.T) {
	ctx := context.Background()

	t.Run("single field", func(t *testing.T) {
		p := houston.BadInput(ctx, houston.Field{Name: "email", Detail: "must be valid"})
		if p.Kind() != houston.KindBadInput {
			t.Errorf("Kind = %q, want %q", p.Kind(), houston.KindBadInput)
		}
		if p.HTTPStatus() != http.StatusBadRequest {
			t.Errorf("HTTPStatus = %d, want %d", p.HTTPStatus(), http.StatusBadRequest)
		}
		if !p.IsBusiness() {
			t.Error("IsBusiness = false, want true")
		}
		want := "invalid input: email — must be valid"
		if p.Message() != want {
			t.Errorf("Message = %q, want %q", p.Message(), want)
		}
	})

	t.Run("multiple fields joined with semicolon", func(t *testing.T) {
		p := houston.BadInput(ctx,
			houston.Field{Name: "email", Detail: "must be valid"},
			houston.Field{Name: "phone", Detail: "must be 10 digits"},
		)
		want := "invalid input: email — must be valid; phone — must be 10 digits"
		if p.Message() != want {
			t.Errorf("Message = %q, want %q", p.Message(), want)
		}
	})

	t.Run("empty fields produces empty message gracefully", func(t *testing.T) {
		p := houston.BadInput(ctx)
		if p.Kind() != houston.KindBadInput {
			t.Errorf("Kind = %q, want %q", p.Kind(), houston.KindBadInput)
		}
	})
}

func TestResourceNotFound(t *testing.T) {
	ctx := context.Background()
	p := houston.ResourceNotFound(ctx, "user", "uid-123")

	if p.Kind() != houston.KindResourceNotFound {
		t.Errorf("Kind = %q, want %q", p.Kind(), houston.KindResourceNotFound)
	}
	if p.HTTPStatus() != http.StatusNotFound {
		t.Errorf("HTTPStatus = %d, want %d", p.HTTPStatus(), http.StatusNotFound)
	}
	if p.Message() != "user not found: uid-123" {
		t.Errorf("Message = %q", p.Message())
	}
	d := p.Details()
	if d["resource_type"] != "user" {
		t.Errorf("detail resource_type = %q", d["resource_type"])
	}
	if d["resource_identifier"] != "uid-123" {
		t.Errorf("detail resource_identifier = %q", d["resource_identifier"])
	}
}

func TestUnauthorized(t *testing.T) {
	p := houston.Unauthorized(context.Background(), "token expired")
	if p.Kind() != houston.KindUnauthorized {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != http.StatusUnauthorized {
		t.Errorf("HTTPStatus = %d", p.HTTPStatus())
	}
	if p.Message() != "unauthorized: token expired" {
		t.Errorf("Message = %q", p.Message())
	}
}

func TestForbidden(t *testing.T) {
	p := houston.Forbidden(context.Background(), "delete", "other user's profile")
	if p.Kind() != houston.KindForbidden {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != http.StatusForbidden {
		t.Errorf("HTTPStatus = %d", p.HTTPStatus())
	}
	d := p.Details()
	if d["action"] != "delete" {
		t.Errorf("detail action = %q", d["action"])
	}
	if d["resource"] != "other user's profile" {
		t.Errorf("detail resource = %q", d["resource"])
	}
}

func TestConflict(t *testing.T) {
	p := houston.Conflict(context.Background(), "vote", "vote-round-42", "already submitted")
	if p.Kind() != houston.KindConflict {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != http.StatusConflict {
		t.Errorf("HTTPStatus = %d", p.HTTPStatus())
	}
	want := "vote conflict [collision=vote-round-42]: already submitted"
	if p.Message() != want {
		t.Errorf("Message = %q, want %q", p.Message(), want)
	}
}

func TestRateLimited(t *testing.T) {
	ctx := context.Background()

	t.Run("with retry seconds auto-tags RetryAfter", func(t *testing.T) {
		p := houston.RateLimited(ctx, 60)
		if p.HTTPStatus() != http.StatusTooManyRequests {
			t.Errorf("HTTPStatus = %d", p.HTTPStatus())
		}
		if p.Message() != "rate limited, retry after 60s" {
			t.Errorf("Message = %q", p.Message())
		}
		tag, ok := findTag[houston.RetryAfter](p)
		if !ok {
			t.Fatal("RetryAfter tag missing")
		}
		if tag.Seconds != 60 {
			t.Errorf("RetryAfter.Seconds = %d, want 60", tag.Seconds)
		}
		if p.Details()["after_seconds"] != "60" {
			t.Errorf("detail after_seconds = %q", p.Details()["after_seconds"])
		}
	})

	t.Run("zero seconds no RetryAfter tag", func(t *testing.T) {
		p := houston.RateLimited(ctx, 0)
		if p.Message() != "rate limited" {
			t.Errorf("Message = %q", p.Message())
		}
		_, ok := findTag[houston.RetryAfter](p)
		if ok {
			t.Error("RetryAfter tag present, want absent")
		}
	})
}

func TestUnexpectedValue(t *testing.T) {
	p := houston.UnexpectedValue(context.Background(), "status", "active|inactive", "deleted")
	if p.Kind() != houston.KindUnexpectedValue {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != http.StatusUnprocessableEntity {
		t.Errorf("HTTPStatus = %d", p.HTTPStatus())
	}
	d := p.Details()
	if d["value_name"] != "status" || d["expected"] != "active|inactive" || d["but_got"] != "deleted" {
		t.Errorf("details = %v", d)
	}
}

// --- technical constructors ---

func TestUnexpectedResponse(t *testing.T) {
	p := houston.UnexpectedResponse(context.Background(), "identity-svc", 503, "gateway timeout")
	if p.Kind() != houston.KindUnexpectedResponse {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != 503 {
		t.Errorf("HTTPStatus = %d, want 503", p.HTTPStatus())
	}
	if p.IsBusiness() {
		t.Error("IsBusiness = true, want false")
	}
	d := p.Details()
	if d["service_name"] != "identity-svc" {
		t.Errorf("detail service_name = %q", d["service_name"])
	}
}

func TestNetworkError(t *testing.T) {
	cause := errors.New("connection refused")
	p := houston.NetworkError(context.Background(), "identity-svc", "POST", "/v1/token", "ExchangeToken", cause)
	if p.Kind() != houston.KindNetworkError {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != http.StatusBadGateway {
		t.Errorf("HTTPStatus = %d", p.HTTPStatus())
	}
	if !errors.Is(p, cause) {
		t.Error("errors.Is: cause not in chain")
	}
}

func TestStorageError(t *testing.T) {
	cause := errors.New("duplicate key")
	p := houston.StorageError(context.Background(), "postgres", "insert", "duplicate key on users.email", cause)
	if p.Kind() != houston.KindStorageError {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != http.StatusInternalServerError {
		t.Errorf("HTTPStatus = %d", p.HTTPStatus())
	}
	if !errors.Is(p, cause) {
		t.Error("errors.Is: cause not in chain")
	}
}

func TestCircuitOpen(t *testing.T) {
	p := houston.CircuitOpen(context.Background(), "profile-svc")
	if p.Kind() != houston.KindCircuitOpen {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != http.StatusServiceUnavailable {
		t.Errorf("HTTPStatus = %d", p.HTTPStatus())
	}
}

func TestTimeout(t *testing.T) {
	p := houston.Timeout(context.Background(), "cms-svc", "GetProducts")
	if p.Kind() != houston.KindTimeout {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != http.StatusGatewayTimeout {
		t.Errorf("HTTPStatus = %d", p.HTTPStatus())
	}
}

func TestConfigMissing(t *testing.T) {
	p := houston.ConfigMissing(context.Background(), "REDIS_ADDR")
	if p.Kind() != houston.KindConfigMissing {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.Message() != "missing required configuration: REDIS_ADDR" {
		t.Errorf("Message = %q", p.Message())
	}
}

func TestInternal(t *testing.T) {
	p := houston.Internal(context.Background(), "mapper returned nil")
	if p.Kind() != houston.KindInternal {
		t.Errorf("Kind = %q", p.Kind())
	}
	if p.HTTPStatus() != http.StatusInternalServerError {
		t.Errorf("HTTPStatus = %d", p.HTTPStatus())
	}
}

// --- Problem behaviour ---

func TestTag_ReturnsCopy(t *testing.T) {
	p := houston.Unauthorized(context.Background(), "expired")
	p2 := p.Tag(houston.SuppressLog{})

	if len(p.Tags()) != 0 {
		t.Error("original mutated after Tag()")
	}
	if len(p2.Tags()) != 1 {
		t.Errorf("p2 tags len = %d, want 1", len(p2.Tags()))
	}
}

func TestTag_Chaining(t *testing.T) {
	p := houston.Internal(context.Background(), "reason").
		Tag(houston.SuppressLog{}).
		Tag(houston.AlertOncall{})

	if len(p.Tags()) != 2 {
		t.Errorf("tags len = %d, want 2", len(p.Tags()))
	}
}

func TestWithContext_ReturnsCopy(t *testing.T) {
	p := houston.Unauthorized(context.Background(), "expired")
	p2 := p.WithContext("step A")

	errStr := p.Error()
	if contains(errStr, "step A") {
		t.Error("original mutated after WithContext()")
	}
	if !contains(p2.Error(), "step A") {
		t.Error("p2 missing context annotation")
	}
}

func TestWithContext_EmptyStringNoOp(t *testing.T) {
	p := houston.Unauthorized(context.Background(), "expired")
	p2 := p.WithContext("")
	if p != p2 {
		t.Error("WithContext(\"\") should return same instance")
	}
}

func TestWithContext_MultipleAnnotations(t *testing.T) {
	p := houston.Internal(context.Background(), "reason").
		WithContext("step A").
		WithContext("step B")

	s := p.Error()
	if !contains(s, "(step A)") || !contains(s, "(step B)") {
		t.Errorf("Error() missing context annotations: %q", s)
	}
}

func TestDetails(t *testing.T) {
	p := houston.ResourceNotFound(context.Background(), "order", "ord-999")
	d := p.Details()
	if d["resource_type"] != "order" {
		t.Errorf("resource_type = %q", d["resource_type"])
	}
	if d["resource_identifier"] != "ord-999" {
		t.Errorf("resource_identifier = %q", d["resource_identifier"])
	}
	// mutating returned map must not affect original
	d["resource_type"] = "tampered"
	d2 := p.Details()
	if d2["resource_type"] == "tampered" {
		t.Error("Details() returned live reference, expected independent copy")
	}
}

func TestUnwrap_ErrorChain(t *testing.T) {
	sentinel := errors.New("root cause")
	p := houston.StorageError(context.Background(), "pg", "select", "query failed", sentinel)
	if !errors.Is(p, sentinel) {
		t.Error("errors.Is did not find sentinel through Unwrap chain")
	}
}

func TestUnwrap_NilCause(t *testing.T) {
	p := houston.Unauthorized(context.Background(), "expired")
	if p.Unwrap() != nil {
		t.Error("Unwrap() should be nil when no cause")
	}
}

func TestError_ContainsKindAndMessage(t *testing.T) {
	p := houston.Unauthorized(context.Background(), "token expired")
	s := p.Error()
	if !contains(s, "[unauthorized]") {
		t.Errorf("Error() missing kind: %q", s)
	}
	if !contains(s, "token expired") {
		t.Errorf("Error() missing message: %q", s)
	}
}

func TestError_ContainsTraceID(t *testing.T) {
	setupTraceExtractor(t)
	ctx := ctxWithTrace("trace-abc")
	p := houston.Internal(ctx, "reason")
	if !contains(p.Error(), "trace=trace-abc") {
		t.Errorf("Error() missing traceID: %q", p.Error())
	}
}

func TestError_ContainsCause(t *testing.T) {
	cause := errors.New("disk full")
	p := houston.StorageError(context.Background(), "pg", "insert", "write failed", cause)
	if !contains(p.Error(), "disk full") {
		t.Errorf("Error() missing cause: %q", p.Error())
	}
}

// --- TraceID ---

func TestTraceID_ExtractedAtCreation(t *testing.T) {
	setupTraceExtractor(t)
	ctx := ctxWithTrace("req-xyz")
	p := houston.Internal(ctx, "reason")
	if p.TraceID() != "req-xyz" {
		t.Errorf("TraceID = %q, want %q", p.TraceID(), "req-xyz")
	}
}

func TestTraceID_EmptyWhenNoExtractor(t *testing.T) {
	houston.SetTraceExtractor(nil)
	p := houston.Internal(context.Background(), "reason")
	if p.TraceID() != "" {
		t.Errorf("TraceID = %q, want empty", p.TraceID())
	}
}

func TestTraceID_EmptyWhenNilContext(t *testing.T) {
	setupTraceExtractor(t)
	p := houston.Internal(nil, "reason") //nolint:staticcheck // intentional: testing nil-ctx guard in ExtractTrace
	if p.TraceID() != "" {
		t.Errorf("TraceID = %q, want empty", p.TraceID())
	}
}

func TestSetTraceExtractor_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			houston.SetTraceExtractor(func(ctx context.Context) string {
				return fmt.Sprintf("trace-%d", i)
			})
			houston.Internal(context.Background(), "reason")
		}(i)
	}
	wg.Wait()
	houston.SetTraceExtractor(nil)
}

// --- DefaultMapper ---

func TestDefaultMapper_KnownKind(t *testing.T) {
	m := houston.NewDefaultMapper(map[string]int32{
		houston.KindBadInput: 4001,
	})
	p := houston.BadInput(context.Background(), houston.Field{Name: "x", Detail: "required"})
	if code := m.Map(p); code != 4001 {
		t.Errorf("Map = %d, want 4001", code)
	}
}

func TestDefaultMapper_FallbackBiz(t *testing.T) {
	m := houston.NewDefaultMapper(nil).WithFallback(4000, 5000)
	p := houston.BadInput(context.Background())
	if code := m.Map(p); code != 4000 {
		t.Errorf("Map = %d, want 4000 (biz fallback)", code)
	}
}

func TestDefaultMapper_FallbackTech(t *testing.T) {
	m := houston.NewDefaultMapper(nil).WithFallback(4000, 5000)
	p := houston.Internal(context.Background(), "reason")
	if code := m.Map(p); code != 5000 {
		t.Errorf("Map = %d, want 5000 (tech fallback)", code)
	}
}

func TestDefaultMapper_Override(t *testing.T) {
	m := houston.NewDefaultMapper(map[string]int32{
		houston.KindBadInput: 4001,
	})
	m.Override(houston.KindBadInput, 4099)
	p := houston.BadInput(context.Background())
	if code := m.Map(p); code != 4099 {
		t.Errorf("Map after Override = %d, want 4099", code)
	}
}

func TestDefaultMapper_OverrideConcurrent(t *testing.T) {
	m := houston.NewDefaultMapper(map[string]int32{
		houston.KindBadInput: 4001,
	})
	p := houston.BadInput(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			m.Override(houston.KindBadInput, int32(4000+i))
		}(i)
		go func() {
			defer wg.Done()
			m.Map(p)
		}()
	}
	wg.Wait()
}

func TestDefaultMapper_InputMapNotMutated(t *testing.T) {
	input := map[string]int32{houston.KindBadInput: 4001}
	houston.NewDefaultMapper(input)
	input[houston.KindBadInput] = 9999
	m := houston.NewDefaultMapper(map[string]int32{houston.KindBadInput: 4001})
	p := houston.BadInput(context.Background())
	if code := m.Map(p); code != 4001 {
		t.Errorf("Map = %d, original map mutation leaked in", code)
	}
}

// --- ResolveCode ---

func TestResolveCode_NoMapper(t *testing.T) {
	houston.SetMapper(nil)
	p := houston.BadInput(context.Background())
	if code := houston.ResolveCode(p); code != 0 {
		t.Errorf("ResolveCode = %d, want 0 when no mapper", code)
	}
}

func TestResolveCode_WithMapper(t *testing.T) {
	m := houston.NewDefaultMapper(map[string]int32{houston.KindBadInput: 4001})
	houston.SetMapper(m)
	t.Cleanup(func() { houston.SetMapper(nil) })
	p := houston.BadInput(context.Background())
	if code := houston.ResolveCode(p); code != 4001 {
		t.Errorf("ResolveCode = %d, want 4001", code)
	}
}

// --- helpers ---

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
