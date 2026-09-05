// Package echokit provides an opinionated layer over Echo for route
// grouping, registration, and OpenAPI spec generation.
//
// Group discovery uses go/ast + go/types to scan all packages in a Go module
// for functions returning echokit.Group. This eliminates the need for
// codegen tools to hardcode imports to handler packages.
//
// Usage:
//
//	import "github.com/joyboydata/kit/echokit"
//
//	groups, err := echokit.DiscoverGroups(echokit.DiscoverConfig{})
//	// groups contains all route definitions parsed from AST,
//	// with Body types and Response model types resolved via go/types.
package echokit

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ============================================================================
// AST-parsed representations of echokit types
// ============================================================================

// TypeRef holds a resolved Go type reference discovered via AST/go/types.
// It carries both the source-level identity (PkgPath.Name) and the
// resolved types.Type for schema generation.
type TypeRef struct {
	PkgPath string      // e.g. "github.com/joyboydata/kit/specification"
	Name    string      // e.g. "BehavioralSegmentation"
	Type    types.Type  // The resolved go/types Type
}

// String returns a human-readable representation for debugging.
func (t *TypeRef) String() string {
	return t.PkgPath + "." + t.Name
}

// ASTOpenAPI holds OpenAPI metadata parsed from AST.
// It mirrors echokit.OpenAPI but uses TypeRef instead of reflect.Type.
type ASTOpenAPI struct {
	Summary     string
	Description string
	Tags        []string
	BodyType    *TypeRef
	Responses   map[int]ASTResponseDef
}

// ASTResponseDef holds a response definition parsed from AST.
type ASTResponseDef struct {
	Description string
	ModelType   *TypeRef // nil defaults to map[string]string
}

// ASTRoute holds a route definition parsed from AST.
// It mirrors echokit.Route but uses AST-parsed metadata instead of
// reflect.Type-based fields. The Handler field is not extracted
// (it's only needed at runtime, not for codegen).
type ASTRoute struct {
	Method  string
	Pattern string
	OpenAPI *ASTOpenAPI
	Tool    *Tool
}

// ASTGroup holds an echokit.Group parsed from AST.
type ASTGroup struct {
	BasePath string
	Routes   []ASTRoute
	FuncName string  // Name of the constructor function, e.g. "NewHandlerSegments"
	PkgPath  string  // Package where the function is defined
}

// DiscoverConfig controls the group discovery process.
type DiscoverConfig struct {
	// ModuleDir is the root directory of the Go module to scan.
	// If empty, the current working directory is used.
	ModuleDir string

	// EChokitPkgPath is the Go package path for x/echokit.
	// If empty, defaults to {module path}/x/echokit.
	// Set this when echokit lives in a different module (e.g. "github.com/joyboydata/kit/echokit").
	EChokitPkgPath string

	// AppName is the human-readable application name used in generated
	// TypeScript code (env var names, error messages).
	// If empty, it is derived from the last segment of the Go module path.
	// Example: module "github.com/joyboydata/slicer" → "Slicer"
	AppName string
}

// ModuleAppName derives an application name from the Go module path.
// It reads the go.mod in the given directory and extracts the last
// segment of the module path, then capitalizes the first letter.
// Returns "App" as fallback if reading fails.
func ModuleAppName(moduleDir string) string {
	goModPath := filepath.Join(moduleDir, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return "App"
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "module ")), "/")
			last := parts[len(parts)-1]
			if len(last) > 0 {
				return strings.ToUpper(last[:1]) + last[1:]
			}
		}
	}
	return "App"
}

// ============================================================================
// Public API
// ============================================================================

