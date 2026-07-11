package execution

import "testing"

func TestReplaceSagaExposesPartialFailure(t *testing.T) {
	s := NewReplaceSaga("s", "o")
	if e := s.CancelConfirmed(); e != nil {
		t.Fatal(e)
	}
	if e := s.ReplacementCreated("r"); e != nil {
		t.Fatal(e)
	}
	if e := s.ReplacementResult(false, false, "rejected"); e != nil {
		t.Fatal(e)
	}
	if s.State != SagaReplaceFailedAfterCancel {
		t.Fatal(s.State)
	}
}
