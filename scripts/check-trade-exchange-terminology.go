// check-trade-exchange-terminology rejects Exchange vocabulary that calls an
// Exchange a provider, broker, venue, or platform.
package main

import (
	"fmt"
	"go/ast"
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

var declarationPattern = regexp.MustCompile(`(?i)^\s*(?:(?:export|declare)\s+)*(?:async\s+)?(message|interface|type|class|enum|service|function)\s+([A-Za-z_][A-Za-z0-9_]*)`)
var arrowFunctionPattern = regexp.MustCompile(`(?i)^\s*(?:(?:export|declare)\s+)*(?:const|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:async\s*)?\(`)
var sourceIdentifierPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
var qualifiedProviderTypePattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*(?:\s*\.\s*[A-Za-z_][A-Za-z0-9_]*)*)\s*\.\s*(TracerProvider|ConfigProvider)`)
var textImportPattern = regexp.MustCompile(`(?s)\bimport\s+(?:type\s+)?(.+?)\s+from\s+["']([^"']+)["']\s*;?`)
var namespaceImportPattern = regexp.MustCompile(`\*\s+as\s+([A-Za-z_][A-Za-z0-9_]*)`)
var namedImportPattern = regexp.MustCompile(`\{([^}]*)\}`)
var arrowEndPattern = regexp.MustCompile(`\)\s*=>`)
var thirdPartyProviderFieldPattern = regexp.MustCompile(`^\s*(?:readonly\s+)?([A-Za-z_][A-Za-z0-9_]*)\??\s*:\s*(.+?)\s*[;,]?\s*$`)
var structTagEntryPattern = regexp.MustCompile(`([A-Za-z0-9_-]+):"([^"]*)"`)
var yamlMappingPattern = regexp.MustCompile(`^\s*(?:-\s*)?(?:"([^"]+)"|'([^']+)'|([A-Za-z_][A-Za-z0-9_-]*))\s*:\s*(.*)$`)
var yamlContextOnlyValuePattern = regexp.MustCompile(`^(?:(?:&[A-Za-z0-9_.-]+|![^\s#]+)\s*)*(?:#.*)?$`)
var markdownHeadingPattern = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
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
	fset                      *token.FileSet
	violations                map[string]violation
	textProviderTypeAliases   map[string]bool
	textHostileTypeQualifiers map[string]bool
}

type providerScope struct {
	parent   *providerScope
	bindings map[string]bool
}

func newProviderScope(parent *providerScope) *providerScope {
	return &providerScope{parent: parent, bindings: make(map[string]bool)}
}

func (s *providerScope) declare(name string, allowed bool) {
	if s == nil || !genericProviderIdentifier(name) {
		return
	}
	s.bindings[name] = allowed
}

func (s *providerScope) allowed(name string) bool {
	if !genericProviderIdentifier(name) {
		return false
	}
	for current := s; current != nil; current = current.parent {
		if allowed, ok := current.bindings[name]; ok {
			return allowed
		}
	}
	return false
}

func (s *providerScope) declaredHere(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.bindings[name]
	return ok
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
			if violations[i].line == violations[j].line {
				return violations[i].term < violations[j].term
			}
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
		case ".proto", ".ts", ".tsx":
			return c.scanText(path)
		case ".yaml", ".yml":
			return c.scanYAML(path)
		case ".md":
			return c.scanMarkdown(path)
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
	normalizeGoProviderImportAliases(file)
	for _, group := range file.Comments {
		for _, comment := range group.List {
			c.addTextAllowingThirdPartyTypes(path, c.fset.Position(comment.Pos()).Line, comment.Text)
		}
	}

	for _, declaration := range file.Decls {
		c.scanGoDeclaration(path, declaration, nil)
	}
	return nil
}

func normalizeGoProviderImportAliases(file *ast.File) {
	hostileAliases := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		pathTokens := terminologyTokens(importPath)
		if !containsDomainToken(pathTokens) {
			continue
		}
		alias := filepath.Base(importPath)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if alias == "." || alias == "_" {
			continue
		}
		for _, item := range pathTokens {
			if isDomainToken(item) {
				hostileAliases[alias] = item
				break
			}
		}
	}
	if len(hostileAliases) == 0 {
		return
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || !thirdPartyProviderTypeName(selector.Sel.Name) {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if domain, hostile := hostileAliases[qualifier.Name]; hostile {
			qualifier.Name = domain
		}
		return true
	})
}

func (c *checker) scanGoDeclaration(path string, declaration ast.Decl, context []string) {
	switch value := declaration.(type) {
	case *ast.FuncDecl:
		c.scanGoFunction(path, value, context)
	case *ast.GenDecl:
		for _, spec := range value.Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				c.scanGoType(path, spec, context)
			case *ast.ValueSpec:
				c.scanGoValueSpec(path, spec, context, nil)
			case *ast.ImportSpec:
				c.scanGoImportSpec(path, spec, context)
			}
		}
	}
}

func (c *checker) scanGoImportSpec(path string, spec *ast.ImportSpec, context []string) {
	var tokens []string
	if spec.Name != nil {
		tokens = append(tokens, sourceTokens(spec.Name.Name)...)
	}
	tokens = append(tokens, goNodeTokens(spec.Path)...)
	c.addTokens(path, c.fset.Position(spec.Pos()).Line, context, tokens)
}

