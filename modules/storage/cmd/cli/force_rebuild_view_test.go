package main

import (
	"testing"
	"time"
)

func TestValidateForceRebuildViewOptionsRequiresDestructiveConfirmationAndLookback(t *testing.T) {
	opts := forceRebuildViewOptions{spaceID: "space", viewID: "view", stream: defaultRepairJSName, consumer: defaultRepairConsumer, timeout: time.Minute, lookback: time.Hour}
	if err := validateForceRebuildViewOptions(opts); err == nil {
		t.Fatal("force rebuild must require --yes")
	}
	opts.yes = true
	if err := validateForceRebuildViewOptions(opts); err != nil {
		t.Fatal(err)
	}
	opts.lookback = 0
	if err := validateForceRebuildViewOptions(opts); err == nil {
		t.Fatal("force rebuild must require a positive lookback")
	}
}
