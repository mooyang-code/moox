package main

import (
	"testing"
	"time"
)

func TestValidateForceRebuildViewOptionsRequiresDestructiveConfirmation(t *testing.T) {
	opts := forceRebuildViewOptions{spaceID: "space", viewID: "view", stream: defaultRepairJSName, consumer: "storage_view_misc_0123456789ab", timeout: time.Minute}
	if err := validateForceRebuildViewOptions(opts); err == nil {
		t.Fatal("force rebuild must require --yes")
	}
	opts.yes = true
	if err := validateForceRebuildViewOptions(opts); err != nil {
		t.Fatal(err)
	}
}
