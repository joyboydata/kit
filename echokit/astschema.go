package echokit

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
)

// ============================================================================
// ASTSchemaBuilder builds JSON Schema from go/types types.
// It uses AST analysis to extract struct field tags (json, description)
// which go/types does not expose directly.
// ============================================================================

// SchemaConfig controls JSON Schema generation behavior.
type SchemaConfig struct {
	// ModuleDir overrides the working directory for package loading.
	ModuleDir string
}

// ASTSchemaBuilder builds JSON Schema definitions from types.
// Multiple types share the same builder to produce $ref relationships
// for named types, avoiding duplicate inline definitions.
type ASTSchemaBuilder struct {
	cfg       SchemaConfig
	typeDefs  map[string]bool         // Types already built as definitions
	builtDefs map[string]interface{}  // Built definitions keyed by type name
}

// NewASTSchemaBuilder creates a schema builder.
func NewASTSchemaBuilder(cfg SchemaConfig) *ASTSchemaBuilder {
	return &ASTSchemaBuilder{
		cfg:       cfg,
		typeDefs:  map[string]bool{},
		builtDefs: map[string]interface{}{},
	}
}

// Schema returns the JSON Schema for a TypeRef, using $ref for named types.
// It registers the type and all its dependencies as definitions.
func (b *ASTSchemaBuilder) Schema(ref *TypeRef) interface{} {
	return b.typeSchema(ref)
}

// CollectDefinitions populates the builder's definition cache for all
// types that will be used, registered from route metadata.
func (b *ASTSchemaBuilder) CollectDefinitions(refs []*TypeRef) {
	for _, ref := range refs {
		if ref != nil {
			b.collectType(ref)
		}
	}
}

// Definitions returns all collected type definitions suitable for
// the "definitions" section of a Swagger 2.0 spec.
func (b *ASTSchemaBuilder) Definitions() map[string]interface{} {
	defs := make(map[string]interface{}, len(b.builtDefs))
	names := make([]string, 0, len(b.builtDefs))
	for name := range b.builtDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		defs[name] = b.builtDefs[name]
	}
	return defs
}

// typeSchema returns the JSON Schema for a TypeRef.
// Named struct types get $ref; primitives and composites are inlined.
func (b *ASTSchemaBuilder) typeSchema(ref *TypeRef) interface{} {
	if ref == nil {
		return map[string]interface{}{
			"type": "object",
			"additionalProperties": map[string]interface{}{
				"type": "string",
			},
		}
	}
	return b.schemaForType(ref.Type)
}

// schemaForType builds a JSON Schema from a go/types.Type.
func (b *ASTSchemaBuilder) schemaForType(t types.Type) interface{} {
	t = derefType(t)

	switch typ := t.(type) {
	case *types.Named:
		// Ensure definition is built, then return $ref.
		name := typeDefName(typ)
		b.buildTypeDef(typ)
		return map[string]interface{}{
			"$ref": fmt.Sprintf("#/definitions/%s", name),
		}

	case *types.Basic:
		return basicSchema(typ.Kind())

	case *types.Pointer:
		// Pointer to float64 → number format float64 (for optional numeric stats)
		elem := typ.Elem()
		if basic, ok := elem.(*types.Basic); ok && basic.Kind() == types.Float64 {
			return map[string]interface{}{
				"type":   "number",
				"format": "float64",
			}
		}
		if named, ok := elem.(*types.Named); ok {
			// Pointer to named type — build definition and return $ref.
			b.buildTypeDef(named)
			name := typeDefName(named)
			return map[string]interface{}{
				"$ref": fmt.Sprintf("#/definitions/%s", name),
			}
		}
		return b.schemaForType(elem)

	case *types.Slice:
		if isJSONRawMessageType(typ) {
			return map[string]interface{}{}
		}
		elem := typ.Elem()
		return map[string]interface{}{
			"type":  "array",
			"items": b.schemaForType(elem),
		}

	case *types.Map:
		valSchema := b.schemaForType(typ.Elem())
		return map[string]interface{}{
			"type":                 "object",
			"additionalProperties": valSchema,
		}

	case *types.Struct:
		if isTimeType(t) {
			return map[string]interface{}{
				"type":   "string",
				"format": "date-time",
			}
		}
		return b.inlineStructSchema(typ)

	default:
		return map[string]interface{}{"type": "string"}
	}
}

// inlineStructSchema builds inline JSON Schema for an anonymous or embedded struct.
func (b *ASTSchemaBuilder) inlineStructSchema(st *types.Struct) interface{} {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
	var required []string

	for i := 0; i < st.NumFields(); i++ {
		field := st.Field(i)
		if !field.Exported() {
			continue
		}

		// Parse json tag from the struct's underlying AST source.
		jsonName, opts := fieldJSONInfo(st, i)
		if jsonName == "-" {
			continue
		}

		ft := field.Type()
		isPtr := isPointer(ft)
		if isPtr {
			ft = derefType(ft)
		}

		fieldSchema := b.schemaForType(ft)
		schema["properties"].(map[string]interface{})[jsonName] = fieldSchema

		if !opts["omitempty"] && !isPtr && !isJSONRawMessageType(ft) {
			required = append(required, jsonName)
		}
	}

	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}

	return schema
}

// buildTypeDef generates a full type definition for a named struct type.
func (b *ASTSchemaBuilder) buildTypeDef(named *types.Named) {
	name := typeDefName(named)
	if _, ok := b.builtDefs[name]; ok {
		return
	}

	// Mark as building to prevent cycles.
	b.builtDefs[name] = nil

	under := named.Underlying()
	switch u := under.(type) {
	case *types.Struct:
		b.builtDefs[name] = b.inlineStructSchema(u)
	case *types.Basic:
		b.builtDefs[name] = basicSchema(u.Kind())
	default:
		b.builtDefs[name] = b.schemaForType(under)
	}
}