// DiscoverGroups scans all packages in the module and discovers functions
// returning echokit.Group. It parses their return composite literals to
// extract route definitions (Method, Pattern, OpenAPI metadata, Tool
// metadata) and resolves referenced types via go/types.
//
// Body and Response model types are returned as TypeRef values, which
// can be passed to SchemaFromType (for JSON Schema) or TSTypeFromType
// (for TypeScript type generation).
func DiscoverGroups(cfg DiscoverConfig) ([]ASTGroup, error) {
	moduleDir := cfg.ModuleDir
	if moduleDir == "" {
		var err error
		moduleDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("echokit discover: getwd: %w", err)
		}
	}

	// Load all packages in the module.
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir:   moduleDir,
		Tests: false,
	}, "./...")
	if err != nil {
		return nil, fmt.Errorf("echokit discover: load packages: %w", err)
	}

	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("echokit discover: package loading errors")
	}

	echokitPkgPath := cfg.EChokitPkgPath
	if echokitPkgPath == "" {
		echokitPkgPath = modulePkgPath(pkgs, moduleDir) + "/x/echokit"
	}

	// Phase 1: Find all functions returning echokit.Group.
	type discoveredFunc struct {
		pkg      *packages.Package
		file     *ast.File
		funcDecl *ast.FuncDecl
	}
	var candidates []discoveredFunc

	for _, pkg := range pkgs {
		if len(pkg.Syntax) == 0 {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				funcDecl, ok := decl.(*ast.FuncDecl)
				if !ok || funcDecl.Type.Results == nil {
					continue
				}
				if returnsEchokitGroup(funcDecl, pkg.TypesInfo, echokitPkgPath) {
					candidates = append(candidates, discoveredFunc{
						pkg:      pkg,
						file:     file,
						funcDecl: funcDecl,
					})
				}
			}
		}
	}

	// Phase 2: Parse each candidate's return statement into an ASTGroup.
	var groups []ASTGroup
	for _, c := range candidates {
		group, err := parseGroupReturn(c.pkg, c.file, c.funcDecl, echokitPkgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "echokit discover: warning: %s.%s: %v\n",
				c.pkg.PkgPath, c.funcDecl.Name.Name, err)
			continue
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}

	return groups, nil
}

// ============================================================================
// Type checking helpers
// ============================================================================

// returnsEchokitGroup checks if a function's result list includes echokit.Group.
func returnsEchokitGroup(fn *ast.FuncDecl, info *types.Info, echokitPkg string) bool {
	for _, result := range fn.Type.Results.List {
		t := info.TypeOf(result.Type)
		if t == nil {
			continue
		}
		named, ok := t.(*types.Named)
		if !ok {
			continue
		}
		if named.Obj().Name() == "Group" && named.Obj().Pkg().Path() == echokitPkg {
			return true
		}
	}
	return false
}

// resolveBasicLit extracts a string literal value from an AST expression.
func resolveBasicLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok {
		return "", false
	}
	if lit.Kind != token.STRING {
		return "", false
	}
	val, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return val, true
}

// resolveStringSlice extracts a []string literal like []string{"a", "b"}.
func resolveStringSlice(expr ast.Expr) ([]string, bool) {
	compLit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	var result []string
	for _, elt := range compLit.Elts {
		s, ok := resolveBasicLit(elt)
		if !ok {
			return nil, false
		}
		result = append(result, s)
	}
	return result, true
}

// isNilExpr checks if an expression is the identifier "nil".
func isNilExpr(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "nil"
}

// ============================================================================
// Parsing the return statement
// ============================================================================

// parseGroupReturn finds the return statement in a function and parses
// the echokit.Group composite literal from it.
func parseGroupReturn(pkg *packages.Package, file *ast.File, fn *ast.FuncDecl, echokitPkg string) (*ASTGroup, error) {
	// Find the return statement.
	var retStmt *ast.ReturnStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if rs, ok := n.(*ast.ReturnStmt); ok {
			retStmt = rs
			return false
		}
		return true
	})
	if retStmt == nil || len(retStmt.Results) == 0 {
		return nil, nil
	}

	// The return value should be a composite literal for echokit.Group{...}.
	compLit, ok := retStmt.Results[0].(*ast.CompositeLit)
	if !ok {
		return nil, nil
	}

	return parseGroupLiteral(pkg, file, compLit, echokitPkg)
}

