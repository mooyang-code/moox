package stockhk

import (
	"testing"
	"time"
)

func TestRegularSessionUsesHongKongLunchBreak(t *testing.T) {
	session, err := RegularSession()
	if err != nil {
		t.Fatal(err)
	}
	buckets, err := session.ExpectedMinuteBuckets(time.Date(2026, 8, 31, 0, 0, 0, 0, session.Location))
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 330 || buckets[149].Hour() != 11 || buckets[150].Hour() != 13 {
		t.Fatalf("unexpected Hong Kong session buckets: len=%d boundary=%s", len(buckets), buckets[150])
	}
}
