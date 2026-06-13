package service

import "testing"

func TestService_Health(t *testing.T) {
	svc := New("order")
	if got := svc.Health(); got.Module != "order" || !got.Ready {
		t.Fatalf("Health() = %+v, want ready order module", got)
	}
}