// parseGroupLiteral parses a echokit.Group{...} composite literal.
func parseGroupLiteral(pkg *packages.Package, file *ast.File, lit *ast.CompositeLit, echokitPkg string) (*ASTGroup, error) {
	group := &ASTGroup{
		PkgPath: pkg.PkgPath,
	}

	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}

		switch key.Name {
		case "BasePath":
			s, ok := resolveBasicLit(kv.Value)
			if !ok {
				return nil, fmt.Errorf("BasePath is not a string literal")
			}
			group.BasePath = s

		case "Routes":
			routes, err := parseRoutesSlice(pkg, file, kv.Value, echokitPkg)
			if err != nil {
				return nil, fmt.Errorf("Routes: %w", err)
			}
			group.Routes = routes
		}
	}

	return group, nil
}

// parseRoutesSlice parses a []echokit.Route{...} composite literal.
func parseRoutesSlice(pkg *packages.Package, file *ast.File, expr ast.Expr, echokitPkg string) ([]ASTRoute, error) {
	compLit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("Routes is not a composite literal")
	}

	var routes []ASTRoute
	for _, elt := range compLit.Elts {
		routeLit, ok := elt.(*ast.CompositeLit)
		if !ok {
			continue
		}
		route, err := parseRouteLiteral(pkg, file, routeLit, echokitPkg)
		if err != nil {
			return nil, fmt.Errorf("route: %w", err)
		}
		if route != nil {
			routes = append(routes, *route)
		}
	}
	return routes, nil
}

// parseRouteLiteral parses a single echokit.Route{...} composite literal.
func parseRouteLiteral(pkg *packages.Package, file *ast.File, lit *ast.CompositeLit, echokitPkg string) (*ASTRoute, error) {
	route := &ASTRoute{}

	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}

		switch key.Name {
		case "Method":
			s, ok := resolveStringExpr(kv.Value, pkg.TypesInfo)
			if !ok {
				continue // skip unparseable
			}
			route.Method = s

		case "Pattern":
			s, ok := resolveStringExpr(kv.Value, pkg.TypesInfo)
			if !ok {
				continue
			}
			route.Pattern = s

		case "OpenAPI":
			api, err := parseOpenAPILiteral(pkg, file, kv.Value, echokitPkg)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI: %w", err)
			}
			route.OpenAPI = api

		case "Tool":
			tool, err := parseToolLiteral(kv.Value)
			if err != nil {
				return nil, fmt.Errorf("Tool: %w", err)
			}
			route.Tool = tool
		}
	}

	return route, nil
}

// parseOpenAPILiteral parses an &echokit.OpenAPI{...} or echokit.OpenAPI{...} literal.
// Handles both pointer and value forms.
func parseOpenAPILiteral(pkg *packages.Package, file *ast.File, expr ast.Expr, echokitPkg string) (*ASTOpenAPI, error) {
	// Unwrap & if pointer.
	inner := expr
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		inner = unary.X
	}

	if isNilExpr(inner) {
		return nil, nil
	}

	compLit, ok := inner.(*ast.CompositeLit)
	if !ok {
		return nil, nil
	}

	api := &ASTOpenAPI{
		Responses: map[int]ASTResponseDef{},
	}

	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}

		switch key.Name {
		case "Summary":
			s, ok := resolveBasicLit(kv.Value)
			if !ok {
				continue
			}
			api.Summary = s

		case "Description":
			s, ok := resolveBasicLit(kv.Value)
			if !ok {
				continue
			}
			api.Description = s

		case "Tags":
			tags, ok := resolveStringSlice(kv.Value)
			if !ok {
				continue
			}
			api.Tags = tags

		case "Body":
			ref := resolveTypeRef(kv.Value, pkg.TypesInfo)
			if ref != nil {
				api.BodyType = ref
			}

		case "Responses":
			respMap, err := parseResponsesLiteral(pkg, file, kv.Value)
			if err != nil {
				return nil, fmt.Errorf("Responses: %w", err)
			}
			api.Responses = respMap
		}
	}

	return api, nil
}