func (c *checker) scanGoValueSpec(path string, spec *ast.ValueSpec, context []string, scope *providerScope) {
	var declarationTokens []string
	for index, name := range spec.Names {
		allowed := providerValueSpecAllowed(spec, index)
		if !allowed {
			declarationTokens = append(declarationTokens, sourceTokens(name.Name)...)
		}
	}
	if spec.Type != nil {
		declarationTokens = append(declarationTokens, goNodeTokens(spec.Type)...)
	}
	for _, value := range spec.Values {
		declarationTokens = append(declarationTokens, goNodeTokensInScope(value, scope)...)
	}

	line := c.fset.Position(spec.Pos()).Line
	c.addTokens(path, line, context, declarationTokens)
	if spec.Type != nil {
		c.scanGoTypeExpression(path, spec.Type, extendDomainContext(context, declarationTokens))
	}
	if scope != nil {
		for index, name := range spec.Names {
			scope.declare(name.Name, providerValueSpecAllowed(spec, index))
		}
	}
}

func (c *checker) scanGoAssignment(path string, assignment *ast.AssignStmt, context []string, scope *providerScope) {
	var statementTokens []string
	for index, left := range assignment.Lhs {
		allowed := identifierAllowedInScope(left, scope)
		newGenericBinding := false
		if identifier, ok := left.(*ast.Ident); ok &&
			assignment.Tok == token.DEFINE &&
			genericProviderIdentifier(identifier.Name) &&
			!scope.declaredHere(identifier.Name) {
			newGenericBinding = true
			allowed = explicitThirdPartyProviderValue(matchedAssignmentValue(assignment, index))
		}
		if !allowed {
			if newGenericBinding {
				statementTokens = append(statementTokens, goNodeTokens(left)...)
			} else {
				statementTokens = append(statementTokens, goNodeTokensInScope(left, scope)...)
			}
		}
	}
	for _, right := range assignment.Rhs {
		statementTokens = append(statementTokens, goNodeTokensInScope(right, scope)...)
	}
	c.addTokens(path, c.fset.Position(assignment.Pos()).Line, context, statementTokens)

	if assignment.Tok == token.DEFINE && scope != nil {
		for index, left := range assignment.Lhs {
			identifier, ok := left.(*ast.Ident)
			if !ok || !genericProviderIdentifier(identifier.Name) || scope.declaredHere(identifier.Name) {
				continue
			}
			scope.declare(
				identifier.Name,
				explicitThirdPartyProviderValue(matchedAssignmentValue(assignment, index)),
			)
		}
	}
}

func (c *checker) scanGoType(path string, spec *ast.TypeSpec, context []string) {
	nameTokens := sourceTokens(spec.Name.Name)
	c.addTokens(path, c.fset.Position(spec.Name.Pos()).Line, context, nameTokens)
	typeContext := extendDomainContext(context, nameTokens)
	c.scanGoFieldList(path, spec.TypeParams, typeContext)
	c.scanGoTypeExpression(path, spec.Type, typeContext)
}

func (c *checker) scanGoFunction(path string, function *ast.FuncDecl, context []string) {
	nameTokens := sourceTokens(function.Name.Name)
	var receiverTokens []string
	if function.Recv != nil {
		receiverTokens = goNodeTokens(function.Recv)
	}
	c.addTokens(
		path,
		c.fset.Position(function.Name.Pos()).Line,
		extendDomainContext(context, receiverTokens),
		nameTokens,
	)

	functionContext := extendDomainContext(context, nameTokens, receiverTokens)
	c.scanGoFieldList(path, function.Recv, extendDomainContext(context, nameTokens))
	c.scanGoFieldList(path, function.Type.TypeParams, functionContext)
	c.scanGoFieldList(path, function.Type.Params, functionContext)
	c.scanGoFieldList(path, function.Type.Results, functionContext)
	if function.Body != nil {
		scope := newProviderScope(nil)
		bindProviderFields(scope, function.Recv)
		bindProviderFields(scope, function.Type.TypeParams)
		bindProviderFields(scope, function.Type.Params)
		bindProviderFields(scope, function.Type.Results)
		c.scanGoBody(path, function.Body, functionContext, scope)
	}
}

func (c *checker) scanGoBody(
	path string,
	body *ast.BlockStmt,
	context []string,
	scope *providerScope,
) {
	visitor := &goBodyVisitor{
		checker: c,
		path:    path,
		context: context,
		scope:   scope,
	}
	for _, statement := range body.List {
		ast.Walk(visitor, statement)
	}
}

type goBodyVisitor struct {
	checker *checker
	path    string
	context []string
	scope   *providerScope
}

func (v *goBodyVisitor) withScope(scope *providerScope) *goBodyVisitor {
	return &goBodyVisitor{
		checker: v.checker,
		path:    v.path,
		context: v.context,
		scope:   scope,
	}
}

