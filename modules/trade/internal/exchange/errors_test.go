package exchange

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorSupportsErrorsAsAndUnwrap(t *testing.T) {
	cause := errors.New("timeout")
	err := &Error{
		Kind:       ErrorTransportUnknown,
		HTTPStatus: 503,
		Code:       "upstream",
		Err:        cause,
	}

	if !IsKind(err, ErrorTransportUnknown) {
		t.Fatal("IsKind() = false")
	}
	if IsKind(err, ErrorRejected) {
		t.Fatal("IsKind() matched wrong kind")
	}
	if !errors.Is(err, cause) {
		t.Fatal("Error does not unwrap its cause")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.HTTPStatus != 503 || typed.Code != "upstream" {
		t.Fatalf("errors.As() = %+v", typed)
	}
}

func TestIsKindHandlesNestedWrappingAndTypedNil(t *testing.T) {
	nested := fmt.Errorf(
		"session sync: %w",
		fmt.Errorf("Exchange request: %w", &Error{Kind: ErrorRateLimited}),
	)
	if !IsKind(nested, ErrorRateLimited) {
		t.Fatal("IsKind() did not traverse nested wrapping")
	}

	var typedNil *Error
	var err error = typedNil
	if IsKind(err, ErrorRateLimited) {
		t.Fatal("typed-nil Error matched a kind")
	}
	if IsKind(nil, ErrorRateLimited) {
		t.Fatal("nil error matched a kind")
	}
}

func TestErrorWithoutCauseStillHasUsefulMessage(t *testing.T) {
	err := (&Error{Kind: ErrorAuthentication, HTTPStatus: 401, Code: "invalid-key"}).Error()
	if err != "AUTHENTICATION (HTTP 401, code invalid-key)" {
		t.Fatalf("Error() = %q", err)
	}
}

func TestAllApprovedErrorKindsAreDistinct(t *testing.T) {
	kinds := []ErrorKind{
		ErrorRejected,
		ErrorInsufficientBalance,
		ErrorRateLimited,
		ErrorOrderNotFound,
		ErrorAuthentication,
		ErrorNotReady,
		ErrorTransportUnknown,
	}
	seen := make(map[ErrorKind]struct{}, len(kinds))
	for _, kind := range kinds {
		if kind == "" {
			t.Fatal("empty error kind")
		}
		if _, duplicate := seen[kind]; duplicate {
			t.Fatalf("duplicate error kind %q", kind)
		}
		seen[kind] = struct{}{}
	}
}
