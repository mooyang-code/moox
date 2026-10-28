package seriesset

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestCanonicalizeSortsDedupesAndHashes(t *testing.T) {
	set, err := Canonicalize([]Series{
		{SubjectID: "ETH-USDT", SeriesTag: "X"},
		{SubjectID: "BTC-USDT", SeriesTag: "x"},
		{SubjectID: " btc-usdt ", SeriesTag: "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Series) != 2 || set.Series[0].SubjectID != "BTC-USDT" {
		t.Fatalf("unexpected set %+v", set.Series)
	}
	if set.Hash != Hash(set.Keys) {
		t.Fatal("hash mismatch")
	}
	again, _ := VerifySorted(set.Series)
	if again != set.Hash {
		t.Fatalf("verify hash %s != %s", again, set.Hash)
	}
}

func TestVerifySortedRejectsUnsortedAndDuplicates(t *testing.T) {
	a := Series{SubjectID: "A", SeriesTag: ""}
	b := Series{SubjectID: "B", SeriesTag: ""}
	if _, err := VerifySorted([]Series{b, a}); err == nil {
		t.Fatal("unsorted input must fail")
	}
	if _, err := VerifySorted([]Series{a, a}); err == nil {
		t.Fatal("duplicate input must fail")
	}
}

func TestHashMatchesLegacyDefinition(t *testing.T) {
	// 与 Collector 原 SeriesSetHash 的定义保持一致：每个键后追加单个 0 字节再做 sha256。
	h := sha256.New()
	key := CanonicalKey("A", "t")
	h.Write([]byte(key))
	h.Write([]byte{0})
	if got, want := Hash([]string{key}), hex.EncodeToString(h.Sum(nil)); got != want {
		t.Fatalf("hash %s != %s", got, want)
	}
}