func (v *goBodyVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}

	switch value := node.(type) {
	case *ast.BlockStmt:
		return v.withScope(newProviderScope(v.scope))
	case *ast.TypeSpec:
		v.checker.scanGoType(v.path, value, v.context)
		return nil
	case *ast.ValueSpec:
		v.checker.scanGoValueSpec(v.path, value, v.context, v.scope)
		return v
	case *ast.AssignStmt:
		v.checker.scanGoAssignment(v.path, value, v.context, v.scope)
		return v
	case *ast.ExprStmt:
		v.checker.scanGoExpression(v.path, value.X, v.context, v.scope)
		return v
	case *ast.ReturnStmt:
		for _, result := range value.Results {
			v.checker.scanGoExpression(v.path, result, v.context, v.scope)
		}
		return v
	case *ast.GoStmt:
		v.checker.scanGoExpression(v.path, value.Call, v.context, v.scope)
		return v
	case *ast.DeferStmt:
		v.checker.scanGoExpression(v.path, value.Call, v.context, v.scope)
		return v
	case *ast.SendStmt:
		v.checker.scanGoExpressionGroup(
			v.path,
			value.Pos(),
			v.context,
			v.scope,
			value.Chan,
			value.Value,
		)
		return v
	case *ast.IncDecStmt:
		v.checker.scanGoExpression(v.path, value.X, v.context, v.scope)
		return v
	case *ast.BranchStmt:
		if value.Label != nil {
			v.checker.addTokens(
				v.path,
				v.checker.fset.Position(value.Label.Pos()).Line,
				v.context,
				sourceTokens(value.Label.Name),
			)
		}
		return v
	case *ast.LabeledStmt:
		v.checker.addTokens(
			v.path,
			v.checker.fset.Position(value.Label.Pos()).Line,
			v.context,
			sourceTokens(value.Label.Name),
		)
		return v
	case *ast.RangeStmt:
		rangeVisitor := v.withScope(newProviderScope(v.scope))
		v.checker.scanGoRange(v.path, value, v.context, rangeVisitor.scope)
		bindUntypedRangeNames(rangeVisitor.scope, value)
		walkGoNode(rangeVisitor, value.X)
		walkGoNode(rangeVisitor, value.Body)
		return nil
	case *ast.IfStmt:
		branchVisitor := v.withScope(newProviderScope(v.scope))
		walkGoNode(branchVisitor, value.Init)
		v.checker.scanGoExpression(v.path, value.Cond, v.context, branchVisitor.scope)
		walkGoNode(branchVisitor, value.Cond)
		walkGoNode(branchVisitor, value.Body)
		walkGoNode(branchVisitor, value.Else)
		return nil
	case *ast.ForStmt:
		loopVisitor := v.withScope(newProviderScope(v.scope))
		walkGoNode(loopVisitor, value.Init)
		v.checker.scanGoExpression(v.path, value.Cond, v.context, loopVisitor.scope)
		walkGoNode(loopVisitor, value.Cond)
		walkGoNode(loopVisitor, value.Post)
		walkGoNode(loopVisitor, value.Body)
		return nil
	case *ast.SwitchStmt:
		switchVisitor := v.withScope(newProviderScope(v.scope))
		walkGoNode(switchVisitor, value.Init)
		v.checker.scanGoExpression(v.path, value.Tag, v.context, switchVisitor.scope)
		walkGoNode(switchVisitor, value.Tag)
		walkGoNode(switchVisitor, value.Body)
		return nil
	case *ast.TypeSwitchStmt:
		switchVisitor := v.withScope(newProviderScope(v.scope))
		walkGoNode(switchVisitor, value.Init)
		walkGoNode(switchVisitor, value.Assign)
		walkGoNode(switchVisitor, value.Body)
		return nil
	case *ast.CaseClause:
		for _, expression := range value.List {
			v.checker.scanGoExpression(v.path, expression, v.context, v.scope)
		}
		return v.withScope(newProviderScope(v.scope))
	case *ast.CommClause:
		return v.withScope(newProviderScope(v.scope))
	case *ast.FuncLit:
		v.checker.scanGoFieldList(v.path, value.Type.TypeParams, v.context)
		v.checker.scanGoFieldList(v.path, value.Type.Params, v.context)
		v.checker.scanGoFieldList(v.path, value.Type.Results, v.context)
		functionScope := newProviderScope(v.scope)
		bindProviderFields(functionScope, value.Type.TypeParams)
		bindProviderFields(functionScope, value.Type.Params)
		bindProviderFields(functionScope, value.Type.Results)
		v.checker.scanGoBody(v.path, value.Body, v.context, functionScope)
		return nil
	default:
		return v
	}
}

func walkGoNode(visitor ast.Visitor, node ast.Node) {
	if node != nil {
		ast.Walk(visitor, node)
	}
}

func (c *checker) scanGoExpression(
	path string,
	expression ast.Expr,
	context []string,
	scope *providerScope,
) {
	if expression == nil {
		return
	}
	c.addTokens(
		path,
		c.fset.Position(expression.Pos()).Line,
		context,
		goNodeTokensInScope(expression, scope),
	)
}

func (c *checker) scanGoExpressionGroup(
	path string,
	position token.Pos,
	context []string,
	scope *providerScope,
	expressions ...ast.Expr,
) {
	var tokens []string
	for _, expression := range expressions {
		tokens = append(tokens, goNodeTokensInScope(expression, scope)...)
	}
	c.addTokens(path, c.fset.Position(position).Line, context, tokens)
}

