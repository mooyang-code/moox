package binance

import (
	"strings"
	"testing"
)

func TestFieldsUseExactKeys(t *testing.T) {
	event, err := exactFields([]byte(`{"c":"new","C":"orig","i":1,"I":2,"r":"NONE","R":true,"N":null,"E":"1736996475556","q":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"c": "new", "C": "orig", "i": "1", "I": "2", "r": "NONE", "N": "", "missing": ""} {
		got, err := event.str(key)
		if err != nil || got != want {
			t.Fatalf("str(%q) = %q, %v; want %q", key, got, err, want)
		}
	}
	if value, err := event.bool("R"); err != nil || !value {
		t.Fatalf("bool(R) = %v, %v", value, err)
	}
	if value, err := event.int64("E"); err != nil || value != 1736996475556 {
		t.Fatalf("int64(E) = %v, %v", value, err)
	}
	if err := event.required("N"); err == nil {
		t.Fatal("null must count as missing for required keys")
	}
	if _, err := event.bool("q"); err == nil || !strings.Contains(err.Error(), `"q"`) {
		t.Fatalf("type error must name the key: %v", err)
	}
	// executionReport 的 o 是字符串，不能当对象取。
	report, _ := exactFields([]byte(`{"e":"executionReport","o":"LIMIT"}`))
	if _, err := report.object("o"); err == nil {
		t.Fatal("string o must not decode as object")
	}
}