// parseResponsesLiteral parses a map[int]echokit.ResponseDef{...} literal.
func parseResponsesLiteral(pkg *packages.Package, file *ast.File, expr ast.Expr) (map[int]ASTResponseDef, error) {
	compLit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("not a composite literal")
	}

	responses := map[int]ASTResponseDef{}

	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		// Key is the HTTP status code (int literal or constant like http.StatusOK).
		code, ok := resolveIntExpr(kv.Key, pkg.TypesInfo)
		if !ok {
			continue
		}

		// Value is a ResponseDef{...} composite literal.
		respLit, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			continue
		}

		def := ASTResponseDef{}
		for _, field := range respLit.Elts {
			fkv, ok := field.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			fkey, ok := fkv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch fkey.Name {
			case "Description":
				s, ok := resolveBasicLit(fkv.Value)
				if !ok {
					continue
				}
				def.Description = s
			case "Model":
				ref := resolveTypeRef(fkv.Value, pkg.TypesInfo)
				if ref != nil {
					def.ModelType = ref
				}
			}
		}
		responses[code] = def
	}

	return responses, nil
}

// parseToolLiteral parses an &echokit.Tool{...} or echokit.Tool{...} literal.
func parseToolLiteral(expr ast.Expr) (*Tool, error) {
	// Unwrap & if pointer.
	inner := expr
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		inner = unary.X
	}

	if isNilExpr(inner) {
		return nil, nil
	}

	compLit, ok := inner.(*ast.CompositeLit)
	if !ok {
		return nil, nil
	}

	tool := &Tool{}

	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}

		switch key.Name {
		case "Name":
			s, ok := resolveBasicLit(kv.Value)
			if !ok {
				continue
			}
			tool.Name = s
		case "Description":
			s, ok := resolveBasicLit(kv.Value)
			if !ok {
				continue
			}
			tool.Description = s
		case "Example":
			s, ok := resolveBasicLit(kv.Value)
			if !ok {
				continue
			}
			tool.Example = s
		case "Disabled":
			// Only parse if it's a boolean literal.
			if bl, ok := kv.Value.(*ast.Ident); ok && bl.Name == "true" {
				tool.Disabled = true
			}
		}
	}

	return tool, nil
}

// ============================================================================
// String expression resolution
// ============================================================================

// resolveStringExpr resolves an expression to a string value.
// Handles:
//   - String literals: "GET"
//   - Constant references: http.MethodGet, http.MethodPost
// Returns empty string and false if unresolvable.
func resolveStringExpr(expr ast.Expr, info *types.Info) (string, bool) {
	// Direct string literal.
	if s, ok := resolveBasicLit(expr); ok {
		return s, true
	}

	// Constant reference (selector expression like http.MethodGet).
	typeInfo := info.Types[expr]
	if typeInfo.Value == nil {
		return "", false
	}
	// Use StringVal() which returns the unquoted value, unlike ExactString()
	// which returns "POST" (with quotes) for string constants.
	return constant.StringVal(typeInfo.Value), true
}

// resolveIntExpr resolves an expression to an int value.
// Handles integer literals and constant references like http.StatusOK.
func resolveIntExpr(expr ast.Expr, info *types.Info) (int, bool) {
	// Integer literal.
	if bl, ok := expr.(*ast.BasicLit); ok && bl.Kind == token.INT {
		val, err := strconv.Atoi(bl.Value)
		if err != nil {
			return 0, false
		}
		return val, true
	}

	// Constant reference.
	typeInfo := info.Types[expr]
	if typeInfo.Value == nil {
		return 0, false
	}
	// Use Int64Val which returns the raw numeric value, avoiding issues with ExactString().
	val, ok := constant.Int64Val(typeInfo.Value)
	if !ok {
		return 0, false
	}
	return int(val), true
}

// ============================================================================
// Type reference resolution (the key piece)
// ============================================================================

