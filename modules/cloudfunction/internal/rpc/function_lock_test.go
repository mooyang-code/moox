package rpc

import (
	"reflect"
	"testing"
)

func TestOrderedUniqueSCFNodeIDsProvidesStableLockOrder(t *testing.T) {
	got := orderedUniqueSCFNodeIDs([]string{"node-z", "node-a", "node-b", "node-a", ""})
	want := []string{"node-a", "node-b", "node-z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orderedUniqueSCFNodeIDs() = %v, want %v", got, want)
	}
}
