package snapshot

import (
	"context"
	"testing"
)

func TestAcquireSharesContentAddressedFile(t *testing.T) {
	s := NewStore(t.TempDir())
	k := Key{Namespace: "factor", DataRevision: "r1", SchemaHash: "s1"}
	a, e := s.Acquire(context.Background(), k, []byte("data"), 1)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Acquire(context.Background(), k, []byte("data"), 1)
	if e != nil || a.Path != b.Path {
		t.Fatalf("a=%+v b=%+v err=%v", a, b, e)
	}
	_ = a.Release()
	_ = b.Release()
}
