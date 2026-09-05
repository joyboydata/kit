// Package echokit provides an opinionated layer over Echo for route
// grouping, registration, and OpenAPI spec generation.
//
// Spec generation uses reflection to derive JSON Schema from Go types,
// eliminating the need for external swag comment annotations. The
// SpecBuilder collects groups registered with OpenAPI metadata and
// produces a Swagger 2.0 document.

package echokit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// ============================================================================
// Config — Top-Level API Metadata
// ============================================================================

// SpecConfig holds the top-level OpenAPI metadata for the specification.
type SpecConfig struct {
	Title       string
	Version     string
	Description string
	BasePath    string
	LogoURL     string // URL path for x-logo
	TagGroups   []TagGroup
}

// TagGroup groups tags under a display heading in the API docs UI.
type TagGroup struct {
	Name string
	Tags []string
}

// ============================================================================
// Spec Builder
// ============================================================================

// SpecBuilder collects route groups and produces a Swagger 2.0 spec.
type SpecBuilder struct {
	cfg    SpecConfig
	groups []Group
	defs   map[reflect.Type]*schemaDef
}

type schemaDef struct {
	name   string
	schema interface{}
}

// NewSpecBuilder creates a new spec builder with the given config.
func NewSpecBuilder(cfg SpecConfig) *SpecBuilder {
	return &SpecBuilder{
		cfg:  cfg,
		defs: map[reflect.Type]*schemaDef{},
	}
}

// AddGroups registers route groups for spec generation.
func (b *SpecBuilder) AddGroups(groups ...Group) {
	b.groups = append(b.groups, groups...)
}

// Build generates the Swagger 2.0 JSON spec and returns the raw bytes.
func (b *SpecBuilder) Build() ([]byte, error) {
	b.collectTypes()
	spec := b.buildSpec()
	return json.MarshalIndent(spec, "", "    ")
}

// WriteTo writes the generated spec to a file path.
func (b *SpecBuilder) WriteTo(path string) error {
	data, err := b.Build()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("echokit: create dir %s: %w", dir, err)
		}
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("echokit: write %s: %w", path, err)
	}
	return nil
}

// ============================================================================
// Type Collection — First pass: find all types referenced by routes
// ============================================================================

func (b *SpecBuilder) collectTypes() {
	for _, g := range b.groups {
		for _, r := range g.Routes {
			if r.OpenAPI == nil {
				continue
			}
			meta := *r.OpenAPI
			if meta.Body != nil {
				b.registerType(meta.Body)
			}
			for _, resp := range meta.Responses {
				if resp.Model != nil {
					b.registerType(resp.Model)
				}
			}
		}
	}
}

// registerType ensures a type and all its dependencies are recorded for
// definition generation. It traverses struct fields, array items, map
// values, and pointer targets recursively with cycle detection.
// Only named types (structs defined with `type X struct{...}`) are
// registered as top-level definitions; unnamed composite types
// (slices, maps, anonymous structs) are inlined where used — but they
// are still traversed so their element types get registered.
func (b *SpecBuilder) registerType(t reflect.Type) {
	if b.defs[t] != nil {
		return
	}

	// For named (non-special) types, create a definition placeholder.
	// This serves as cycle detection — if we see this type again
	// during recursion, b.defs[t] already exists and we return.
	hasName := t.Name() != ""
	if hasName && !isTimeTime(t) && !isJSONRawMessage(t) {
		b.defs[t] = &schemaDef{name: typeName(t)}
	} else if hasName {
		return // time.Time and json.RawMessage are never definitions
	}

	b.traverseType(t)
}

