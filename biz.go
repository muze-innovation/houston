package houston

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Field represents a single invalid input field with its validation detail.
type Field struct {
	Name   string
	Detail string
}

// BadInput signals that the caller provided one or more invalid field values.
// Accepts multiple Field entries — all failures are reported in a single error.
//
//	houston.BadInput(ctx, houston.Field{Name: "email", Detail: "must be valid"})
//	→ "invalid input: email — must be valid"
//
//	houston.BadInput(ctx, fieldA, fieldB)
//	→ "invalid input: email — must be valid; phone — must be 10 digits"
func BadInput(ctx context.Context, fields ...Field) Problem {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = f.Name + " — " + f.Detail
	}
	return newErr(
		ctx,
		KindBadInput,
		"invalid input: "+strings.Join(parts, "; "),
		http.StatusBadRequest,
		true, nil,
	)
}

// ResourceNotFound signals that a requested resource does not exist.
//
//	houston.ResourceNotFound(ctx, "user", "uid-abc123")
//	→ "user not found: uid-abc123"
func ResourceNotFound(ctx context.Context, resourceType, id string) Problem {
	return newErr(
		ctx,
		KindResourceNotFound,
		fmt.Sprintf("%s not found: %s", resourceType, id),
		http.StatusNotFound,
		true, nil,
	).
		addDetail("resource_type", resourceType).
		addDetail("resource_identifier", id)
}

// Unauthorized signals that the request lacks valid authentication credentials.
//
//	houston.Unauthorized(ctx, "token expired")
//	→ "unauthorized: token expired"
func Unauthorized(ctx context.Context, reason string) Problem {
	return newErr(
		ctx,
		KindUnauthorized,
		fmt.Sprintf("unauthorized: %s", reason),
		http.StatusUnauthorized,
		true, nil,
	)
}

// Forbidden signals that the authenticated caller is not permitted to perform the action.
//
//	houston.Forbidden(ctx, "delete", "other user's profile")
//	→ "forbidden: cannot delete on other user's profile"
func Forbidden(ctx context.Context, action, resource string) Problem {
	return newErr(
		ctx,
		KindForbidden,
		fmt.Sprintf("forbidden: cannot %s on %s", action, resource),
		http.StatusForbidden,
		true, nil,
	).
		addDetail("action", action).
		addDetail("resource", resource)
}

// Conflict signals that the operation cannot proceed due to a conflicting resource state.
// collisionIdentity identifies the specific resource or key that caused the conflict.
//
//	houston.Conflict(ctx, "vote", "vote-round-42", "already submitted for this round")
//	→ "vote conflict [collision=vote-round-42]: already submitted for this round"
func Conflict(ctx context.Context, resourceType, collisionIdentity, detail string) Problem {
	return newErr(
		ctx,
		KindConflict,
		fmt.Sprintf("%s conflict [collision=%s]: %s", resourceType, collisionIdentity, detail),
		http.StatusConflict,
		true, nil,
	)
}

// RateLimited signals that the caller has exceeded the allowed request rate.
// Pass retryAfterSeconds = 0 if the retry window is unknown.
//
//	houston.RateLimited(ctx, 60)
//	→ "rate limited, retry after 60s"
func RateLimited(ctx context.Context, retryAfterSeconds int) Problem {
	var msg string
	if retryAfterSeconds > 0 {
		msg = fmt.Sprintf("rate limited, retry after %ds", retryAfterSeconds)
	} else {
		msg = "rate limited"
	}
	p := newErr(
		ctx,
		KindRateLimited,
		msg,
		http.StatusTooManyRequests,
		true, nil,
	).
		addDetail("after_seconds", strconv.Itoa(retryAfterSeconds))
	if retryAfterSeconds > 0 {
		return p.Tag(RetryAfter{Seconds: retryAfterSeconds})
	}
	return p
}

// UnexpectedValue signals that a value was present but did not match the expected shape or range.
//
//	houston.UnexpectedValue(ctx, "status", "active|inactive", "deleted")
//	→ "unexpected value for status: expected active|inactive, got deleted"
func UnexpectedValue(ctx context.Context, valueName, expected, butGot string) Problem {
	return newErr(
		ctx,
		KindUnexpectedValue,
		fmt.Sprintf("unexpected value for %s: expected %s, got %s", valueName, expected, butGot),
		http.StatusUnprocessableEntity,
		true, nil,
	).
		addDetail("value_name", valueName).
		addDetail("expected", expected).
		addDetail("but_got", butGot)
}
