// Package main demonstrates core houston usage:
// creating problems, tagging them, and annotating with call-site context.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/muze-innovation/houston"
)

func main() {
	// ── Startup ──────────────────────────────────────────────────────────────

	// Register a trace extractor so every Problem captures the correlation ID
	// from the context at creation time.
	type correlationKey struct{}
	houston.SetTraceExtractor(func(ctx context.Context) string {
		id, _ := ctx.Value(correlationKey{}).(string)
		return id
	})

	// Register numeric API codes used in JSON response bodies.
	houston.SetMapper(
		houston.NewDefaultMapper(map[string]int32{
			houston.KindBadInput:         4001,
			houston.KindResourceNotFound: 4004,
			houston.KindUnauthorized:     4010,
			houston.KindForbidden:        4030,
			houston.KindConflict:         4090,
			houston.KindRateLimited:      4029,
			houston.KindUnexpectedValue:  4022,
		}).WithFallback(4000, 5000),
	)

	ctx := context.WithValue(context.Background(), correlationKey{}, "req-abc123")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// ── Business errors ───────────────────────────────────────────────────────

	badInput := houston.BadInput(ctx,
		houston.Field{Name: "email", Detail: "must be a valid email address"},
		houston.Field{Name: "age", Detail: "must be 18 or older"},
	)
	fmt.Println(badInput.Message())
	// invalid input: email — must be a valid email address; age — must be 18 or older

	notFound := houston.ResourceNotFound(ctx, "user", "uid-abc123")
	fmt.Println(notFound.Message())
	// user not found: uid-abc123

	fmt.Println(notFound.HTTPStatus()) // 404
	fmt.Println(notFound.IsBusiness()) // true
	fmt.Println(notFound.TraceID())    // req-abc123

	// ── Technical errors ──────────────────────────────────────────────────────

	rawErr := errors.New("dial tcp: connection refused")
	netErr := houston.NetworkError(ctx, "identity-svc", "POST", "/v1/token", "ExchangeToken", rawErr)
	fmt.Println(netErr.HTTPStatus())       // 502
	fmt.Println(errors.Is(netErr, rawErr)) // true — cause reachable via Unwrap

	// ── Tags — cross-cutting metadata ────────────────────────────────────────

	// Suppress log noise for expected 404s.
	suppressedNotFound := houston.ResourceNotFound(ctx, "session", "sess-xyz").
		Tag(houston.SuppressLog{})

	// Alert oncall for critical storage failures.
	criticalStorage := houston.StorageError(ctx, "postgres", "insert", "primary key sequence exhausted", nil).
		Tag(houston.AlertOncall{})

	// Forward upstream 429 as-is instead of collapsing to 502.
	rateLimitBubble := houston.UnexpectedResponse(ctx, "rate-limiter", 429, "quota exceeded").
		Tag(houston.BubbleStatus{Status: 429})

	for _, p := range []houston.Problem{suppressedNotFound, criticalStorage, rateLimitBubble} {
		for _, tag := range p.Tags() {
			switch tag.(type) {
			case houston.SuppressLog:
				fmt.Println("skip log")
			case houston.AlertOncall:
				fmt.Println("page oncall")
			case houston.BubbleStatus:
				fmt.Println("bubble HTTP status")
			}
		}
	}

	// ── WithContext — call-site annotation ───────────────────────────────────

	// Annotate as the error propagates up without changing kind, status, or message.
	p := houston.StorageError(ctx, "postgres", "select", "query failed", nil).
		WithContext("UserRepository.FindByEmail").
		WithContext("UserUsecase.GetProfile")

	fmt.Println(p.Kind())      // storage_error
	fmt.Println(p.HTTPStatus()) // 500
	fmt.Println(p.Error())
	// [storage_error] storage error on postgres during select: query failed
	//   trace=req-abc123 data_source=postgres operation=select message=query failed
	//   (UserRepository.FindByEmail) (UserUsecase.GetProfile)

	// ── Logging pattern ───────────────────────────────────────────────────────

	logger.Error(p.Message(),
		slog.String("kind", p.Kind()),
		slog.String("trace_id", p.TraceID()),
		slog.Int("http_status", p.HTTPStatus()),
		slog.Int("api_code", int(houston.ResolveCode(p))),
	)
}
