package main

import "testing"

func TestCommandsAreNamed(t *testing.T) {
	for _, v := range []string{"validate", "run-once"} {
		if v == "" {
			t.Fatal()
		}
	}
}
