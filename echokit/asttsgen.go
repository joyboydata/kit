package echokit

import (
	"fmt"
	"go/types"
	"strings"
)

// ============================================================================
// AST TS Generator — generates TypeScript type expressions from go/types types.
// Mirrors the reflection-based generation in piextgen but uses AST types.
// ============================================================================

// bt is the backtick character for TypeScript template literals.
const bt = "`"

// TSTypeGenerator generates TypeScript type expression strings from go/types types.
// Named types are collected and output as shared type definitions.
// Uses two-phase logic: "visiting" prevents cycles during typeToTSExpr,
// "built" tracks completed type definitions.
type TSTypeGenerator struct {
	namedTypes map[string]string      // type name → TS expression (for type definitions)
	typeOrder  []string               // topological sort order
	built      map[string]bool        // types already built
	visiting   map[string]bool        // cycle detection during expression generation
}

// NewTSTypeGenerator creates a new TypeScript type generator.
func NewTSTypeGenerator() *TSTypeGenerator {
	return &TSTypeGenerator{
		namedTypes: map[string]string{},
		built:      map[string]bool{},
		visiting:   map[string]bool{},
	}
}

// CollectType registers a TypeRef for shared type generation.
// It recursively discovers and registers dependency types.
func (g *TSTypeGenerator) CollectType(ref *TypeRef) {
	if ref == nil {
		return
	}
	named, ok := ref.Type.(*types.Named)
	if !ok {
		return
	}
	g.collectNamedStruct(named)
}

// collectNamedStruct registers a named struct type and all its dependencies.
// Uses visiting/visited (built) maps for cycle detection.
func (g *TSTypeGenerator) collectNamedStruct(named *types.Named) {
	ref := &TypeRef{
		PkgPath: named.Obj().Pkg().Path(),
		Name:    named.Obj().Name(),
		Type:    named,
	}

	// Skip already built.
	if g.built[ref.String()] {
		return
	}

	// Skip non-struct named types.
	if isTimeType(named) || isJSONRawMessageType(named) {
		return
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return
	}

	// Cycle detection — guard against infinite recursion.
	if g.visiting[ref.String()] {
		return
	}
	g.visiting[ref.String()] = true

	// First, recursively collect all field dependencies.
	// This registers them as "built" so typeToTSExpr will reference them by name.
	st := named.Underlying().(*types.Struct)
	for i := 0; i < st.NumFields(); i++ {
		g.collectFromType(st.Field(i).Type())
	}

	// Generate the expression BEFORE marking this type as built.
	// This way typeToTSExpr sees this type as NOT built and expands it into
	// a Type.Object({...}) with field types referencing already-built deps.
	delete(g.visiting, ref.String())
	expr := g.typeToTSExpr(named, 0)

	// Now mark as built so dependents can reference this type by name.
	g.built[ref.String()] = true
	g.namedTypes[ref.String()] = expr
	g.typeOrder = append(g.typeOrder, ref.String())
}

// collectFromType recursively collects a type's named struct dependencies.
func (g *TSTypeGenerator) collectFromType(t types.Type) {
	t = derefType(t)

	switch typ := t.(type) {
	case *types.Named:
		g.collectNamedStruct(typ)

	case *types.Struct:
		for i := 0; i < typ.NumFields(); i++ {
			g.collectFromType(typ.Field(i).Type())
		}

	case *types.Slice:
		g.collectFromType(typ.Elem())

	case *types.Map:
		g.collectFromType(typ.Elem())

	case *types.Pointer:
		g.collectFromType(typ.Elem())
	}
}

// NamedTypes returns the collected type definitions keyed by clean TS type name.
func (g *TSTypeGenerator) NamedTypes() map[string]string {
	result := make(map[string]string, len(g.namedTypes))
	for qualified, expr := range g.namedTypes {
		tsName := cleanTSTypeName(qualified)
		result[tsName] = expr
	}
	return result
}

// TypeOrder returns the dependency-ordered list of TS type names.
func (g *TSTypeGenerator) TypeOrder() []string {
	result := make([]string, 0, len(g.typeOrder))
	for _, q := range g.typeOrder {
		result = append(result, cleanTSTypeName(q))
	}
	return result
}

// cleanTSTypeName converts a qualified Go type name like
// "github.com/joyboydata/slicer/internal/specification.BehavioralSegmentation"
// into a clean TypeScript identifier like "SpecificationBehavioralSegmentation".
func cleanTSTypeName(qualified string) string {
	// Extract the last meaningful segment before the type name.
	// The qualified format is "pkg/path.Name".
	lastDot := strings.LastIndex(qualified, ".")
	if lastDot < 0 {
		return qualified
	}
	pkgPath := qualified[:lastDot]
	typeName := qualified[lastDot+1:]
	
	// Get the last component of the package path.
	pkgParts := strings.Split(pkgPath, "/")
	shortPkg := ""
	for i := len(pkgParts) - 1; i >= 0; i-- {
		if pkgParts[i] != "" {
			shortPkg = pkgParts[i]
			break
		}
	}
	
	// Capitalize the first letter.
	if len(shortPkg) > 0 {
		shortPkg = strings.ToUpper(shortPkg[:1]) + shortPkg[1:]
	}
	
	return shortPkg + typeName
}

