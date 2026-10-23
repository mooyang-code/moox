package rpc

import (
	"reflect"
	"testing"
)

func TestOrderedUniqueSCFInstanceIDsProvidesStableLockOrder(t *testing.T) {
	got := orderedUniqueSCFInstanceIDs([]string{"instance-z", "instance-a", "instance-b", "instance-a", ""})
	want := []string{"instance-a", "instance-b", "instance-z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orderedUniqueSCFInstanceIDs() = %v, want %v", got, want)
	}
}