func (c *checker) scanGoRange(
	path string,
	statement *ast.RangeStmt,
	context []string,
	scope *providerScope,
) {
	var headerTokens []string
	if statement.Key != nil {
		headerTokens = append(
			headerTokens,
			goRangeBindingTokens(statement.Key, statement.Tok, scope)...,
		)
	}
	if statement.Value != nil {
		headerTokens = append(
			headerTokens,
			goRangeBindingTokens(statement.Value, statement.Tok, scope)...,
		)
	}
	headerTokens = append(headerTokens, goNodeTokensInScope(statement.X, scope)...)
	c.addTokens(path, c.fset.Position(statement.Pos()).Line, context, headerTokens)
}

func (c *checker) scanGoFieldList(path string, fields *ast.FieldList, context []string) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		c.scanGoField(path, field, context)
	}
}

func (c *checker) scanGoField(path string, field *ast.Field, context []string) {
	typeTokens := goNodeTokens(field.Type)
	if len(field.Names) == 0 {
		c.addTokens(path, c.fset.Position(field.Pos()).Line, context, goStructTagTokens(field, false))
		c.scanGoTypeExpression(path, field.Type, context)
		return
	}

	for _, name := range field.Names {
		nameTokens := sourceTokens(name.Name)
		secretClientProvider := allowedSecretClientProvider(path, field, name.Name)
		if secretClientProvider || allowedTypedProviderIdentifier(name.Name, field.Type) {
			nameTokens = nil
		}
		tagTokens := goStructTagTokens(field, secretClientProvider)
		c.addTokens(
			path,
			c.fset.Position(name.Pos()).Line,
			context,
			append(append(append([]string{}, nameTokens...), typeTokens...), tagTokens...),
		)
		fieldContext := extendDomainContext(context, nameTokens, typeTokens, tagTokens)
		c.scanGoTypeExpression(path, field.Type, fieldContext)
	}
}

func (c *checker) scanGoTypeExpression(path string, expression ast.Expr, context []string) {
	if expression == nil {
		return
	}

	switch value := expression.(type) {
	case *ast.StructType:
		c.scanGoFieldList(path, value.Fields, context)
	case *ast.InterfaceType:
		c.scanGoFieldList(path, value.Methods, context)
	case *ast.FuncType:
		c.scanGoFieldList(path, value.TypeParams, context)
		c.scanGoFieldList(path, value.Params, context)
		c.scanGoFieldList(path, value.Results, context)
	default:
		c.addTokens(
			path,
			c.fset.Position(expression.Pos()).Line,
			context,
			goNodeTokens(expression),
		)
		c.scanNestedGoTypeDeclarations(path, expression, context)
	}
}

func (c *checker) scanNestedGoTypeDeclarations(path string, expression ast.Expr, context []string) {
	ast.Inspect(expression, func(node ast.Node) bool {
		if node == expression {
			return true
		}
		switch value := node.(type) {
		case *ast.StructType:
			c.scanGoFieldList(path, value.Fields, context)
			return false
		case *ast.InterfaceType:
			c.scanGoFieldList(path, value.Methods, context)
			return false
		case *ast.FuncType:
			c.scanGoFieldList(path, value.TypeParams, context)
			c.scanGoFieldList(path, value.Params, context)
			c.scanGoFieldList(path, value.Results, context)
			return false
		default:
			return true
		}
	})
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

func allowedTypedProviderIdentifier(name string, expression ast.Expr) bool {
	return genericProviderIdentifier(name) && explicitThirdPartyProviderType(expression)
}

func genericProviderIdentifier(name string) bool {
	return strings.EqualFold(name, "provider") || strings.EqualFold(name, "providers")
}

func explicitThirdPartyProviderType(expression ast.Expr) bool {
	if expression == nil {
		return false
	}
	switch expression.(type) {
	case *ast.FuncType, *ast.StructType, *ast.InterfaceType:
		return false
	}

	found := false
	safe := true
	selectorTypePositions := make(map[token.Pos]bool)
	ast.Inspect(expression, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok &&
			thirdPartyProviderTypeName(selector.Sel.Name) {
			selectorTypePositions[selector.Sel.Pos()] = true
			found = true
			if !thirdPartyProviderQualifierAllowed(selector.X) {
				safe = false
			}
			return true
		}
		identifier, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if selectorTypePositions[identifier.Pos()] {
			return true
		}
		if thirdPartyProviderTypeName(identifier.Name) {
			found = true
			return true
		}
		if containsForbiddenToken(terminologyTokens(identifier.Name)) {
			safe = false
		}
		return true
	})
	return found && safe
}

func thirdPartyProviderTypeName(name string) bool {
	return name == "TracerProvider" || name == "ConfigProvider"
}

func thirdPartyProviderQualifierAllowed(expression ast.Expr) bool {
	safe := true
	ast.Inspect(expression, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		tokens := terminologyTokens(identifier.Name)
		if containsForbiddenToken(tokens) || containsDomainToken(tokens) {
			safe = false
		}
		return true
	})
	return safe
}

