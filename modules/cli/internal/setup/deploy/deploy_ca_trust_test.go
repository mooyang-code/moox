package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLocalCATrustFailuresAreRecognizable(t *testing.T) {
	err := EnsureLocalCATrust(context.Background(), "", "")
	if !errors.Is(err, ErrBrowserCATrust) {
		t.Fatalf("EnsureLocalCATrust error %v does not wrap ErrBrowserCATrust", err)
	}
	if !strings.HasPrefix(err.Error(), "browser_ca_trust_failed: ") {
		t.Fatalf("error text changed: %q", err.Error())
	}
}