// traverseType explores the fields/elements of a type and registers
// any referenced types as definitions. It does not create a definition
// for the type itself — that is handled by registerType.
func (b *SpecBuilder) traverseType(t reflect.Type) {
	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			ft := f.Type
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if isPrimitive(ft) || isTimeTime(ft) || isJSONRawMessage(ft) {
				continue
			}
			b.registerType(ft)
		}

	case reflect.Slice:
		if isJSONRawMessage(t) {
			return
		}
		elem := t.Elem()
		if elem.Kind() == reflect.Ptr {
			elem = elem.Elem()
		}
		if !isPrimitive(elem) && !isTimeTime(elem) && !isJSONRawMessage(elem) {
			b.registerType(elem)
		}

	case reflect.Map:
		valType := t.Elem()
		if valType.Kind() == reflect.Ptr {
			valType = valType.Elem()
		}
		if !isPrimitive(valType) && !isTimeTime(valType) && !isJSONRawMessage(valType) {
			b.registerType(valType)
		}

	case reflect.Ptr:
		elem := t.Elem()
		if !isPrimitive(elem) && !isTimeTime(elem) && !isJSONRawMessage(elem) {
			b.registerType(elem)
		}
	}
}

// ============================================================================
// Spec Construction — Second pass: build the full Swagger document
// ============================================================================

func (b *SpecBuilder) buildSpec() map[string]interface{} {
	spec := map[string]interface{}{
		"swagger":  "2.0",
		"info":     b.buildInfo(),
		"basePath": b.cfg.BasePath,
		"paths":    b.buildPaths(),
	}

	if defs := b.buildDefinitions(); len(defs) > 0 {
		spec["definitions"] = defs
	}

	// x-tagGroups
	if len(b.cfg.TagGroups) > 0 {
		tgs := make([]map[string]interface{}, len(b.cfg.TagGroups))
		for i, tg := range b.cfg.TagGroups {
			tgs[i] = map[string]interface{}{
				"name": tg.Name,
				"tags": tg.Tags,
			}
		}
		spec["x-tagGroups"] = tgs
	}

	return spec
}

func (b *SpecBuilder) buildInfo() map[string]interface{} {
	info := map[string]interface{}{
		"title":       b.cfg.Title,
		"description": b.cfg.Description,
		"version":     b.cfg.Version,
	}
	if b.cfg.LogoURL != "" {
		info["x-logo"] = map[string]interface{}{
			"url": b.cfg.LogoURL,
		}
	}
	return info
}

func (b *SpecBuilder) buildPaths() map[string]interface{} {
	paths := map[string]interface{}{}
	specBase := b.cfg.BasePath

	for _, g := range b.groups {
		for _, r := range g.Routes {
			if r.OpenAPI == nil {
				continue
			}
			// Combine group base + pattern for the full path
			fullPath := normalizeEchoPath(g.BasePath + r.Pattern)

			// The path key is relative to spec.BasePath (Swagger 2.0 convention)
			pathKey := strings.TrimPrefix(fullPath, specBase)
			if pathKey == "" {
				pathKey = "/"
			}

			method := strings.ToLower(r.Method)

			pathItem, ok := paths[pathKey]
			if !ok {
				pathItem = map[string]interface{}{}
				paths[pathKey] = pathItem
			}

			pm := pathItem.(map[string]interface{})
			pm[method] = b.buildOperation(r, fullPath)
		}
	}

	return paths
}

func (b *SpecBuilder) buildOperation(r Route, fullPath string) map[string]interface{} {
	meta := *r.OpenAPI

	op := map[string]interface{}{}

	if meta.Summary != "" {
		op["summary"] = meta.Summary
	}
	if meta.Description != "" {
		op["description"] = meta.Description
	}
	if len(meta.Tags) > 0 {
		op["tags"] = meta.Tags
	}

	// Produces — assume JSON for all API endpoints
	op["produces"] = []string{"application/json"}

	// Consumes — only for methods with a body
	if meta.Body != nil && (r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH") {
		op["consumes"] = []string{"application/json"}
	}

	// Parameters
	params := b.buildParameters(meta, fullPath)
	if len(params) > 0 {
		op["parameters"] = params
	}

	// Responses
	op["responses"] = b.buildResponses(meta)

	return op
}

