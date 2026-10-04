package broker

import (
	"testing"
)

func TestJetStreamBaseOptionsHonorsExplicitMaxStore(t *testing.T) {
	const want = int64(9 << 30)
	opts, err := jetStreamBaseOptions(t.TempDir(), want)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.JetStream || opts.JetStreamMaxStore != want {
		t.Fatalf("JetStream=%v MaxStore=%d, want enabled with %d", opts.JetStream, opts.JetStreamMaxStore, want)
	}
}
