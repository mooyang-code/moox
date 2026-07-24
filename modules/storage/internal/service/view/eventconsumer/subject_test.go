package eventconsumer

import "testing"

func TestDatasetRowsUpsertedSubjectRoundTrip(t *testing.T) {
	subject, err := DatasetRowsUpsertedSubject("", "量化.space", "kline/BTC")
	if err != nil {
		t.Fatal(err)
	}
	spaceID, datasetID, err := ParseDatasetRowsUpsertedSubject("", subject)
	if err != nil {
		t.Fatal(err)
	}
	if spaceID != "量化.space" || datasetID != "kline/BTC" {
		t.Fatalf("round trip = %q/%q", spaceID, datasetID)
	}
}

func TestDatasetRowsUpsertedSubjectRejectsMismatch(t *testing.T) {
	if _, _, err := ParseDatasetRowsUpsertedSubject("", DatasetRowsUpsertedSubjectPrefix+".bad.bad"); err == nil {
		t.Fatal("expected invalid token")
	}
}
