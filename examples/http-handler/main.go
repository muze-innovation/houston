// Package main demonstrates integrating houston with an HTTP service.
// It shows the recommended pattern for:
//   - translating upstream failures into typed Problems at the infra layer
//   - interpreting infra results at the usecase layer
//   - rendering Problems into JSON responses at the handler layer
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/muze-innovation/houston"
)

// ── Startup ───────────────────────────────────────────────────────────────────

func init() {
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
}

// ── Infrastructure layer ──────────────────────────────────────────────────────
// Infra knows what happened — not what it means to the business.
// Translate transport and upstream failures into typed Problems here.

func fetchUser(r *http.Request, client *http.Client, userID string) (string, error) {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		"https://identity.internal/v1/users/"+userID, nil)

	resp, err := client.Do(req)
	if err != nil {
		return "", houston.NetworkError(r.Context(),
			"identity-svc", http.MethodGet, "/v1/users/"+userID, "FetchUser", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		body, _ := io.ReadAll(resp.Body)
		return string(body), nil
	case http.StatusNotFound:
		// Upstream confirmed the resource does not exist.
		return "", houston.ResourceNotFound(r.Context(), "user", userID)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", houston.UnexpectedResponse(r.Context(),
			"identity-svc", resp.StatusCode, string(body))
	}
}

func chargeUser(r *http.Request, client *http.Client, userID string, amount int) error {
	payload := fmt.Sprintf(`{"user_id":%q,"amount":%d}`, userID, amount)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"https://payment.internal/v1/charges",
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return houston.NetworkError(r.Context(),
			"payment-svc", http.MethodPost, "/v1/charges", "ChargeUser", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusConflict:
		return houston.Conflict(r.Context(),
			"charge", userID, "charge already submitted for this session")
	case http.StatusTooManyRequests:
		// Bubble upstream 429 as-is — don't collapse it to 502.
		return houston.RateLimited(r.Context(), 30).
			Tag(houston.BubbleStatus{Status: http.StatusTooManyRequests})
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return houston.UnexpectedResponse(r.Context(),
			"payment-svc", resp.StatusCode, string(body))
	}
}

// ── Response layer ────────────────────────────────────────────────────────────
// Translates any Problem into a JSON HTTP response.
// Reads tags to apply cross-cutting behavior (status override, log suppression).

func writeError(w http.ResponseWriter, logger *slog.Logger, p houston.Problem) {
	status := p.HTTPStatus()
	var suppress bool

	for _, tag := range p.Tags() {
		switch t := tag.(type) {
		case houston.BubbleStatus:
			status = t.Status
		case houston.SuppressLog:
			suppress = true
		case houston.AlertOncall:
			logger.Error("ONCALL ALERT", slog.String("error", p.Error()))
		}
	}

	if !suppress {
		logger.Error(p.Message(),
			slog.String("kind", p.Kind()),
			slog.String("trace_id", p.TraceID()),
			slog.Int("http_status", status),
		)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"message": p.Message(),
		"kind":    p.Kind(),
		"code":    houston.ResolveCode(p),
		"trace":   p.TraceID(),
	})
}

// ── Handler layer ─────────────────────────────────────────────────────────────

func checkoutHandler(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			writeError(w, logger, houston.BadInput(ctx, houston.Field{
				Name:   "user_id",
				Detail: "required",
			}))
			return
		}

		_, err := fetchUser(r, http.DefaultClient, userID)
		if err != nil {
			writeError(w, logger, err.(houston.Problem))
			return
		}

		if err := chargeUser(r, http.DefaultClient, userID, 100); err != nil {
			writeError(w, logger, err.(houston.Problem))
			return
		}

		w.WriteHeader(http.StatusCreated)
	})
}

// ── Demo ──────────────────────────────────────────────────────────────────────

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := checkoutHandler(logger)

	// Demo: missing user_id → 400 BadInput
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/checkout", nil)
	handler.ServeHTTP(w, r)

	fmt.Printf("Status: %d\n", w.Code)
	fmt.Printf("Body:   %s", w.Body.String())
	// Status: 400
	// Body:   {"code":4001,"kind":"bad_input","message":"invalid input: user_id — required","trace":""}
}
