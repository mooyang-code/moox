package events

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestEventContractArchitecture(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, event := range AllEventTypes {
		if _, ok := registry.Schema(event); !ok {
			t.Fatalf("producer event %s@%d is not registered", event.Name, event.Version)
		}
	}
	for _, schema := range registry.Schemas() {
		if schema.Payload == "google.protobuf.Struct" {
			t.Fatalf("event %s still uses untyped Struct payload", schema.Name)
		}
		if _, ok := registry.PayloadFactory(schema.Payload); !ok {
			t.Fatalf("event %s payload %s has no factory", schema.Name, schema.Payload)
		}
		family, err := registry.FamilyPattern(EventType{Name: schema.Name, Version: schema.Version})
		if err != nil || family == "" {
			t.Fatalf("event %s family = %q, err = %v", schema.Name, family, err)
		}
	}

	descriptor := (&eventpb.EventMessage{}).ProtoReflect().Descriptor()
	for i := 0; i < descriptor.Fields().Len(); i++ {
		field := descriptor.Fields().Get(i)
		switch field.Name() {
		case "event_id", "event_name", "event_version", "space_id", "subject_id", "occurred_at", "payload":
		default:
			t.Fatalf("EventMessage contains out-of-contract field %q", field.Name())
		}
	}
	if descriptor.Fields().ByName(protoreflect.Name("producer")) != nil || descriptor.Fields().ByName(protoreflect.Name("protocol_version")) != nil {
		t.Fatal("EventMessage contains removed legacy envelope metadata")
	}
}

func TestEventVocabularyGateMatchesEventTypeDeclarations(t *testing.T) {
	declared := declaredEventTypes(t)
	listed := make(map[string]EventType, len(AllEventTypes))
	for _, event := range AllEventTypes {
		key := eventVocabularyKey(event)
		if _, exists := listed[key]; exists {
			t.Fatalf("duplicate event in AllEventTypes: %s", key)
		}
		listed[key] = event
	}
	if len(declared) != len(listed) {
		t.Fatalf("event vocabulary declaration count=%d, gate count=%d", len(declared), len(listed))
	}
	for name, event := range declared {
		key := eventVocabularyKey(event)
		if _, ok := listed[key]; !ok {
			t.Fatalf("declared EventType %s (%s) is missing from AllEventTypes", name, key)
		}
	}
}

func declaredEventTypes(t *testing.T) map[string]EventType {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	registryPath := filepath.Join(filepath.Dir(testFile), "registry.go")
	file, err := parser.ParseFile(token.NewFileSet(), registryPath, nil, 0)
	if err != nil {
		t.Fatalf("parse event registry source: %v", err)
	}
	declared := make(map[string]EventType)
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			values := spec.(*ast.ValueSpec)
			for i, name := range values.Names {
				if i >= len(values.Values) {
					continue
				}
				event, ok := parseEventTypeLiteral(values.Values[i])
				if ok {
					declared[name.Name] = event
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("no EventType declarations found in registry.go")
	}
	return declared
}

func parseEventTypeLiteral(expression ast.Expr) (EventType, bool) {
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return EventType{}, false
	}
	typeName, ok := literal.Type.(*ast.Ident)
	if !ok || typeName.Name != "EventType" {
		return EventType{}, false
	}
	var event EventType
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return EventType{}, false
		}
		fieldName, ok := field.Key.(*ast.Ident)
		value, valueOK := field.Value.(*ast.BasicLit)
		if !ok || !valueOK {
			return EventType{}, false
		}
		switch fieldName.Name {
		case "Name":
			name, err := strconv.Unquote(value.Value)
			if err != nil {
				return EventType{}, false
			}
			event.Name = name
		case "Version":
			version, err := strconv.ParseUint(value.Value, 0, 32)
			if err != nil {
				return EventType{}, false
			}
			event.Version = uint32(version)
		}
	}
	return event, event.Name != "" && event.Version != 0
}

func eventVocabularyKey(event EventType) string {
	return event.Name + "@" + strconv.FormatUint(uint64(event.Version), 10)
}
