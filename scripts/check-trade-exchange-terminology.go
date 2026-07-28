// check-trade-exchange-terminology rejects Exchange vocabulary that calls an
// Exchange a provider, broker, venue, or platform.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var declarationPattern = regexp.MustCompile(`(?i)^\s*(?:(?:export|declare)\s+)*(?:message|interface|type|class|enum|service)\s+([A-Za-z_][A-Za-z0-9_]*)`)
var thirdPartyProviderFieldPattern = regexp.MustCompile(`^\s*(?:readonly\s+)?([A-Za-z_][A-Za-z0-9_]*)\??\s*:\s*(?:[A-Za-z_][A-Za-z0-9_]*\.)?(TracerProvider|ConfigProvider)\s*[;,]?\s*$`)

var defaultRoots = []string{
	"modules/trade",
	"packages/tradeeventpb",
	"web/src/api/trade",
}

type violation struct {
	path string
	line int
	term string
}

type checker struct {
	fset       *token.FileSet
	violations map[string]violation
}

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = defaultRoots
	}

	c := checker{
		fset:       token.NewFileSet(),
		violations: make(map[string]violation),
	}
	for _, root := range roots {
		if err := c.scanRoot(root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	if len(c.violations) == 0 {
		fmt.Println("trade Exchange terminology passed")
		return
	}

	violations := make([]violation, 0, len(c.violations))
	for _, item := range c.violations {
		violations = append(violations, item)
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].path == violations[j].path {
			return violations[i].line < violations[j].line
		}
		return violations[i].path < violations[j].path
	})

	fmt.Fprintln(os.Stderr, "trade Exchange terminology violations:")
	for _, item := range violations {
		fmt.Fprintf(os.Stderr, "  %s:%d: Exchange synonym %q is not allowed; use Exchange\n", item.path, item.line, item.term)
	}
	os.Exit(1)
}

func (c *checker) scanRoot(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if generatedProtobuf(path) {
			return nil
		}

		switch filepath.Ext(path) {
		case ".go":
			return c.scanGo(path)
		case ".proto", ".ts", ".tsx", ".yaml", ".yml", ".md":
			return c.scanText(path)
		default:
			return nil
		}
	})
}

func generatedProtobuf(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, ".trpc.go")
}

func (c *checker) scanGo(path string) error {
	file, err := parser.ParseFile(c.fset, path, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			c.addText(path, c.fset.Position(comment.Pos()).Line, comment.Text)
		}
	}

	for _, declaration := range file.Decls {
		c.scanGoDeclaration(path, declaration)
	}
	return nil
}

func (c *checker) scanGoDeclaration(path string, declaration ast.Decl) {
	switch decl := declaration.(type) {
	case *ast.GenDecl:
		for _, spec := range decl.Specs {
			switch value := spec.(type) {
			case *ast.TypeSpec:
				c.scanGoType(path, value)
			case *ast.ValueSpec:
				c.addNode(path, value)
			}
		}
	case *ast.FuncDecl:
		c.scanGoFunction(path, decl)
	}

	ast.Inspect(declaration, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			c.addNode(path, value)
		case *ast.ValueSpec:
			c.addNode(path, value)
		}
		return true
	})
}

func (c *checker) scanGoType(path string, spec *ast.TypeSpec) {
	c.addText(path, c.fset.Position(spec.Name.Pos()).Line, spec.Name.Name)
	var fields *ast.FieldList
	switch value := spec.Type.(type) {
	case *ast.StructType:
		fields = value.Fields
	case *ast.InterfaceType:
		fields = value.Methods
	default:
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			if allowedSecretClientProvider(path, field, name.Name) || allowedThirdPartyProvider(field, name.Name) {
				continue
			}
			context := spec.Name.Name + " " + name.Name
			c.addText(path, c.fset.Position(name.Pos()).Line, context)
			if functionType, ok := field.Type.(*ast.FuncType); ok {
				c.scanGoParameters(path, context, functionType.Params, functionType.Results)
			}
		}
	}
}

func (c *checker) scanGoFunction(path string, function *ast.FuncDecl) {
	c.addText(path, c.fset.Position(function.Name.Pos()).Line, function.Name.Name)
	context := function.Name.Name
	if function.Recv != nil {
		for _, field := range function.Recv.List {
			context = nodeText(c.fset, field.Type) + " " + function.Name.Name
			c.addText(path, c.fset.Position(function.Name.Pos()).Line, context)
		}
	}
	c.scanGoParameters(path, context, function.Type.Params, function.Type.Results)
}