func explicitThirdPartyProviderValue(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.CallExpr:
		if standardThirdPartyProviderConstructor(value.Fun) {
			return true
		}
		if identifier, ok := value.Fun.(*ast.Ident); ok &&
			(identifier.Name == "make" || identifier.Name == "new") &&
			len(value.Args) > 0 {
			return explicitThirdPartyProviderType(value.Args[0])
		}
		return explicitThirdPartyProviderType(value.Fun)
	case *ast.CompositeLit:
		return explicitThirdPartyProviderType(value.Type)
	default:
		return false
	}
}

func standardThirdPartyProviderConstructor(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "NewTracerProvider" {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && qualifier.Name == "sdktrace"
}

func providerValueSpecAllowed(spec *ast.ValueSpec, index int) bool {
	if index >= len(spec.Names) || !genericProviderIdentifier(spec.Names[index].Name) {
		return false
	}
	if explicitThirdPartyProviderType(spec.Type) {
		return true
	}
	if index < len(spec.Values) {
		return explicitThirdPartyProviderValue(spec.Values[index])
	}
	return false
}

func matchedAssignmentValue(assignment *ast.AssignStmt, index int) ast.Expr {
	if index < len(assignment.Rhs) {
		return assignment.Rhs[index]
	}
	if len(assignment.Rhs) == 1 {
		return assignment.Rhs[0]
	}
	return nil
}

func identifierAllowedInScope(expression ast.Expr, scope *providerScope) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && scope.allowed(identifier.Name)
}

func bindProviderFields(scope *providerScope, fields *ast.FieldList) {
	if scope == nil || fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			scope.declare(name.Name, allowedTypedProviderIdentifier(name.Name, field.Type))
		}
	}
}

func bindUntypedRangeNames(scope *providerScope, statement *ast.RangeStmt) {
	if scope == nil || statement.Tok != token.DEFINE {
		return
	}
	for _, expression := range []ast.Expr{statement.Key, statement.Value} {
		if identifier, ok := expression.(*ast.Ident); ok {
			scope.declare(identifier.Name, false)
		}
	}
}

func goRangeBindingTokens(
	expression ast.Expr,
	declaration token.Token,
	scope *providerScope,
) []string {
	if declaration == token.DEFINE {
		if identifier, ok := expression.(*ast.Ident); ok &&
			genericProviderIdentifier(identifier.Name) {
			return sourceTokens(identifier.Name)
		}
	}
	return goNodeTokensInScope(expression, scope)
}

func goStructTagTokens(field *ast.Field, allowedJSONProvider bool) []string {
	if field.Tag == nil {
		return nil
	}
	tag, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return terminologyTokens(field.Tag.Value)
	}
	if !allowedJSONProvider {
		return terminologyTokens(tag)
	}

	var tokens []string
	matches := structTagEntryPattern.FindAllStringSubmatch(tag, -1)
	if len(matches) == 0 {
		return terminologyTokens(tag)
	}
	for _, match := range matches {
		tokens = append(tokens, terminologyTokens(match[1])...)
		name, options, _ := strings.Cut(match[2], ",")
		if match[1] != "json" || name != "provider" {
			tokens = append(tokens, terminologyTokens(name)...)
		}
		tokens = append(tokens, terminologyTokens(options)...)
	}
	return tokens
}

func goNodeTokens(node ast.Node) []string {
	return goNodeTokensInScope(node, nil)
}

func goNodeTokensInScope(node ast.Node, scope *providerScope) []string {
	if node == nil {
		return nil
	}

	nonBindingPositions := make(map[token.Pos]bool)
	hostileProviderTypePositions := make(map[token.Pos]bool)
	ast.Inspect(node, func(current ast.Node) bool {
		if literal, ok := current.(*ast.FuncLit); ok && ast.Node(literal) != node {
			return false
		}
		switch value := current.(type) {
		case *ast.SelectorExpr:
			nonBindingPositions[value.Sel.Pos()] = true
			if thirdPartyProviderTypeName(value.Sel.Name) &&
				!thirdPartyProviderQualifierAllowed(value.X) {
				hostileProviderTypePositions[value.Sel.Pos()] = true
			}
		case *ast.KeyValueExpr:
			if identifier, ok := value.Key.(*ast.Ident); ok {
				nonBindingPositions[identifier.Pos()] = true
			}
		}
		return true
	})

	var tokens []string
	ast.Inspect(node, func(current ast.Node) bool {
		if literal, ok := current.(*ast.FuncLit); ok && ast.Node(literal) != node {
			return false
		}
		switch value := current.(type) {
		case *ast.Ident:
			if !nonBindingPositions[value.Pos()] && scope.allowed(value.Name) {
				return true
			}
			if hostileProviderTypePositions[value.Pos()] {
				tokens = append(tokens, terminologyTokens(value.Name)...)
			} else {
				tokens = append(tokens, sourceTokens(value.Name)...)
			}
		case *ast.BasicLit:
			text := value.Value
			if value.Kind == token.STRING || value.Kind == token.CHAR {
				if unquoted, err := strconv.Unquote(value.Value); err == nil {
					text = unquoted
				}
			}
			tokens = append(tokens, sourceTokens(text)...)
		}
		return true
	})
	return tokens
}