// collectType registers a type and its dependencies for definition building.
func (b *ASTSchemaBuilder) collectType(ref *TypeRef) {
	if ref == nil {
		return
	}
	named, ok := ref.Type.(*types.Named)
	if !ok {
		return
	}
	name := typeDefName(named)
	if b.typeDefs[name] {
		return
	}
	b.typeDefs[name] = true

	// Traverse struct fields, array items, map values to collect dependencies.
	under := named.Underlying()
	switch u := under.(type) {
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			ft := u.Field(i).Type()
			b.collectTypeFromType(ft)
		}
	case *types.Slice:
		b.collectTypeFromType(u.Elem())
	case *types.Map:
		b.collectTypeFromType(u.Elem())
	}
}

// collectTypeFromType extracts TypeRefs from a types.Type and recurses.
func (b *ASTSchemaBuilder) collectTypeFromType(t types.Type) {
	t = derefType(t)

	switch typ := t.(type) {
	case *types.Named:
		if isTimeType(typ) || isJSONRawMessageType(typ) || isBuiltinType(typ) {
			return
		}
		b.collectType(&TypeRef{
			PkgPath: typ.Obj().Pkg().Path(),
			Name:    typ.Obj().Name(),
			Type:    typ,
		})
	case *types.Slice:
		b.collectTypeFromType(typ.Elem())
	case *types.Map:
		b.collectTypeFromType(typ.Elem())
	case *types.Struct:
		for i := 0; i < typ.NumFields(); i++ {
			b.collectTypeFromType(typ.Field(i).Type())
		}
	case *types.Pointer:
		b.collectTypeFromType(typ.Elem())
	}
}

// ============================================================================
// Helpers
// ============================================================================

// typeDefName generates a definition name from a named type.
// It strips the module path for concise names, e.g.:
// "github.com/joyboydata/kit/specification.BehavioralSegmentation"
// becomes "specification.BehavioralSegmentation"
func typeDefName(named *types.Named) string {
	pkg := named.Obj().Pkg().Path()
	parts := strings.Split(pkg, "/")
	// Use the last meaningful component as prefix.
	short := ""
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" && !strings.HasPrefix(parts[i], ".") {
			short = parts[i]
			break
		}
	}
	return short + "." + named.Obj().Name()
}

// basicSchema returns a JSON Schema for a basic Go type.
func basicSchema(kind types.BasicKind) interface{} {
	switch kind {
	case types.String:
		return map[string]interface{}{"type": "string"}
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64:
		return map[string]interface{}{"type": "integer"}
	case types.Float32, types.Float64:
		return map[string]interface{}{"type": "number", "format": "float64"}
	case types.Bool:
		return map[string]interface{}{"type": "boolean"}
	default:
		return map[string]interface{}{"type": "string"}
	}
}

// derefType unwraps pointer types.
func derefType(t types.Type) types.Type {
	for {
		ptr, ok := t.(*types.Pointer)
		if !ok {
			return t
		}
		t = ptr.Elem()
	}
}

// isPointer returns true if a type is a pointer.
func isPointer(t types.Type) bool {
	_, ok := t.(*types.Pointer)
	return ok
}

// isTimeType checks if a type is time.Time.
func isTimeType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Pkg().Path() == "time" && named.Obj().Name() == "Time"
}

// isJSONRawMessageType checks if a type is json.RawMessage or its underlying.
func isJSONRawMessageType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Pkg().Path() == "encoding/json" && named.Obj().Name() == "RawMessage"
}

// isBuiltinType checks if a type comes from a standard library package
// that should not be expanded as a named definition.
func isBuiltinType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	pkg := named.Obj().Pkg().Path()
	return pkg == "time" || pkg == "encoding/json"
}

// fieldJSONInfo extracts the JSON field name and options from a struct field's tag.
// It uses AST to parse the struct tag rather than go/types (which doesn't expose tags).
func fieldJSONInfo(st *types.Struct, fieldIndex int) (name string, opts map[string]bool) {
	opts = map[string]bool{}

	// go/types doesn't expose struct tags directly via the field API in older versions.
	// However, the Struct type does have a Tag(i int) method since Go 1.x.
	// Let's use it.
	tagStr := st.Tag(fieldIndex)
	if tagStr == "" {
		field := st.Field(fieldIndex)
		return field.Name(), opts
	}

	// Parse the json tag: `json:"name,omitempty"`
	tagStr, ok := reflectTagLookup(tagStr, "json")
	if !ok || tagStr == "" || tagStr == "-" {
		if tagStr == "-" {
			return "-", opts
		}
		field := st.Field(fieldIndex)
		return field.Name(), opts
	}

	parts := strings.Split(tagStr, ",")
	name = parts[0]
	if name == "" {
		field := st.Field(fieldIndex)
		return field.Name(), opts
	}
	for _, p := range parts[1:] {
		opts[p] = true
	}
	return name, opts
}

// reflectTagLookup simulates reflect.StructTag.Lookup for a given key.
// This is needed because go/types.Struct.Tag() returns the raw tag string.
func reflectTagLookup(tag, key string) (string, bool) {
	for tag != "" {
		// Skip leading space.
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}

		// Scan to colon. A key is an identifier.
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			break
		}
		keyValue := tag[:i]
		tag = tag[i+1:]

		// Scan quoted string.
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		quotedValue := tag[1:i]
		tag = tag[i+1:]

		if keyValue == key {
			return quotedValue, true
		}
	}
	return "", false
}
