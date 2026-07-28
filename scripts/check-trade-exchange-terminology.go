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

var declarationPattern = regexp.MustCompile(`(?i)^\s*(?:(?:export|declare)\s+)*(message|interface|type|class|enum|service)\s+([A-Za-z_][A-Za-z0-9_]*)`)
var sourceIdentifierPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
var thirdPartyProviderFieldPattern = regexp.MustCompile(`^\s*(?:readonly\s+)?([A-Za-z_][A-Za-z0-9_]*)\??\s*:\s*(.+?)\s*[;,]?\s*$`)
var structTagEntryPattern = regexp.MustCompile(`([A-Za-z0-9_-]+):"([^"]*)"`)
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
			c.addTextAllowingThirdPartyTypes(path, c.fset.Position(comment.Pos()).Line, comment.Text)
		}
	}

	for _, declaration := range file.Decls {
		c.scanGoDeclaration(path, declaration, nil)
	}
	return nil
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
			}
		}
	}
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
		walkGoNode(branchVisitor, value.Cond)
		walkGoNode(branchVisitor, value.Body)
		walkGoNode(branchVisitor, value.Else)
		return nil
	case *ast.ForStmt:
		loopVisitor := v.withScope(newProviderScope(v.scope))
		walkGoNode(loopVisitor, value.Init)
		walkGoNode(loopVisitor, value.Cond)
		walkGoNode(loopVisitor, value.Post)
		walkGoNode(loopVisitor, value.Body)
		return nil
	case *ast.SwitchStmt:
		switchVisitor := v.withScope(newProviderScope(v.scope))
		walkGoNode(switchVisitor, value.Init)
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
	ast.Inspect(expression, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		switch identifier.Name {
		case "TracerProvider", "ConfigProvider":
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
	ast.Inspect(node, func(current ast.Node) bool {
		if literal, ok := current.(*ast.FuncLit); ok && ast.Node(literal) != node {
			return false
		}
		switch value := current.(type) {
		case *ast.SelectorExpr:
			nonBindingPositions[value.Sel.Pos()] = true
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
			tokens = append(tokens, sourceTokens(value.Name)...)
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

	context := ""
	depth := 0
	awaitingBrace := false
	awaitingTypeBody := false
	typeAliasBranchSeen := false
	typeAliasRequiresNext := false
	for index, line := range strings.Split(string(contents), "\n") {
		declaration := false
		if match := declarationPattern.FindStringSubmatch(line); len(match) == 3 {
			kind := strings.ToLower(match[1])
			context = match[2]
			depth = 0
			declaration = true
			awaitingBrace = kind != "type" && !strings.Contains(line, "{")
			awaitingTypeBody = kind == "type" &&
				!strings.Contains(line, "{") &&
				typeAliasAwaitsBody(line)
			typeAliasRequiresNext = kind == "type" && typeAliasEndsWithOperator(line)
			typeAliasBranchSeen = typeAliasRequiresNext
		} else if awaitingTypeBody &&
			typeAliasBranchSeen &&
			!typeAliasRequiresNext &&
			!ignorableTextLine(line) &&
			!typeAliasContinuationLine(line) &&
			!strings.Contains(line, "{") {
			context = ""
			awaitingTypeBody = false
			typeAliasBranchSeen = false
		}
		c.addTextAllowingThirdPartyTypes(path, index+1, context+" "+textLineForScan(line))
		if context == "" {
			continue
		}

		openBraces := strings.Count(line, "{")
		closeBraces := strings.Count(line, "}")
		if openBraces > 0 || depth > 0 {
			awaitingBrace = false
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

func textLineForScan(line string) string {
	match := thirdPartyProviderFieldPattern.FindStringSubmatch(line)
	if len(match) != 3 ||
		!genericProviderIdentifier(match[1]) ||
		!explicitThirdPartyProviderText(match[2]) {
		return line
	}
	return match[2]
}

func explicitThirdPartyProviderText(text string) bool {
	found := false
	for _, identifier := range sourceIdentifierPattern.FindAllString(text, -1) {
		switch identifier {
		case "TracerProvider", "ConfigProvider":
			found = true
		default:
			if containsForbiddenToken(terminologyTokens(identifier)) {
				return false
			}
		}
	}
	return found
}

func (c *checker) addTextAllowingThirdPartyTypes(path string, line int, text string) {
	term, ok := exchangeSynonymWithThirdPartyTypes(text)
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

func exchangeSynonymWithThirdPartyTypes(text string) (string, bool) {
	var tokens []string
	for _, identifier := range sourceIdentifierPattern.FindAllString(text, -1) {
		tokens = append(tokens, sourceTokens(identifier)...)
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
