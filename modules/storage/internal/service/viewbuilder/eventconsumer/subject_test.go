package eventconsumer

import "testing"

func TestDatasetFieldsChangedSubjectRoundTrip(t *testing.T) {
	subject, err := DatasetFieldsChangedSubject("", "量化.space", "kline/BTC")
	if err != nil {
		t.Fatal(err)
	}
	spaceID, datasetID, err := ParseDatasetFieldsChangedSubject("", subject)
	if err != nil {
		t.Fatal(err)
	}
	if spaceID != "量化.space" || datasetID != "kline/BTC" {
		t.Fatalf("round trip = %q/%q", spaceID, datasetID)
	}
}

func TestDatasetFieldsChangedSubjectRejectsMismatch(t *testing.T) {
	if _, _, err := ParseDatasetFieldsChangedSubject("", DatasetFieldsChangedSubjectPrefix+".bad.bad"); err == nil {
		t.Fatal("expected invalid token")
	}
}