func sourceTokens(text string) []string {
	tokens := terminologyTokens(text)
	filtered := make([]string, 0, len(tokens))
	for index, item := range tokens {
		// Keep the narrow third-party compound while leaving standalone names
		// such as provider or broker visible to the surrounding domain context.
		if item == "provider" && index > 0 &&
			(tokens[index-1] == "tracer" || tokens[index-1] == "config") &&
			!containsAny(tokens, "exchange", "binance", "okx") {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func extendDomainContext(context []string, tokenSets ...[]string) []string {
	// Only domain markers propagate. A violation in one child must not make
	// unrelated siblings look forbidden.
	extended := append([]string{}, context...)
	for _, tokens := range tokenSets {
		for _, item := range tokens {
			if !isDomainToken(item) || contains(extended, item) {
				continue
			}
			extended = append(extended, item)
		}
	}
	return extended
}

func isDomainToken(item string) bool {
	switch item {
	case "exchange", "trade", "binance", "okx":
		return true
	default:
		return false
	}
}

func containsDomainToken(tokens []string) bool {
	for _, item := range tokens {
		if isDomainToken(item) {
			return true
		}
	}
	return false
}

func (c *checker) addTokens(path string, line int, context, tokens []string) {
	term, ok := exchangeSynonymTokens(append(append([]string{}, context...), tokens...))
	if !ok {
		return
	}
	c.addViolation(path, line, term)
}

func (c *checker) scanText(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	c.textProviderTypeAliases, c.textHostileTypeQualifiers =
		textProviderImportAliases(string(contents))
	defer func() {
		c.textProviderTypeAliases = nil
		c.textHostileTypeQualifiers = nil
	}()

	context := ""
	depth := 0
	awaitingBrace := false
	awaitingTypeBody := false
	typeAliasBranchSeen := false
	typeAliasRequiresNext := false
	arrowAwaitingBody := false
	var lexState textLexState
	for index, line := range strings.Split(string(contents), "\n") {
		openBraces, closeBraces := textBraceCounts(line, &lexState)
		hasOpeningBrace := openBraces > 0
		declaration := false
		if match := declarationPattern.FindStringSubmatch(line); len(match) == 3 {
			kind := strings.ToLower(match[1])
			context = match[2]
			depth = 0
			declaration = true
			awaitingBrace = kind != "type" && !hasOpeningBrace
			awaitingTypeBody = kind == "type" &&
				!hasOpeningBrace &&
				typeAliasAwaitsBody(line)
			typeAliasRequiresNext = kind == "type" && typeAliasEndsWithOperator(line)
			typeAliasBranchSeen = typeAliasRequiresNext
			arrowAwaitingBody = false
		} else if match := arrowFunctionPattern.FindStringSubmatch(line); len(match) == 2 {
			context = match[1]
			depth = 0
			declaration = true
			awaitingBrace = true
			awaitingTypeBody = false
			typeAliasRequiresNext = false
			typeAliasBranchSeen = false
			arrowAwaitingBody = true
		} else if awaitingTypeBody &&
			typeAliasBranchSeen &&
			!typeAliasRequiresNext &&
			!ignorableTextLine(line) &&
			!typeAliasContinuationLine(line) &&
			!hasOpeningBrace {
			context = ""
			awaitingTypeBody = false
			typeAliasBranchSeen = false
		}
		c.addTextAllowingThirdPartyTypes(path, index+1, context+" "+c.textLineForScan(line))
		if context == "" {
			continue
		}
		if arrowAwaitingBody && arrowEndPattern.MatchString(line) && openBraces == 0 {
			context = ""
			awaitingBrace = false
			arrowAwaitingBody = false
			continue
		}

		if openBraces > 0 || depth > 0 {
			awaitingBrace = false
			arrowAwaitingBody = false
			awaitingTypeBody = false
			typeAliasBranchSeen = false
			typeAliasRequiresNext = false
			depth += openBraces - closeBraces
			if depth <= 0 && closeBraces > 0 {
				context = ""
			}
			continue
		}
		if declaration {
			if !awaitingBrace && !awaitingTypeBody {
				context = ""
			}
			continue
		}
		if awaitingTypeBody && !ignorableTextLine(line) {
			trimmed := strings.TrimSpace(line)
			endsWithOperator := typeAliasEndsWithOperator(line)
			continuation := typeAliasContinuationLine(line) ||
				typeAliasRequiresNext ||
				endsWithOperator
			if continuation {
				typeAliasBranchSeen = true
				typeAliasRequiresNext = endsWithOperator
				if strings.HasSuffix(trimmed, ";") {
					context = ""
					awaitingTypeBody = false
					typeAliasBranchSeen = false
					typeAliasRequiresNext = false
				}
			} else {
				context = ""
				awaitingTypeBody = false
			}
		}
	}
	return nil
}

func textProviderImportAliases(contents string) (map[string]bool, map[string]bool) {
	typeAliases := make(map[string]bool)
	hostileQualifiers := make(map[string]bool)
	for _, match := range textImportPattern.FindAllStringSubmatch(contents, -1) {
		clause := strings.TrimSpace(match[1])
		hostile := containsDomainToken(terminologyTokens(match[2]))
		if namespace := namespaceImportPattern.FindStringSubmatch(clause); len(namespace) == 2 {
			if hostile {
				hostileQualifiers[namespace[1]] = true
			}
		}
		if !strings.HasPrefix(clause, "{") && !strings.HasPrefix(clause, "*") {
			if defaultImport := sourceIdentifierPattern.FindString(clause); defaultImport != "" && hostile {
				hostileQualifiers[defaultImport] = true
			}
		}
		named := namedImportPattern.FindStringSubmatch(clause)
		if len(named) != 2 {
			continue
		}
		for _, entry := range strings.Split(named[1], ",") {
			parts := strings.Fields(strings.TrimSpace(entry))
			if len(parts) == 0 {
				continue
			}
			if parts[0] == "type" {
				parts = parts[1:]
			}
			if len(parts) == 0 || !thirdPartyProviderTypeName(parts[0]) {
				continue
			}
			localName := parts[0]
			if len(parts) == 3 && parts[1] == "as" {
				localName = parts[2]
			}
			typeAliases[localName] = hostile
		}
	}
	return typeAliases, hostileQualifiers
}

type textLexState struct {
	blockComment bool
	quote        byte
	regex        bool
	regexClass   bool
}

func textBraceCounts(line string, state *textLexState) (int, int) {
	var openBraces, closeBraces int
	for index := 0; index < len(line); index++ {
		current := line[index]
		if state.blockComment {
			if current == '*' && index+1 < len(line) && line[index+1] == '/' {
				state.blockComment = false
				index++
			}
			continue
		}
		if state.quote != 0 {
			if current == '\\' {
				index++
				continue
			}
			if current == state.quote {
				state.quote = 0
			}
			continue
		}
		if state.regex {
			if current == '\\' {
				index++
				continue
			}
			if current == '[' {
				state.regexClass = true
				continue
			}
			if current == ']' {
				state.regexClass = false
				continue
			}
			if current == '/' && !state.regexClass {
				state.regex = false
			}
			continue
		}
		if current == '/' && index+1 < len(line) {
			switch line[index+1] {
			case '/':
				return openBraces, closeBraces
			case '*':
				state.blockComment = true
				index++
				continue
			}
		}
		if current == '/' && startsTextRegexLiteral(line, index) {
			state.regex = true
			state.regexClass = false
			continue
		}
		switch current {
		case '"', '\'', '`':
			state.quote = current
		case '{':
			openBraces++
		case '}':
			closeBraces++
		}
	}
	// JavaScript regular-expression literals cannot continue across a raw newline.
	state.regex = false
	state.regexClass = false
	return openBraces, closeBraces
}

func startsTextRegexLiteral(line string, slash int) bool {
	prefix := strings.TrimSpace(line[:slash])
	if prefix == "" {
		return true
	}
	previous := prefix[len(prefix)-1]
	if strings.ContainsRune("=([{,:;!?&|+-*%^~<>", rune(previous)) {
		return true
	}
	for _, keyword := range []string{
		"return", "case", "throw", "yield", "await", "typeof",
		"instanceof", "in", "of", "delete", "void", "new",
	} {
		if prefix == keyword || strings.HasSuffix(prefix, " "+keyword) {
			return true
		}
	}
	return false
}

type indentedDomainContext struct {
	indent  int
	domains []string
}

func (c *checker) scanYAML(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var contexts []indentedDomainContext
	for index, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingTextIndent(line)
		for len(contexts) > 0 && contexts[len(contexts)-1].indent >= indent {
			contexts = contexts[:len(contexts)-1]
		}

		var domains []string
		if len(contexts) > 0 {
			domains = contexts[len(contexts)-1].domains
		}
		c.addTextAllowingThirdPartyTypes(
			path,
			index+1,
			strings.Join(domains, " ")+" "+line,
		)

		match := yamlMappingPattern.FindStringSubmatch(line)
		if len(match) != 5 {
			continue
		}
		key := match[1]
		if key == "" {
			key = match[2]
		}
		if key == "" {
			key = match[3]
		}
		value := strings.TrimSpace(match[4])
		if !yamlContextOnlyValuePattern.MatchString(value) {
			continue
		}
		contexts = append(contexts, indentedDomainContext{
			indent:  indent,
			domains: extendDomainContext(domains, sourceTokens(key)),
		})
	}
	return nil
}

func leadingTextIndent(line string) int {
	indent := 0
	for _, character := range line {
		switch character {
		case ' ':
			indent++
		case '\t':
			indent += 2
		default:
			return indent
		}
	}
	return indent
}

type markdownDomainContext struct {
	level   int
	domains []string
}

func (c *checker) scanMarkdown(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var contexts []markdownDomainContext
	for index, line := range strings.Split(string(contents), "\n") {
		match := markdownHeadingPattern.FindStringSubmatch(line)
		if len(match) == 3 {
			level := len(match[1])
			for len(contexts) > 0 && contexts[len(contexts)-1].level >= level {
				contexts = contexts[:len(contexts)-1]
			}
			var parentDomains []string
			if len(contexts) > 0 {
				parentDomains = contexts[len(contexts)-1].domains
			}
			c.addTextAllowingThirdPartyTypes(
				path,
				index+1,
				strings.Join(parentDomains, " ")+" "+line,
			)
			contexts = append(contexts, markdownDomainContext{
				level:   level,
				domains: extendDomainContext(parentDomains, sourceTokens(match[2])),
			})
			continue
		}

		var domains []string
		if len(contexts) > 0 {
			domains = contexts[len(contexts)-1].domains
		}
		c.addTextAllowingThirdPartyTypes(
			path,
			index+1,
			strings.Join(domains, " ")+" "+line,
		)
	}
	return nil
}

func typeAliasAwaitsBody(line string) bool {
	line, _, _ = strings.Cut(line, "//")
	trimmed := strings.TrimSpace(line)
	return strings.HasSuffix(trimmed, "=") || typeAliasEndsWithOperator(trimmed)
}

func typeAliasEndsWithOperator(line string) bool {
	line, _, _ = strings.Cut(line, "//")
	trimmed := strings.TrimSpace(line)
	return strings.HasSuffix(trimmed, "|") || strings.HasSuffix(trimmed, "&")
}

func typeAliasContinuationLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "&")
}

func ignorableTextLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" ||
		strings.HasPrefix(trimmed, "//") ||
		strings.HasPrefix(trimmed, "/*") ||
		strings.HasPrefix(trimmed, "*") ||
		strings.HasPrefix(trimmed, "#")
}

func (c *checker) textLineForScan(line string) string {
	match := thirdPartyProviderFieldPattern.FindStringSubmatch(line)
	if len(match) != 3 ||
		!genericProviderIdentifier(match[1]) ||
		!c.explicitThirdPartyProviderText(match[2]) {
		return line
	}
	return match[2]
}

func (c *checker) explicitThirdPartyProviderText(text string) bool {
	found := false
	hostilePositions := c.hostileQualifiedProviderTypePositions(text)
	for _, match := range sourceIdentifierPattern.FindAllStringIndex(text, -1) {
		identifier := text[match[0]:match[1]]
		if hostile, importedType := c.textProviderTypeAliases[identifier]; importedType {
			if hostile {
				return false
			}
			found = true
			continue
		}
		if thirdPartyProviderTypeName(identifier) {
			if hostilePositions[match[0]] {
				return false
			}
			found = true
			continue
		}
		if containsForbiddenToken(terminologyTokens(identifier)) {
			return false
		}
	}
	return found
}

func (c *checker) hostileQualifiedProviderTypePositions(text string) map[int]bool {
	positions := make(map[int]bool)
	for _, match := range qualifiedProviderTypePattern.FindAllStringSubmatchIndex(text, -1) {
		qualifier := text[match[2]:match[3]]
		tokens := terminologyTokens(qualifier)
		firstQualifier := sourceIdentifierPattern.FindString(qualifier)
		if containsForbiddenToken(tokens) ||
			containsDomainToken(tokens) ||
			c.textHostileTypeQualifiers[firstQualifier] {
			positions[match[4]] = true
		}
	}
	return positions
}

