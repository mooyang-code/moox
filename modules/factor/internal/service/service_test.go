package service

import "testing"

func TestService_Health(t *testing.T) {
	svc := New("factor")
	if got := svc.Health(); got.Module != "factor" || !got.Ready {
		t.Fatalf("Health() = %+v, want ready factor module", got)
	}
}
