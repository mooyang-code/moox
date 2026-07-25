package view

import "testing"

func TestEventConsumerOptionsDefaults(t *testing.T) {
	opts, err := (EventConsumerOptions{}).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Consumer != "storage_view" || opts.AckWaitMS != 120000 || opts.FetchBatch != 8 || opts.MaxWorkers != 4 || opts.MaxRetryAttempts != 10 || opts.Ordering != "subject" {
		t.Fatalf("options = %+v", opts)
	}
}

func TestEventConsumerOptionsRejectsNegativeRetryAttempts(t *testing.T) {
	if _, err := (EventConsumerOptions{MaxRetryAttempts: -1}).withDefaults(); err == nil {
		t.Fatal("negative MaxRetryAttempts was accepted")
	}
}