// resolveTypeRef extracts the Go type from a Body or Model expression.
// Handles two patterns used in handler constructors:
//
//  1. typeOf[SomeType]()  — generic helper pattern
//  2. reflect.TypeOf((*SomeType)(nil)).Elem()  — reflection pattern
//  3. A bare type reference when the info.TypeOf gives us the type directly
//
// Returns nil if the type cannot be resolved.
func resolveTypeRef(expr ast.Expr, info *types.Info) *TypeRef {
	if expr == nil || isNilExpr(expr) {
		return nil
	}

	// Pattern 1: typeOf[SomeType]() — call with generic instantiation.
	if callExpr, ok := expr.(*ast.CallExpr); ok {
		if idxExpr, ok := callExpr.Fun.(*ast.IndexExpr); ok {
			return resolveTypeFromInstantiation(idxExpr, info)
		}
		// Pattern 2 (nested): reflect.TypeOf((*SomeType)(nil)).Elem()
		// Top-level is CallExpr(.Elem on reflect.TypeOf(...)) with no args.
		if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok && selExpr.Sel.Name == "Elem" && len(callExpr.Args) == 0 {
			return resolveTypeFromReflectTypeOf(selExpr.X, info)
		}
	}

	// Pattern 3: Bare type reference — use info.TypeOf to get the type.
	if named := namedFromInfo(expr, info); named != nil {
		return named
	}

	return nil
}

// resolveTypeFromInstantiation handles typeOf[SomeType]() pattern.
// It extracts the type argument from the generic instantiation via
// TypesInfo.Instances.
// The Instances map is keyed by the *ast.Ident of the generic function name.
func resolveTypeFromInstantiation(idxExpr *ast.IndexExpr, info *types.Info) *TypeRef {
	ident, ok := idxExpr.X.(*ast.Ident)
	if !ok {
		return nil
	}
	inst, ok := info.Instances[ident]
	if !ok || inst.TypeArgs == nil || inst.TypeArgs.Len() == 0 {
		return nil
	}

	// Get the first type argument — this is the T in typeOf[T]().
	typeArg := inst.TypeArgs.At(0)
	return typeRefFromType(typeArg)
}

// resolveTypeFromReflectTypeOf handles reflect.TypeOf((*SomeType)(nil)).Elem().
// It traces through the call chain to find the type conversion argument.
func resolveTypeFromReflectTypeOf(inner ast.Expr, info *types.Info) *TypeRef {
	innerCall, ok := inner.(*ast.CallExpr)
	if !ok {
		return nil
	}

	// innerCall should be reflect.TypeOf(...)
	if len(innerCall.Args) != 1 {
		return nil
	}

	typeConv := innerCall.Args[0]

	// The argument is (*SomeType)(nil) — a type conversion.
	// info.TypeOf(typeConv) gives us *SomeType.
	typeInfo := info.Types[typeConv]
	if typeInfo.Type == nil {
		return nil
	}

	return typeRefFromType(typeInfo.Type)
}

// typeRefFromType extracts a TypeRef from a go/types Type, unwrapping pointers.
func typeRefFromType(t types.Type) *TypeRef {
	// Unwrap pointer.
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}

	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}

	return &TypeRef{
		PkgPath: named.Obj().Pkg().Path(),
		Name:    named.Obj().Name(),
		Type:    named,
	}
}

// namedFromInfo extracts a TypeRef from an expression using info.TypeOf.
// This handles bare type references where the expression's resolved type
// is directly the named type we want.
func namedFromInfo(expr ast.Expr, info *types.Info) *TypeRef {
	t := info.TypeOf(expr)
	if t == nil {
		return nil
	}
	return typeRefFromType(t)
}

// ============================================================================
// Module path discovery
// ============================================================================

// modulePkgPath discovers the module's package path from go.mod.
func modulePkgPath(pkgs []*packages.Package, moduleDir string) string {
	// Try reading go.mod directly.
	goModPath := filepath.Join(moduleDir, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "module ") {
				return strings.TrimSpace(strings.TrimPrefix(line, "module "))
			}
		}
	}

	// Fallback: derive from the first package.
	for _, pkg := range pkgs {
		return pkg.PkgPath
	}
	return ""
}
