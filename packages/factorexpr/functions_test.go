package factorexpr

import "testing"

func TestFunctionDocs(t *testing.T) {
	docs := FunctionDocs()
	if len(docs) != len(functionList) {
		t.Fatalf("docs = %d, want %d", len(docs), len(functionList))
	}
	byName := map[string]FunctionDoc{}
	for _, doc := range docs {
		byName[doc.Name] = doc
		for _, arg := range doc.Args {
			if arg.Kind == "" {
				t.Fatalf("%s argument %s has no kind", doc.Name, arg.Name)
			}
		}
	}
	if got := byName["pct_change"].Signature; got != "pct_change(x, k=1)" {
		t.Fatalf("pct_change signature = %s", got)
	}
	if got := byName["ts_quantile"]; got.Signature != "ts_quantile(x, n, q)" || got.Category != "timeseries" || got.Args[2].Kind != "constant" {
		t.Fatalf("ts_quantile doc = %+v", got)
	}
	if byName["is_nan"].Result != "bool" || byName["cs_rank"].Category != "cross_section" {
		t.Fatal("result or category wrong")
	}
}