func (b *SpecBuilder) buildParameters(meta OpenAPI, fullPath string) []interface{} {
	var params []interface{}

	// Path parameters derived from the route pattern
	pathParams := extractPathParams(fullPath)
	definedParams := map[string]bool{}
	for _, p := range meta.Parameters {
		definedParams[p.Name] = true
	}

	// Add path params that aren't explicitly defined
	for _, name := range pathParams {
		if !definedParams[name] {
			params = append(params, map[string]interface{}{
				"in":          "path",
				"name":        name,
				"type":        "string",
				"required":    true,
				"description": name,
			})
		}
	}

	// Add explicitly defined parameters
	for _, p := range meta.Parameters {
		params = append(params, map[string]interface{}{
			"in":          p.In,
			"name":        p.Name,
			"type":        "string",
			"required":    p.Required,
			"description": p.Description,
		})
	}

	// Body parameter
	if meta.Body != nil {
		bodySchema := b.typeSchema(meta.Body)
		bodyParam := map[string]interface{}{
			"in":       "body",
			"name":     "body",
			"required": true,
			"schema":   bodySchema,
		}
		params = append(params, bodyParam)
	}

	return params
}

func (b *SpecBuilder) buildResponses(meta OpenAPI) map[string]interface{} {
	responses := map[string]interface{}{}

	// Sort response codes for deterministic output
	codes := make([]int, 0, len(meta.Responses))
	for code := range meta.Responses {
		codes = append(codes, code)
	}
	sort.Ints(codes)

	for _, code := range codes {
		resp := meta.Responses[code]
		r := map[string]interface{}{
			"description": resp.Description,
		}
		if resp.Model != nil {
			r["schema"] = b.typeSchema(resp.Model)
		} else {
			// Default: map[string]string for error responses
			r["schema"] = map[string]interface{}{
				"type": "object",
				"additionalProperties": map[string]interface{}{
					"type": "string",
				},
			}
		}
		responses[fmt.Sprintf("%d", code)] = r
	}

	return responses
}

// ============================================================================
// JSON Schema Generation from reflect.Type
// ============================================================================

func (b *SpecBuilder) typeSchema(t reflect.Type) interface{} {
	// Resolve pointers
	if t.Kind() == reflect.Ptr {
		inner := t.Elem()
		// Float64 pointer — used for optional numeric statistics
		if inner.Kind() == reflect.Float64 {
			return map[string]interface{}{
				"type":   "number",
				"format": "float64",
			}
		}
		if isPrimitive(inner) {
			return primitiveSchema(inner)
		}
		// Pointer to struct → $ref
		return b.refOrInline(inner)
	}

	// Empty interface (interface{})
	if t.Kind() == reflect.Interface {
		return map[string]interface{}{}
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]interface{}{"type": "string"}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]interface{}{"type": "integer"}

	case reflect.Float32, reflect.Float64:
		return map[string]interface{}{"type": "number", "format": "float64"}

	case reflect.Bool:
		return map[string]interface{}{"type": "boolean"}

	case reflect.Slice:
		if isJSONRawMessage(t) {
			return map[string]interface{}{}
		}
		elem := t.Elem()
		items := b.typeSchema(elem)
		return map[string]interface{}{
			"type":  "array",
			"items": items,
		}

	case reflect.Map:
		valType := t.Elem()
		valSchema := b.typeSchema(valType)
		return map[string]interface{}{
			"type":                 "object",
			"additionalProperties": valSchema,
		}

	case reflect.Struct:
		if isTimeTime(t) {
			return map[string]interface{}{
				"type":   "string",
				"format": "date-time",
			}
		}
		return b.refOrInline(t)

	default:
		return map[string]interface{}{"type": "string"}
	}
}

// refOrInline returns a $ref if the type has a definition, or inlines the schema.
func (b *SpecBuilder) refOrInline(t reflect.Type) interface{} {
	if def, ok := b.defs[t]; ok {
		return map[string]interface{}{
			"$ref": fmt.Sprintf("#/definitions/%s", def.name),
		}
	}
	return b.inlineSchema(t)
}

