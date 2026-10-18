package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNonceConsumePersistsAndRejectsConcurrentDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonces")
	nonces, err := OpenNonces(path)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := nonces.Consume(context.Background(), "service", "abc", time.Minute)
			if err != nil {
				t.Errorf("Consume() = %v", err)
				return
			}
			if ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted = %d", accepted.Load())
	}
	if err := nonces.Close(); err != nil {
		t.Fatal(err)
	}
	nonces, err = OpenNonces(path)
	if err != nil {
		t.Fatal(err)
	}
	defer nonces.Close()
	if ok, err := nonces.Consume(context.Background(), "service", "abc", time.Minute); err != nil || ok {
		t.Fatalf("persistent consume = %v, %v", ok, err)
	}
}