func (c *checker) scanGoParameters(path, context string, fieldLists ...*ast.FieldList) {
	for _, fields := range fieldLists {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			for _, name := range field.Names {
				if allowedThirdPartyProvider(field, name.Name) {
					continue
				}
				c.addText(path, c.fset.Position(name.Pos()).Line, context+" "+name.Name)
			}
		}
	}
}

func allowedSecretClientProvider(path string, field *ast.Field, name string) bool {
	const secretClientPath = "modules/trade/internal/secretclient/"
	normalizedPath := filepath.ToSlash(filepath.Clean(path))
	inSecretClient := strings.HasPrefix(normalizedPath, secretClientPath) ||
		strings.Contains(normalizedPath, "/"+secretClientPath)
	if !inSecretClient || !strings.EqualFold(name, "provider") || field.Tag == nil {
		return false
	}
	tag, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return false
	}
	jsonTag, ok := reflect.StructTag(tag).Lookup("json")
	if !ok {
		return false
	}
	jsonName, _, _ := strings.Cut(jsonTag, ",")
	return jsonName == "provider"
}

func allowedThirdPartyProvider(field *ast.Field, _ string) bool {
	switch goTypeIdentifier(field.Type) {
	case "TracerProvider", "ConfigProvider":
		return true
	default:
		return false
	}
}

func goTypeIdentifier(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.StarExpr:
		return goTypeIdentifier(value.X)
	default:
		return ""
	}
}

func (c *checker) addNode(path string, node ast.Node) {
	c.addText(path, c.fset.Position(node.Pos()).Line, nodeText(c.fset, node))
}

func nodeText(fset *token.FileSet, node ast.Node) string {
	var rendered bytes.Buffer
	if err := format.Node(&rendered, fset, node); err != nil {
		return ""
	}
	return rendered.String()
}

func (c *checker) scanText(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	context := ""
	depth := 0
	for index, line := range strings.Split(string(contents), "\n") {
		if match := declarationPattern.FindStringSubmatch(line); len(match) == 2 {
			context = match[1]
			depth = 0
		}
		if !allowedThirdPartyProviderText(line) {
			c.addText(path, index+1, context+" "+line)
		}
		if context != "" {
			depth += strings.Count(line, "{") - strings.Count(line, "}")
			if depth <= 0 && strings.Contains(line, "}") {
				context = ""
			}
		}
	}
	return nil
}

func allowedThirdPartyProviderText(line string) bool {
	match := thirdPartyProviderFieldPattern.FindStringSubmatch(line)
	if len(match) != 3 {
		return false
	}
	switch match[2] {
	case "TracerProvider", "ConfigProvider":
		return true
	default:
		return false
	}
}

func (c *checker) addText(path string, line int, text string) {
	term, ok := exchangeSynonym(text)
	if !ok {
		return
	}
	displayPath := displayPath(path)
	key := fmt.Sprintf("%s:%d:%s", displayPath, line, term)
	c.violations[key] = violation{path: displayPath, line: line, term: term}
}

func displayPath(path string) string {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return path
	}
	relative, err := filepath.Rel(workingDirectory, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.ToSlash(relative)
}

func exchangeSynonym(text string) (string, bool) {
	tokens := terminologyTokens(text)
	if !containsAny(tokens, "exchange", "trade", "binance", "okx") {
		return "", false
	}
	for _, term := range []string{"provider", "broker", "venue", "platform"} {
		if contains(tokens, term) {
			return term, true
		}
	}
	return "", false
}

func terminologyTokens(text string) []string {
	var tokens []string
	var token strings.Builder
	flush := func() {
		if token.Len() > 0 {
			tokens = append(tokens, strings.ToLower(token.String()))
			token.Reset()
		}
	}
	characters := []rune(text)
	for index, character := range characters {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			flush()
			continue
		}
		if token.Len() > 0 && unicode.IsUpper(character) {
			previous := characters[index-1]
			nextIsLower := index+1 < len(characters) && unicode.IsLower(characters[index+1])
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || unicode.IsUpper(previous) && nextIsLower {
				flush()
			}
		}
		if token.Len() > 0 && unicode.IsDigit(character) && unicode.IsLetter(characters[index-1]) {
			flush()
		}
		token.WriteRune(character)
	}
	flush()
	return tokens
}

func contains(tokens []string, wanted string) bool {
	for _, token := range tokens {
		if token == wanted {
			return true
		}
	}
	return false
}

func containsAny(tokens []string, wanted ...string) bool {
	for _, item := range wanted {
		if contains(tokens, item) {
			return true
		}
	}
	return false
}