// TSName returns the TypeScript identifier for a TypeRef.
func (g *TSTypeGenerator) TSName(ref *TypeRef) string {
	return cleanTSTypeName(ref.PkgPath + "." + ref.Name)
}

// InlineExpr returns a TypeScript type expression for a TypeRef, inlining if needed.
func (g *TSTypeGenerator) InlineExpr(ref *TypeRef) string {
	if ref == nil {
		return "Type.Any()"
	}

	// Named types — reference by name if we have a definition.
	if _, ok := ref.Type.(*types.Named); ok && g.built[ref.String()] {
		return g.TSName(ref)
	}

	return g.typeToTSExpr(ref.Type, 0)
}

// typeToTSExpr converts a go/types.Type to a TypeScript type expression.
func (g *TSTypeGenerator) typeToTSExpr(t types.Type, depth int) string {
	t = derefType(t)

	switch typ := t.(type) {
	case *types.Named:
		if isTimeType(typ) {
			return "Type.String()"
		}
		if isJSONRawMessageType(typ) {
			return "Type.Any()"
		}
		ref := &TypeRef{
			PkgPath: typ.Obj().Pkg().Path(),
			Name:    typ.Obj().Name(),
			Type:    typ,
		}
		if g.built[ref.String()] {
			return g.TSName(ref)
		}
		// Not collected — inline it from the underlying struct.
		if st, ok := typ.Underlying().(*types.Struct); ok {
			return g.structToTS(st, depth)
		}
		return g.typeToTSExpr(typ.Underlying(), depth)

	case *types.Basic:
		return basicTSType(typ.Kind())

	case *types.Pointer:
		elem := g.typeToTSExpr(typ.Elem(), depth)
		return elem // pointers are optional in TS, handled via json tag

	case *types.Slice:
		elem := g.typeToTSExpr(typ.Elem(), depth)
		return fmt.Sprintf("Type.Array(%s)", elem)

	case *types.Map:
		val := g.typeToTSExpr(typ.Elem(), depth)
		return fmt.Sprintf("Type.Record(Type.String(), %s)", val)

	case *types.Struct:
		return g.structToTS(typ, depth)

	default:
		return "Type.Any()"
	}
}

// structToTS converts a *types.Struct to a Type.Object({...}) expression.
func (g *TSTypeGenerator) structToTS(st *types.Struct, depth int) string {
	var fields []string

	for i := 0; i < st.NumFields(); i++ {
		field := st.Field(i)
		if !field.Exported() {
			continue
		}
		if field.Anonymous() {
			continue
		}

		jsonName, opts := fieldJSONInfo(st, i)
		if jsonName == "-" {
			continue
		}

		ft := field.Type()
		isPtr := isPointer(ft)
		ft = derefType(ft)

		fieldTS := g.typeToTSExpr(ft, depth+1)

		_, omitempty := opts["omitempty"]
		optional := omitempty || isPtr

		// Try to get description from the description struct tag.
		tagStr := st.Tag(i)
		desc, _ := reflectTagLookup(tagStr, "description")

		indent := strings.Repeat("\t", depth)
		if optional {
			if desc != "" {
				fields = append(fields, fmt.Sprintf("%s\t\t%s: Type.Optional(%s, { description: %s }),",
					indent, jsonName, fieldTS, bt+desc+bt))
			} else {
				fields = append(fields, fmt.Sprintf("%s\t\t%s: Type.Optional(%s),",
					indent, jsonName, fieldTS))
			}
		} else {
			if desc != "" {
				fields = append(fields, fmt.Sprintf("%s\t\t%s: %s, // %s",
					indent, jsonName, fieldTS, desc))
			} else {
				fields = append(fields, fmt.Sprintf("%s\t\t%s: %s,",
					indent, jsonName, fieldTS))
			}
		}
	}

	indent := strings.Repeat("\t", depth)
	if len(fields) == 0 {
		return fmt.Sprintf("Type.Object({%s})", indent)
	}

	return fmt.Sprintf("Type.Object({\n%s\n%s\t})",
		strings.Join(fields, "\n"), indent)
}

// ============================================================================
// Helpers
// ============================================================================

// tsTypeNameFromPkg generates a TypeScript type name from a Go package path and type name.
func tsTypeNameFromPkg(pkgPath, typeName string) string {
	parts := strings.Split(pkgPath, "/")
	prefix := ""
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" && !strings.HasPrefix(parts[i], ".") {
			prefix = parts[i]
			break
		}
	}
	// Capitalize first letter.
	if len(prefix) > 0 {
		prefix = strings.ToUpper(prefix[:1]) + prefix[1:]
	}
	return prefix + typeName
}

// basicTSType maps a Go basic kind to a TypeScript type expression.
func basicTSType(kind types.BasicKind) string {
	switch kind {
	case types.String:
		return "Type.String()"
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
		types.Float32, types.Float64:
		return "Type.Number()"
	case types.Bool:
		return "Type.Boolean()"
	default:
		return "Type.Any()"
	}
}
