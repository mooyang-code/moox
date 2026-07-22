package eventconsumer

import "testing"

func TestRowsUpsertedSubjectRoundTrip(t *testing.T) {
	subject, err := RowsUpsertedSubject("", "量化.space", "kline/BTC")
	if err != nil {
		t.Fatal(err)
	}
	spaceID, datasetID, err := ParseRowsUpsertedSubject("", subject)
	if err != nil {
		t.Fatal(err)
	}
	if spaceID != "量化.space" || datasetID != "kline/BTC" {
		t.Fatalf("round trip = %q/%q", spaceID, datasetID)
	}
}

func TestRowsUpsertedSubjectRejectsMismatch(t *testing.T) {
	if _, _, err := ParseRowsUpsertedSubject("", RowsUpsertedSubjectPrefix+".bad.bad"); err == nil {
		t.Fatal("expected invalid token")
	}
}
