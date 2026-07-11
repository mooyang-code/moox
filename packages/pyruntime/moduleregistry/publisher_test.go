package moduleregistry

import (
	"context"
	"errors"
	"testing"
)

func TestPublishRejectsTraversal(t *testing.T) {
	_, err := NewSourcePublisher(t.TempDir()).Publish(context.Background(), ModuleSource{Type: "factor", LogicalID: "../x", Source: []byte("x=1")})
	if !errors.Is(err, ErrInvalidLogicalID) {
		t.Fatal(err)
	}
}
func TestPublishIsContentAddressed(t *testing.T) {
	p := NewSourcePublisher(t.TempDir())
	a, e := p.Publish(context.Background(), ModuleSource{Type: "factor", LogicalID: "Bias", Source: []byte("x=1")})
	if e != nil {
		t.Fatal(e)
	}
	b, e := p.Publish(context.Background(), ModuleSource{Type: "factor", LogicalID: "Bias", Source: []byte("x=1")})
	if e != nil || a.Path != b.Path || a.SourceHash != b.SourceHash {
		t.Fatalf("a=%+v b=%+v err=%v", a, b, e)
	}
}
