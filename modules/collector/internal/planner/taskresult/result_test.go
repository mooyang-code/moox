package taskresult

import "testing"

func TestResultIDsAreDeterministicAndTaskExclusive(t *testing.T) {
	first := resultIDs("crypto", "task-btc-1")
	second := resultIDs("crypto", "task-btc-1")
	otherTask := resultIDs("crypto", "task-eth-1")
	otherSpace := resultIDs("stockcn", "task-btc-1")

	if first != second {
		t.Fatalf("result IDs are not deterministic: first=%+v second=%+v", first, second)
	}
	if first.DatasetID == otherTask.DatasetID || first.ViewID == otherTask.ViewID {
		t.Fatalf("different tasks share result IDs: first=%+v other=%+v", first, otherTask)
	}
	if first.DatasetID == otherSpace.DatasetID || first.ViewID == otherSpace.ViewID {
		t.Fatalf("different spaces share result IDs: first=%+v other=%+v", first, otherSpace)
	}
	if first.DatasetID == "" || first.ViewID == "" {
		t.Fatal("result IDs must not be empty")
	}
}
