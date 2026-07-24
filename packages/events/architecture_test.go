package events

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
			t.Fatalf("producer event %s@%d is not registered", event.Name(), event.Version())
		}
	}
	for _, schema := range registry.Schemas() {
		if schema.Payload == "google.protobuf.Struct" {
			t.Fatalf("event %s still uses untyped Struct payload", schema.Name)
		}
		if _, ok := registry.PayloadFactory(schema.Payload); !ok {
			t.Fatalf("event %s payload %s has no factory", schema.Name, schema.Payload)
		}
		family, err := registry.FamilyPattern(EventType{name: schema.Name, version: schema.Version})
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

func TestEventTypeLiteralsDoNotBypassTheVocabularyGate(t *testing.T) {
	violations, err := qualifiedEventTypeLiterals()
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("production code constructs qualified EventType literals: %s", strings.Join(violations, "; "))
	}
}

func TestQualifiedEventTypeScannerRecognizesImportAliases(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "alias.go", `package example

import ev "github.com/mooyang-code/moox/packages/events"

var _ = ev.EventType{}
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	violations := qualifiedEventTypeLiteralsInFile(file, "alias.go")
	if len(violations) != 1 {
		t.Fatalf("alias scanner violations=%v, want one", violations)
	}
	file, err = parser.ParseFile(token.NewFileSet(), "dot.go", `package example

import . "github.com/mooyang-code/moox/packages/events"

var _ = EventType{}
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	violations = qualifiedEventTypeLiteralsInFile(file, "dot.go")
	if len(violations) != 1 {
		t.Fatalf("dot-import scanner violations=%v, want one", violations)
	}
	file, err = parser.ParseFile(token.NewFileSet(), "bypass.go", `package example

import ev "github.com/mooyang-code/moox/packages/events"

type ET = ev.EventType
var event ET
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	violations = qualifiedEventTypeLiteralsInFile(file, "bypass.go")
	if len(violations) != 2 {
		t.Fatalf("alias and variable scanner violations=%v, want two", violations)
	}
}

func declaredEventTypes(t *testing.T) map[string]EventType {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	declared := make(map[string]EventType)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(testFile), "*.go"))
	if err != nil {
		t.Fatalf("find event package sources: %v", err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse event source %s: %v", path, err)
		}
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
					if !ok {
						continue
					}
					if previous, exists := declared[name.Name]; exists && previous != event {
						t.Fatalf("EventType declaration %s appears more than once", name.Name)
					}
					declared[name.Name] = event
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("no EventType declarations found in packages/events")
	}
	return declared
}

func qualifiedEventTypeLiterals() ([]string, error) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("locate architecture test")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	violations := make([]string, 0)
	for _, root := range []string{filepath.Join(repoRoot, "modules"), filepath.Join(repoRoot, "packages")} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == ".git" || entry.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			violations = append(violations, qualifiedEventTypeLiteralsInFile(file, path)...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return violations, nil
}

func qualifiedEventTypeLiteralsInFile(file *ast.File, path string) []string {
	aliases := make(map[string]struct{})
	dotImport := false
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || importPath != "github.com/mooyang-code/moox/packages/events" {
			continue
		}
		if spec.Name == nil {
			aliases["events"] = struct{}{}
			continue
		}
		if spec.Name.Name == "_" {
			continue
		}
		if spec.Name.Name == "." {
			dotImport = true
			continue
		}
		aliases[spec.Name.Name] = struct{}{}
	}
	if len(aliases) == 0 && !dotImport {
		return nil
	}
	violations := make([]string, 0)
	appendViolation := func() { violations = append(violations, path) }
	// Track aliases of EventType as well as the direct package qualifier. This
	// keeps the architecture gate effective even when a producer hides the
	// governed type behind a local alias or variable declaration.
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if isEventTypeExpr(typeSpec.Type, aliases, dotImport) {
				aliases[typeSpec.Name.Name] = struct{}{}
				appendViolation()
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		valueSpec, valueOK := node.(*ast.ValueSpec)
		if valueOK && valueSpec.Type != nil && isEventTypeExpr(valueSpec.Type, aliases, dotImport) {
			appendViolation()
			return true
		}
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		selector, selectorOK := literal.Type.(*ast.SelectorExpr)
		if selectorOK && selector.Sel.Name == "EventType" {
			identifier, identifierOK := selector.X.(*ast.Ident)
			if identifierOK {
				if _, ok := aliases[identifier.Name]; ok {
					appendViolation()
				}
			}
			return true
		}
		identifier, identifierOK := literal.Type.(*ast.Ident)
		if dotImport && identifierOK && identifier.Name == "EventType" {
			appendViolation()
			return true
		}
		if identifierOK {
			if _, ok := aliases[identifier.Name]; ok {
				appendViolation()
			}
		}
		return true
	})
	return violations
}

func isEventTypeExpr(expression ast.Expr, aliases map[string]struct{}, dotImport bool) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if ok && selector.Sel.Name == "EventType" {
		identifier, identifierOK := selector.X.(*ast.Ident)
		if identifierOK {
			_, ok = aliases[identifier.Name]
			return ok
		}
	}
	identifier, ok := expression.(*ast.Ident)
	if !ok {
		return false
	}
	if dotImport && identifier.Name == "EventType" {
		return true
	}
	_, ok = aliases[identifier.Name]
	return ok
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
		case "name":
			name, err := strconv.Unquote(value.Value)
			if err != nil {
				return EventType{}, false
			}
			event.name = name
		case "version":
			version, err := strconv.ParseUint(value.Value, 0, 32)
			if err != nil {
				return EventType{}, false
			}
			event.version = uint32(version)
		}
	}
	return event, event.name != "" && event.version != 0
}

func eventVocabularyKey(event EventType) string {
	return event.name + "@" + strconv.FormatUint(uint64(event.version), 10)
}
