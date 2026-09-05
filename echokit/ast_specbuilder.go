package echokit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ============================================================================
// ASTSpecBuilder generates Swagger 2.0 specs from AST-discovered groups.
// It mirrors SpecBuilder but works with ASTGroup and ASTSchemaBuilder
// instead of reflect.Type-based Group.
// ============================================================================

// ASTSpecBuilder builds Swagger 2.0 JSON specs from AST-discovered route groups.
type ASTSpecBuilder struct {
	cfg    SpecConfig
	groups []ASTGroup
	schema *ASTSchemaBuilder
}

// NewASTSpecBuilder creates a new spec builder from AST-discovered groups.
func NewASTSpecBuilder(cfg SpecConfig) *ASTSpecBuilder {
	return &ASTSpecBuilder{
		cfg:    cfg,
		schema: NewASTSchemaBuilder(SchemaConfig{}),
	}
}

// AddGroups registers AST-discovered groups for spec generation.
func (b *ASTSpecBuilder) AddGroups(groups ...ASTGroup) {
	b.groups = append(b.groups, groups...)
}

// Build generates the Swagger 2.0 JSON spec and returns the raw bytes.
func (b *ASTSpecBuilder) Build() ([]byte, error) {
	b.collectTypes()
	spec := b.buildSpec()
	return json.MarshalIndent(spec, "", "    ")
}

// WriteTo builds and writes the spec to a file.
func (b *ASTSpecBuilder) WriteTo(path string) error {
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

// collectTypes registers all referenced types from route metadata.
func (b *ASTSpecBuilder) collectTypes() {
	for _, g := range b.groups {
		for _, r := range g.Routes {
			if r.OpenAPI == nil {
				continue
			}
			if r.OpenAPI.BodyType != nil {
				b.schema.collectType(r.OpenAPI.BodyType)
			}
			for _, resp := range r.OpenAPI.Responses {
				if resp.ModelType != nil {
					b.schema.collectType(resp.ModelType)
				}
			}
		}
	}
}

func (b *ASTSpecBuilder) buildSpec() map[string]interface{} {
	spec := map[string]interface{}{
		"swagger":  "2.0",
		"info":     b.buildInfo(),
		"basePath": b.cfg.BasePath,
		"paths":    b.buildPaths(),
	}

	if defs := b.schema.Definitions(); len(defs) > 0 {
		spec["definitions"] = defs
	}

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

func (b *ASTSpecBuilder) buildInfo() map[string]interface{} {
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

func (b *ASTSpecBuilder) buildPaths() map[string]interface{} {
	paths := map[string]interface{}{}
	specBase := b.cfg.BasePath

	for _, g := range b.groups {
		for _, r := range g.Routes {
			if r.OpenAPI == nil {
				continue
			}
			fullPath := normalizeEchoPath(g.BasePath + r.Pattern)
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

func (b *ASTSpecBuilder) buildOperation(r ASTRoute, fullPath string) map[string]interface{} {
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

	op["produces"] = []string{"application/json"}

	if meta.BodyType != nil && (r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH") {
		op["consumes"] = []string{"application/json"}
	}

	params := b.buildParameters(meta, fullPath)
	if len(params) > 0 {
		op["parameters"] = params
	}

	op["responses"] = b.buildResponses(meta)

	return op
}

func (b *ASTSpecBuilder) buildParameters(meta ASTOpenAPI, fullPath string) []interface{} {
	var params []interface{}

	pathParams := extractPathParams(fullPath)

	for _, name := range pathParams {
		params = append(params, map[string]interface{}{
			"in":          "path",
			"name":        name,
			"type":        "string",
			"required":    true,
			"description": name,
		})
	}

	if meta.BodyType != nil {
		bodySchema := b.schema.Schema(meta.BodyType)
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

func (b *ASTSpecBuilder) buildResponses(meta ASTOpenAPI) map[string]interface{} {
	responses := map[string]interface{}{}

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
		if resp.ModelType != nil {
			r["schema"] = b.schema.Schema(resp.ModelType)
		} else {
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