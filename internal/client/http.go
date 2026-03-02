package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/jefflinse/toasters/internal/service"
)

const maxResponseSize = 10 << 20 // 10 MiB

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

var (
	// ErrConnectionFailed indicates the client could not reach the server.
	ErrConnectionFailed = errors.New("connection failed")

	// ErrConflict indicates a 409 Conflict response.
	ErrConflict = errors.New("conflict")

	// ErrUnprocessable indicates a 422 Unprocessable Entity response.
	ErrUnprocessable = errors.New("unprocessable entity")

	// ErrRateLimited indicates a 429 Too Many Requests response.
	ErrRateLimited = errors.New("rate limited")

	// ErrServerError indicates a 500 Internal Server Error response.
	ErrServerError = errors.New("server error")

	// ErrServiceUnavailable indicates a 503 Service Unavailable response.
	ErrServiceUnavailable = errors.New("service unavailable")
)

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

// httpTransport is the low-level HTTP transport used by RemoteClient.
// It constructs requests, sets standard headers, and returns raw responses.
type httpTransport struct {
	client  *http.Client
	baseURL string
}

// get sends a GET request to the given path.
func (t *httpTransport) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GET request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		if connErr := asConnectionError(err); connErr != nil {
			return nil, connErr
		}
		return nil, fmt.Errorf("executing GET %s: %w", path, err)
	}
	return resp, nil
}

// post sends a POST request with a JSON body to the given path.
func (t *httpTransport) post(ctx context.Context, path string, body any) (*http.Response, error) {
	return t.doWithBody(ctx, http.MethodPost, path, body)
}

// put sends a PUT request with a JSON body to the given path.
func (t *httpTransport) put(ctx context.Context, path string, body any) (*http.Response, error) {
	return t.doWithBody(ctx, http.MethodPut, path, body)
}

// delete sends a DELETE request to the given path.
func (t *httpTransport) delete(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating DELETE request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		if connErr := asConnectionError(err); connErr != nil {
			return nil, connErr
		}
		return nil, fmt.Errorf("executing DELETE %s: %w", path, err)
	}
	return resp, nil
}

// doWithBody is the shared implementation for POST and PUT requests.
func (t *httpTransport) doWithBody(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating %s request: %w", method, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := t.client.Do(req)
	if err != nil {
		if connErr := asConnectionError(err); connErr != nil {
			return nil, connErr
		}
		return nil, fmt.Errorf("executing %s %s: %w", method, path, err)
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Response decoding
// ---------------------------------------------------------------------------

// decodeResponse reads and decodes an HTTP response into the target type T.
// It handles status code mapping, error response parsing, and connection errors.
//
// For 204 No Content responses, it returns the zero value of T.
// For 2xx responses, it decodes the JSON body into T.
// For non-2xx responses, it maps the status code to a typed error.
func decodeResponse[T any](resp *http.Response) (T, error) {
	var zero T
	defer func() { _ = resp.Body.Close() }()

	// 204 No Content — success with no body.
	if resp.StatusCode == http.StatusNoContent {
		return zero, nil
	}

	// 2xx — decode JSON body.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		limited := io.LimitReader(resp.Body, maxResponseSize)
		if err := json.NewDecoder(limited).Decode(&zero); err != nil {
			return zero, fmt.Errorf("decoding response body: %w", err)
		}
		return zero, nil
	}

	// Non-2xx — try to decode error response, then map status to typed error.
	serverMsg := extractErrorMessage(resp)
	return zero, mapStatusToError(resp.StatusCode, serverMsg)
}

// decodeNoContent handles responses where no body is expected (DELETE, etc.).
// It checks the status code and returns an error for non-2xx responses.
func decodeNoContent(resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	serverMsg := extractErrorMessage(resp)
	return mapStatusToError(resp.StatusCode, serverMsg)
}

// extractErrorMessage attempts to decode an errorResponse from the body.
// If decoding fails, it falls back to the HTTP status text.
// The body is limited to maxResponseSize to prevent memory exhaustion.
func extractErrorMessage(resp *http.Response) string {
	limited := io.LimitReader(resp.Body, maxResponseSize)
	var errResp errorResponse
	if err := json.NewDecoder(limited).Decode(&errResp); err == nil && errResp.Error.Message != "" {
		return errResp.Error.Message
	}
	return http.StatusText(resp.StatusCode)
}

// mapStatusToError maps an HTTP status code to a typed sentinel error,
// wrapping the server's error message for context.
func mapStatusToError(status int, msg string) error {
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", service.ErrNotFound, msg)
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", ErrConflict, msg)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrUnprocessable, msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrRateLimited, msg)
	case http.StatusInternalServerError:
		return fmt.Errorf("%w: %s", ErrServerError, msg)
	case http.StatusServiceUnavailable:
		return fmt.Errorf("%w: %s", ErrServiceUnavailable, msg)
	default:
		return fmt.Errorf("unexpected status %d: %s", status, msg)
	}
}

// ---------------------------------------------------------------------------
// Connection error detection
// ---------------------------------------------------------------------------

// isConnectionError returns true if the error indicates a connection-level
// failure (timeout, connection refused, DNS resolution failure, etc.).
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrConnectionFailed) {
		return true
	}
	// Check for net.Error (timeout, temporary).
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// Check for net.OpError (connection refused, etc.).
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// asConnectionError checks if err is a connection-level error and wraps it
// as ErrConnectionFailed. Returns nil if err is not a connection error.
func asConnectionError(err error) error {
	if err == nil {
		return nil
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return fmt.Errorf("%w: %s", ErrConnectionFailed, err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("%w: %s", ErrConnectionFailed, err)
	}
	return nil
}
