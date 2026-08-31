package stockus

import (
	"testing"
	"time"
)

func TestRegularSessionPreservesNewYorkDST(t *testing.T) {
	session, err := RegularSession()
	if err != nil {
		t.Fatal(err)
	}
	winter, err := session.ExpectedMinuteBuckets(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	summer, err := session.ExpectedMinuteBuckets(time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(winter) != 390 || len(summer) != 390 || winter[0].UTC().Hour() != 14 || summer[0].UTC().Hour() != 13 {
		t.Fatalf("DST conversion mismatch: winter=%s summer=%s", winter[0], summer[0])
	}
}
