package server

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

// Error classification flows through the service sentinels — including when
// they're wrapped — instead of string matching (C20).
func TestMapServiceError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"nil", nil, http.StatusOK, ""},
		{"not found", service.ErrNotFound, http.StatusNotFound, "not_found"},
		{"conflict", service.Conflictf("operator turn already in progress"), http.StatusConflict, "conflict"},
		{"wrapped conflict", fmt.Errorf("sending: %w", service.Conflictf("turn in progress")), http.StatusConflict, "conflict"},
		{"unavailable", service.Unavailablef("store not configured"), http.StatusServiceUnavailable, "service_unavailable"},
		{"invalid", service.Invalidf("cannot delete system skill"), http.StatusUnprocessableEntity, "unprocessable_entity"},
		{"busy", service.Busyf("too many concurrent operations"), http.StatusTooManyRequests, "too_many_requests"},
		{"legacy provider ID substring", errors.New(`invalid provider ID "../x"`), http.StatusUnprocessableEntity, "unprocessable_entity"},
		{"unclassified", errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := mapServiceError(tc.err)
			if status != tc.wantStatus || code != tc.wantCode {
				t.Errorf("mapServiceError(%v) = (%d, %q), want (%d, %q)",
					tc.err, status, code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

// The classified message must stay clean — no "conflict:" prefix leaking into
// what the user sees.
func TestClassifiedErrorMessage(t *testing.T) {
	t.Parallel()

	err := service.Conflictf("operator turn already in progress")
	if err.Error() != "operator turn already in progress" {
		t.Errorf("Error() = %q, want clean message", err.Error())
	}
	if !errors.Is(err, service.ErrConflict) {
		t.Error("errors.Is(err, ErrConflict) = false, want true")
	}
}