func (c *checker) addTextAllowingThirdPartyTypes(path string, line int, text string) {
	term, ok := c.exchangeSynonymWithThirdPartyTypes(text)
	if !ok {
		return
	}
	c.addViolation(path, line, term)
}

func (c *checker) addViolation(path string, line int, term string) {
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

func (c *checker) exchangeSynonymWithThirdPartyTypes(text string) (string, bool) {
	var tokens []string
	hostilePositions := c.hostileQualifiedProviderTypePositions(text)
	for _, match := range sourceIdentifierPattern.FindAllStringIndex(text, -1) {
		identifier := text[match[0]:match[1]]
		if hostile, importedType := c.textProviderTypeAliases[identifier]; importedType {
			if hostile {
				tokens = append(tokens, "provider")
			}
		} else if hostilePositions[match[0]] {
			tokens = append(tokens, terminologyTokens(identifier)...)
		} else {
			tokens = append(tokens, sourceTokens(identifier)...)
		}
	}
	return exchangeSynonymTokens(tokens)
}

func exchangeSynonymTokens(tokens []string) (string, bool) {
	if !containsAny(tokens, "exchange", "trade", "binance", "okx") {
		return "", false
	}
	for _, token := range tokens {
		if token == "provider" || token == "providers" {
			return "provider", true
		}
		for _, term := range []string{"broker", "venue", "platform"} {
			if token == term {
				return term, true
			}
		}
	}
	return "", false
}

func containsForbiddenToken(tokens []string) bool {
	return containsAny(tokens, "provider", "providers", "broker", "venue", "platform")
}

func terminologyTokens(text string) []string {
	var tokens []string
	var token strings.Builder
	flush := func() {
		if token.Len() > 0 {
			tokens = append(tokens, splitVocabularyToken(strings.ToLower(token.String()))...)
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

func splitVocabularyToken(value string) []string {
	terms := []string{
		"exchange", "trade", "binance", "okx",
		"provider", "broker", "venue", "platform",
	}
	var tokens []string
	for value != "" {
		index := -1
		term := ""
		for _, candidate := range terms {
			candidateIndex := strings.Index(value, candidate)
			if candidateIndex < 0 ||
				index >= 0 && candidateIndex > index ||
				candidateIndex == index && len(candidate) <= len(term) {
				continue
			}
			index = candidateIndex
			term = candidate
		}
		if index < 0 {
			tokens = append(tokens, value)
			break
		}
		if index > 0 {
			tokens = append(tokens, value[:index])
		}
		tokens = append(tokens, term)
		value = value[index+len(term):]
	}
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