// inlineSchema builds an inline JSON Schema for a struct type.
func (b *SpecBuilder) inlineSchema(t reflect.Type) interface{} {
	if t.Kind() != reflect.Struct {
		return b.typeSchema(t)
	}

	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}

	var required []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}

		// Handle embedded/anonymous fields — flatten into parent
		if f.Anonymous {
			if embedMap, ok := b.inlineSchema(f.Type).(map[string]interface{}); ok {
				if props, ok := embedMap["properties"].(map[string]interface{}); ok {
					for k, v := range props {
						schema["properties"].(map[string]interface{})[k] = v
					}
				}
				if embedReq, ok := embedMap["required"].([]string); ok {
					required = append(required, embedReq...)
				}
			}
			continue
		}

		jsonName, opts := jsonFieldName(f)
		ft := f.Type
		isPtr := ft.Kind() == reflect.Ptr
		if isPtr {
			ft = ft.Elem()
		}

		fieldSchema := b.typeSchema(f.Type)
		schema["properties"].(map[string]interface{})[jsonName] = fieldSchema

		if !opts["omitempty"] && !isPtr && !isJSONRawMessage(ft) {
			required = append(required, jsonName)
		}
	}

	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}

	return schema
}

// ============================================================================
// Definitions
// ============================================================================

func (b *SpecBuilder) buildDefinitions() map[string]interface{} {
	defs := map[string]interface{}{}

	names := make([]string, 0, len(b.defs))
	typeByName := map[string]reflect.Type{}
	for t, def := range b.defs {
		typeByName[def.name] = t
		names = append(names, def.name)
	}
	sort.Strings(names)

	for _, name := range names {
		t := typeByName[name]
		defs[name] = b.buildDefinition(t)
	}

	return defs
}

func (b *SpecBuilder) buildDefinition(t reflect.Type) interface{} {
	if t.Kind() == reflect.Struct && !isTimeTime(t) {
		return b.inlineSchema(t)
	}
	return b.typeSchema(t)
}

// ============================================================================
// Helpers
// ============================================================================

func typeName(t reflect.Type) string {
	pkg := t.PkgPath()
	short := strings.TrimPrefix(pkg, "github.com/joyboydata/slicer/")
	short = strings.TrimPrefix(short, "x/")
	short = strings.TrimPrefix(short, "internal/")
	return short + "." + t.Name()
}

func jsonFieldName(f reflect.StructField) (name string, opts map[string]bool) {
	opts = map[string]bool{}
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name, opts
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "" {
		return f.Name, opts
	}
	name = parts[0]
	for _, p := range parts[1:] {
		opts[p] = true
	}
	return name, opts
}

func isPrimitive(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Bool:
		return true
	}
	return false
}

func primitiveSchema(t reflect.Type) interface{} {
	switch t.Kind() {
	case reflect.String:
		return map[string]interface{}{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]interface{}{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]interface{}{"type": "number", "format": "float64"}
	case reflect.Bool:
		return map[string]interface{}{"type": "boolean"}
	default:
		return map[string]interface{}{"type": "string"}
	}
}

func isTimeTime(t reflect.Type) bool {
	return t.PkgPath() == "time" && t.Name() == "Time"
}

func isJSONRawMessage(t reflect.Type) bool {
	return t.PkgPath() == "encoding/json" && t.Name() == "RawMessage"
}

// normalizeEchoPath converts Echo's :param syntax to OpenAPI's {param} syntax.
func normalizeEchoPath(path string) string {
	var result strings.Builder
	i := 0
	for i < len(path) {
		if path[i] == ':' {
			if result.Len() > 0 {
				b := result.String()[result.Len()-1]
				if b != '/' {
					result.WriteByte('/')
				}
			}
			result.WriteByte('{')
			i++
			start := i
			for i < len(path) && path[i] != '/' && path[i] != ':' {
				i++
			}
			result.WriteString(path[start:i])
			result.WriteByte('}')
		} else {
			result.WriteByte(path[i])
			i++
		}
	}
	return result.String()
}

// extractPathParams extracts parameter names from an OpenAPI-style path.
func extractPathParams(path string) []string {
	var params []string
	for i := 0; i < len(path); i++ {
		if path[i] == '{' {
			j := i + 1
			for j < len(path) && path[j] != '}' {
				j++
			}
			if j > i+1 && j < len(path) {
				params = append(params, path[i+1:j])
			}
			i = j
		}
	}
	return params
}
