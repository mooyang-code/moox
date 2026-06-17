package service

import "testing"

func TestService_Health(t *testing.T) {
	svc := New("account")
	if got := svc.Health(); got.Module != "account" || !got.Ready {
		t.Fatalf("Health() = %+v, want ready account module", got)
	}
}
