package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

// ---------------------------------------------------------------------------
// URL construction tests
// ---------------------------------------------------------------------------

func TestHTTPTransport_URLConstruction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"GET", "GET", "/api/v1/jobs"},
		{"POST", "POST", "/api/v1/operator/messages"},
		{"PUT", "PUT", "/api/v1/teams/team-1/coordinator"},
		{"DELETE", "DELETE", "/api/v1/skills/skill-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotURL = r.URL.String()
				if r.Method != tt.method {
					t.Errorf("method = %q, want %q", r.Method, tt.method)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			transport := &httpTransport{
				client:  srv.Client(),
				baseURL: srv.URL,
			}

			ctx := context.Background()
			var resp *http.Response
			var err error

			switch tt.method {
			case "GET":
				resp, err = transport.get(ctx, tt.path)
			case "POST":
				resp, err = transport.post(ctx, tt.path, nil)
			case "PUT":
				resp, err = transport.put(ctx, tt.path, nil)
			case "DELETE":
				resp, err = transport.delete(ctx, tt.path)
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_ = resp.Body.Close()

			if gotURL != tt.path {
				t.Errorf("URL = %q, want %q", gotURL, tt.path)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Request header tests
// ---------------------------------------------------------------------------

func TestHTTPTransport_RequestHeaders_GET(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want %q", got, "application/json")
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want empty for GET", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.get(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
}

func TestHTTPTransport_RequestHeaders_POST(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want %q", got, "application/json")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want %q", got, "application/json")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.post(context.Background(), "/test", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
}

func TestHTTPTransport_RequestHeaders_PUT(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want %q", got, "application/json")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want %q", got, "application/json")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.put(context.Background(), "/test", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
}

func TestHTTPTransport_RequestHeaders_DELETE(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want %q", got, "application/json")
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want empty for DELETE", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.delete(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Request body encoding tests
// ---------------------------------------------------------------------------

func TestHTTPTransport_RequestBodyEncoding_POST(t *testing.T) {
	t.Parallel()

	type testBody struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}

		var got testBody
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshaling body: %v", err)
		}

		if got.Name != "test" {
			t.Errorf("body.Name = %q, want %q", got.Name, "test")
		}
		if got.Count != 42 {
			t.Errorf("body.Count = %d, want 42", got.Count)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.post(context.Background(), "/test", testBody{Name: "test", Count: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
}

func TestHTTPTransport_RequestBodyEncoding_PUT(t *testing.T) {
	t.Parallel()

	type testBody struct {
		Value string `json:"value"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}

		var got testBody
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshaling body: %v", err)
		}

		if got.Value != "updated" {
			t.Errorf("body.Value = %q, want %q", got.Value, "updated")
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.put(context.Background(), "/test", testBody{Value: "updated"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
}

// ---------------------------------------------------------------------------
// decodeResponse tests
// ---------------------------------------------------------------------------

func TestDecodeResponse_Success(t *testing.T) {
	t.Parallel()

	type result struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(result{ID: "123", Name: "test"})
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.get(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := decodeResponse[result](resp)
	if err != nil {
		t.Fatalf("decodeResponse error: %v", err)
	}

	if got.ID != "123" {
		t.Errorf("ID = %q, want %q", got.ID, "123")
	}
	if got.Name != "test" {
		t.Errorf("Name = %q, want %q", got.Name, "test")
	}
}

func TestDecodeResponse_204NoContent(t *testing.T) {
	t.Parallel()

	type result struct {
		ID string `json:"id"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.get(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := decodeResponse[result](resp)
	if err != nil {
		t.Fatalf("decodeResponse error: %v", err)
	}

	// Should return zero value.
	if got.ID != "" {
		t.Errorf("ID = %q, want empty (zero value)", got.ID)
	}
}

// ---------------------------------------------------------------------------
// decodeNoContent tests
// ---------------------------------------------------------------------------

func TestDecodeNoContent_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.delete(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := decodeNoContent(resp); err != nil {
		t.Fatalf("decodeNoContent error: %v", err)
	}
}

func TestDecodeNoContent_200OK(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.delete(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := decodeNoContent(resp); err != nil {
		t.Fatalf("decodeNoContent error: %v", err)
	}
}

func TestDecodeNoContent_Error(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(errorResponse{
			Error: errorDetail{Code: "not_found", Message: "resource not found"},
		})
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.delete(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = decodeNoContent(resp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("error should wrap ErrNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error mapping tests
// ---------------------------------------------------------------------------

func TestErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		wantErr    error
	}{
		{"404 → ErrNotFound", http.StatusNotFound, service.ErrNotFound},
		{"409 → ErrConflict", http.StatusConflict, ErrConflict},
		{"422 → ErrUnprocessable", http.StatusUnprocessableEntity, ErrUnprocessable},
		{"429 → ErrRateLimited", http.StatusTooManyRequests, ErrRateLimited},
		{"500 → ErrServerError", http.StatusInternalServerError, ErrServerError},
		{"503 → ErrServiceUnavailable", http.StatusServiceUnavailable, ErrServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(errorResponse{
					Error: errorDetail{Code: "test", Message: "test error"},
				})
			}))
			defer srv.Close()

			transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
			resp, err := transport.get(context.Background(), "/test")
			if err != nil {
				t.Fatalf("unexpected transport error: %v", err)
			}

			type dummy struct{}
			_, err = decodeResponse[dummy](resp)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tt.wantErr, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error message extraction tests
// ---------------------------------------------------------------------------

func TestErrorMessageExtraction(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(errorResponse{
			Error: errorDetail{Code: "not_found", Message: "skill 'foo' not found"},
		})
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.get(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	type dummy struct{}
	_, err = decodeResponse[dummy](resp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "skill 'foo' not found") {
		t.Errorf("error message should contain server message, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Non-JSON error body tests
// ---------------------------------------------------------------------------

func TestErrorMapping_NonJSONBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("something went wrong"))
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.get(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	type dummy struct{}
	_, err = decodeResponse[dummy](resp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, ErrServerError) {
		t.Errorf("error should wrap ErrServerError, got: %v", err)
	}

	// Should fall back to HTTP status text when JSON decode fails.
	if !strings.Contains(err.Error(), "Internal Server Error") {
		t.Errorf("error should contain status text fallback, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Connection refused tests
// ---------------------------------------------------------------------------

func TestConnectionRefused(t *testing.T) {
	t.Parallel()

	// Find a port that's not listening.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close() // Close immediately so nothing is listening.

	transport := &httpTransport{
		client:  &http.Client{},
		baseURL: "http://" + addr,
	}

	_, err = transport.get(context.Background(), "/test")
	if err == nil {
		t.Fatal("expected error for connection refused, got nil")
	}

	if !errors.Is(err, ErrConnectionFailed) {
		t.Errorf("error should wrap ErrConnectionFailed, got: %v", err)
	}

	if !isConnectionError(err) {
		t.Errorf("isConnectionError() = false, want true for: %v", err)
	}
}

func TestConnectionRefused_POST(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	transport := &httpTransport{
		client:  &http.Client{},
		baseURL: "http://" + addr,
	}

	_, err = transport.post(context.Background(), "/test", map[string]string{"key": "val"})
	if err == nil {
		t.Fatal("expected error for connection refused, got nil")
	}

	if !errors.Is(err, ErrConnectionFailed) {
		t.Errorf("error should wrap ErrConnectionFailed, got: %v", err)
	}
}

func TestConnectionRefused_DELETE(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	transport := &httpTransport{
		client:  &http.Client{},
		baseURL: "http://" + addr,
	}

	_, err = transport.delete(context.Background(), "/test")
	if err == nil {
		t.Fatal("expected error for connection refused, got nil")
	}

	if !errors.Is(err, ErrConnectionFailed) {
		t.Errorf("error should wrap ErrConnectionFailed, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation tests
// ---------------------------------------------------------------------------

func TestContextCancellation_GET(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This handler should never be reached.
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := transport.get(ctx, "/test")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}

	if !errors.Is(err, context.Canceled) {
		// The error might be wrapped, check if it contains the context error.
		if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("error should indicate context cancellation, got: %v", err)
		}
	}
}

func TestContextCancellation_POST(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := transport.post(ctx, "/test", map[string]string{"key": "val"})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// ---------------------------------------------------------------------------
// POST with nil body tests
// ---------------------------------------------------------------------------

func TestHTTPTransport_POST_NilBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want empty for nil body", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		if len(body) != 0 {
			t.Errorf("body = %q, want empty for nil body", body)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.post(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
}

func TestHTTPTransport_PUT_NilBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want empty for nil body", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.put(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()
}

// ---------------------------------------------------------------------------
// isConnectionError tests
// ---------------------------------------------------------------------------

func TestIsConnectionError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"regular error", errors.New("something"), false},
		{"ErrConnectionFailed", ErrConnectionFailed, true},
		{"wrapped ErrConnectionFailed", errors.Join(ErrConnectionFailed, errors.New("details")), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isConnectionError(tt.err); got != tt.want {
				t.Errorf("isConnectionError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// asConnectionError tests
// ---------------------------------------------------------------------------

func TestAsConnectionError(t *testing.T) {
	t.Parallel()

	t.Run("nil error", func(t *testing.T) {
		t.Parallel()
		if got := asConnectionError(nil); got != nil {
			t.Errorf("asConnectionError(nil) = %v, want nil", got)
		}
	})

	t.Run("regular error", func(t *testing.T) {
		t.Parallel()
		if got := asConnectionError(errors.New("not a net error")); got != nil {
			t.Errorf("asConnectionError(regular) = %v, want nil", got)
		}
	})

	t.Run("net.OpError wraps as ErrConnectionFailed", func(t *testing.T) {
		t.Parallel()
		opErr := &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Addr: &net.TCPAddr{
				IP:   net.ParseIP("127.0.0.1"),
				Port: 9999,
			},
			Err: errors.New("connection refused"),
		}
		got := asConnectionError(opErr)
		if got == nil {
			t.Fatal("asConnectionError(net.OpError) = nil, want non-nil")
		}
		if !errors.Is(got, ErrConnectionFailed) {
			t.Errorf("error should wrap ErrConnectionFailed, got: %v", got)
		}
	})
}

// ---------------------------------------------------------------------------
// mapStatusToError tests
// ---------------------------------------------------------------------------

func TestMapStatusToError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		msg     string
		wantErr error
		wantMsg string
	}{
		{"404", http.StatusNotFound, "not found", service.ErrNotFound, "not found"},
		{"409", http.StatusConflict, "conflict", ErrConflict, "conflict"},
		{"422", http.StatusUnprocessableEntity, "bad input", ErrUnprocessable, "bad input"},
		{"429", http.StatusTooManyRequests, "slow down", ErrRateLimited, "slow down"},
		{"500", http.StatusInternalServerError, "oops", ErrServerError, "oops"},
		{"503", http.StatusServiceUnavailable, "down", ErrServiceUnavailable, "down"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := mapStatusToError(tt.status, tt.msg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tt.wantErr, err)
			}

			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error message should contain %q, got: %v", tt.wantMsg, err)
			}
		})
	}
}

func TestMapStatusToError_UnexpectedStatus(t *testing.T) {
	t.Parallel()

	err := mapStatusToError(418, "I'm a teapot")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if strings.Contains(err.Error(), "418") && strings.Contains(err.Error(), "I'm a teapot") {
		// Good — contains both status code and message.
	} else {
		t.Errorf("error should contain status and message, got: %v", err)
	}

	// Should NOT match any sentinel error.
	if errors.Is(err, service.ErrNotFound) || errors.Is(err, ErrConflict) ||
		errors.Is(err, ErrUnprocessable) || errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrServerError) || errors.Is(err, ErrServiceUnavailable) {
		t.Errorf("unexpected status error should not match any sentinel, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// extractErrorMessage tests
// ---------------------------------------------------------------------------

func TestExtractErrorMessage_ValidJSON(t *testing.T) {
	t.Parallel()

	body := `{"error":{"code":"not_found","message":"skill not found"}}`
	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	got := extractErrorMessage(resp)
	if got != "skill not found" {
		t.Errorf("extractErrorMessage = %q, want %q", got, "skill not found")
	}
}

func TestExtractErrorMessage_InvalidJSON(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader("not json")),
	}

	got := extractErrorMessage(resp)
	if got != "Internal Server Error" {
		t.Errorf("extractErrorMessage = %q, want %q", got, "Internal Server Error")
	}
}

func TestExtractErrorMessage_EmptyMessage(t *testing.T) {
	t.Parallel()

	body := `{"error":{"code":"err","message":""}}`
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	got := extractErrorMessage(resp)
	// Empty message should fall back to status text.
	if got != "Bad Request" {
		t.Errorf("extractErrorMessage = %q, want %q", got, "Bad Request")
	}
}

// ---------------------------------------------------------------------------
// decodeResponse error path tests
// ---------------------------------------------------------------------------

func TestDecodeResponse_ErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(errorResponse{
			Error: errorDetail{Code: "conflict", Message: "already exists"},
		})
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.get(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	type dummy struct{}
	_, err = decodeResponse[dummy](resp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, ErrConflict) {
		t.Errorf("error should wrap ErrConflict, got: %v", err)
	}

	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should contain server message, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Response envelope type tests
// ---------------------------------------------------------------------------

func TestPaginatedResponse_Decode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(paginatedResponse[wireJob]{
			Items: []wireJob{
				{ID: "job-1", Title: "Job 1", Status: "active", CreatedAt: testTime, UpdatedAt: testTime},
				{ID: "job-2", Title: "Job 2", Status: "pending", CreatedAt: testTime, UpdatedAt: testTime},
			},
			Total: 2,
		})
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.get(context.Background(), "/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := decodeResponse[paginatedResponse[wireJob]](resp)
	if err != nil {
		t.Fatalf("decodeResponse error: %v", err)
	}

	if got.Total != 2 {
		t.Errorf("Total = %d, want 2", got.Total)
	}
	if len(got.Items) != 2 {
		t.Fatalf("Items len = %d, want 2", len(got.Items))
	}
	if got.Items[0].ID != "job-1" {
		t.Errorf("Items[0].ID = %q, want %q", got.Items[0].ID, "job-1")
	}
}

func TestAsyncResponse_Decode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(asyncResponse{OperationID: "op-123"})
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.post(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := decodeResponse[asyncResponse](resp)
	if err != nil {
		t.Fatalf("decodeResponse error: %v", err)
	}

	if got.OperationID != "op-123" {
		t.Errorf("OperationID = %q, want %q", got.OperationID, "op-123")
	}
}

func TestTurnResponse_Decode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(turnResponse{TurnID: "turn-456"})
	}))
	defer srv.Close()

	transport := &httpTransport{client: srv.Client(), baseURL: srv.URL}
	resp, err := transport.post(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := decodeResponse[turnResponse](resp)
	if err != nil {
		t.Fatalf("decodeResponse error: %v", err)
	}

	if got.TurnID != "turn-456" {
		t.Errorf("TurnID = %q, want %q", got.TurnID, "turn-456")
	}
}
