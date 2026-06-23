package deriver

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestBatcherFlushesBySize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := newBatcher[string](BatchOptions{
		BatchSize: 3,
		BatchWait: time.Hour,
	})
	out := make(chan []string, 1)
	go b.run(ctx, out)

	for _, item := range []string{"a", "b", "c"} {
		if err := b.add(ctx, item); err != nil {
			t.Fatalf("add(%q) error = %v", item, err)
		}
	}

	select {
	case got := <-out:
		want := []string{"a", "b", "c"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("batch = %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for size flush")
	}
}

func TestBatcherFlushesByWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := newBatcher[string](BatchOptions{
		BatchSize: 10,
		BatchWait: 10 * time.Millisecond,
	})
	out := make(chan []string, 1)
	go b.run(ctx, out)

	if err := b.add(ctx, "a"); err != nil {
		t.Fatalf("add error = %v", err)
	}

	select {
	case got := <-out:
		want := []string{"a"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("batch = %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for wait flush")
	}
}

func TestBatcherFlushesPendingOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	b := newBatcher[string](BatchOptions{
		BatchSize: 10,
		BatchWait: time.Hour,
	})
	out := make(chan []string, 1)
	go b.run(ctx, out)

	if err := b.add(ctx, "a"); err != nil {
		t.Fatalf("add error = %v", err)
	}
	cancel()

	select {
	case got := <-out:
		want := []string{"a"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("batch = %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancel flush")
	}
}
