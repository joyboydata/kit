// Command piextgen generates PI extension .ts files from echokit route definitions.
// It discovers route groups automatically via AST without importing handler packages.
//
// For each route with a Tool annotation, it produces a structured TypeScript
// tool definition with recursive Type.Object(...) body params derived from Go
// types via AST + go/types analysis.
//
// The generator works in phases:
// 1. Scan all packages via AST to discover groups and routes with Tool metadata
// 2. Resolve request body types and build a type dependency graph
// 3. Write shared types file (slicer-types.ts)
// 4. Write tool files grouped by API tag
//
// Usage:
//
//	go run ./x/echokit/cmd/piextgen/
//	go run ./x/echokit/cmd/piextgen/ --out agents/extensions
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/joyboydata/kit/echokit"
)

// toolRoute represents a route that has Tool metadata, cached with its
// group and resolved tag for processing.
type toolRoute struct {
	group echokit.ASTGroup
	route echokit.ASTRoute
	tag   string // Resolved from OpenAPI.Tags[0] or "default"
}

// main generates TypeScript PI extension tools from API route definitions.
// It discovers routes via AST, extracts shared types, and writes .ts files.
func main() {
	outDir := "agents/extensions"
	echokitPkg := ""
	defaultApiURL := "http://localhost:8088"
	if len(os.Args) > 1 {
		for i := 1; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--out":
				if i+1 < len(os.Args) {
					outDir = os.Args[i+1]
					i++
				}
			case "--echokit-pkg":
				if i+1 < len(os.Args) {
					echokitPkg = os.Args[i+1]
					i++
				}
			case "--default-api-url":
				if i+1 < len(os.Args) {
					defaultApiURL = os.Args[i+1]
					i++
				}
			}
		}
	}

	appName := echokit.ModuleAppName("")

	// Discover groups via AST — no hardcoded imports.
	groups, err := echokit.DiscoverGroups(echokit.DiscoverConfig{
		EChokitPkgPath: echokitPkg,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: discovering groups: %v\n", err)
		os.Exit(1)
	}

	var tools []toolRoute
	tsGen := echokit.NewTSTypeGenerator()

	for _, g := range groups {
		for _, r := range g.Routes {
			if r.Tool == nil || r.Tool.Disabled {
				continue
			}
			tag := resolveTag(r)
			tools = append(tools, toolRoute{group: g, route: r, tag: tag})

			if r.OpenAPI != nil && r.OpenAPI.BodyType != nil {
				tsGen.CollectType(r.OpenAPI.BodyType)
			}
		}
	}

	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error: mkdir %s: %v\n", outDir, err)
		os.Exit(1)
	}

	// Write shared types file.
	typesPath := filepath.Join(outDir, "slicer-types.ts")
	if err := writeSharedTypes(typesPath, tsGen); err != nil {
		fmt.Fprintf(os.Stderr, "error: write %s: %v\n", typesPath, err)
		os.Exit(1)
	}
	fmt.Printf("  -> %s  (%d shared types)\n", typesPath, len(tsGen.NamedTypes()))

	// Write per-tag tool files.
	tagGroups := groupByTag(tools)
	tagSlugs := make([]string, 0, len(tagGroups))
	for _, tg := range tagGroups {
		slug := tagSlug(tg.tag)
		tagSlugs = append(tagSlugs, slug)
		filename := filepath.Join(outDir, fmt.Sprintf("slicer-%s.ts", slug))
		if err := writeToolFile(filename, tg.tools, tsGen); err != nil {
			fmt.Fprintf(os.Stderr, "error: write %s: %v\n", filename, err)
			os.Exit(1)
		}
		fmt.Printf("  -> %s  (%d tools)\n", filename, len(tg.tools))
	}

	// Write client.ts with shared apiFetch.
	clientPath := filepath.Join(outDir, "client.ts")
	if err := writeClientFile(clientPath, appName, defaultApiURL); err != nil {
		fmt.Fprintf(os.Stderr, "error: write %s: %v\n", clientPath, err)
		os.Exit(1)
	}
	fmt.Printf("  -> %s\n", clientPath)

	// Write index.ts that registers all tool modules.
	if len(tagSlugs) > 0 {
		indexPath := filepath.Join(outDir, "index.ts")
		if err := writeIndex(indexPath, tagSlugs); err != nil {
			fmt.Fprintf(os.Stderr, "error: write %s: %v\n", indexPath, err)
			os.Exit(1)
		}
		fmt.Printf("  -> %s  (%d modules)\n", indexPath, len(tagSlugs))
	}
}

// resolveTag extracts the first tag from a route's OpenAPI metadata.
func resolveTag(r echokit.ASTRoute) string {
	if r.OpenAPI != nil && len(r.OpenAPI.Tags) > 0 {
		return r.OpenAPI.Tags[0]
	}
	return "default"
}

// tagSlug converts a tag string to a filename-safe slug.
func tagSlug(tag string) string {
	return strings.ReplaceAll(tag, "-", "_")
}

// tagGroup represents a collection of tools grouped by their API tag.
type tagGroup struct {
	tag   string
	tools []toolRoute
}

// groupByTag organizes tools by their tag.
func groupByTag(tools []toolRoute) []tagGroup {
	tagOrder := make([]string, 0, len(tools))
	tagMap := map[string][]toolRoute{}

	for _, t := range tools {
		if _, ok := tagMap[t.tag]; !ok {
			tagOrder = append(tagOrder, t.tag)
		}
		tagMap[t.tag] = append(tagMap[t.tag], t)
	}

	for tag := range tagMap {
		sort.Slice(tagMap[tag], func(i, j int) bool {
			return toolName(tagMap[tag][i].route) < toolName(tagMap[tag][j].route)
		})
	}

	groups := make([]tagGroup, 0, len(tagOrder))
	for _, tag := range tagOrder {
		groups = append(groups, tagGroup{tag: tag, tools: tagMap[tag]})
	}
	return groups
}

// toolName extracts the tool name from a route.
func toolName(r echokit.ASTRoute) string {
	if r.Tool != nil && r.Tool.Name != "" {
		return r.Tool.Name
	}
	if r.OpenAPI != nil && r.OpenAPI.Summary != "" {
		return generateToolName(r.OpenAPI.Summary)
	}
	return "slicer_unnamed"
}

// generateToolName converts an OpenAPI summary into a tool identifier.
func generateToolName(summary string) string {
	slug := strings.ToLower(summary)
	slug = strings.NewReplacer(
		" ", "_",
		"-", "_",
		"/", "_",
		"(", "",
		")", "",
		",", "",
		".", "",
	).Replace(slug)
	for strings.Contains(slug, "__") {
		slug = strings.ReplaceAll(slug, "__", "_")
	}
	slug = strings.Trim(slug, "_")
	return "slicer_" + slug
}

// ============================================================================
// Shared types file generation
// ============================================================================

// writeSharedTypes generates slicer-types.ts with all shared type definitions.
func writeSharedTypes(path string, gen *echokit.TSTypeGenerator) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()

	writeLine := func(s string) {
		fmt.Fprintln(f, s)
	}

	writeLine("// Code generated by piextgen. DO NOT EDIT.")
	writeLine(fmt.Sprintf("// Generated: %s", time.Now().Format(time.RFC3339)))
	writeLine("// Regenerate with: go generate ./...")
	writeLine("")
	writeLine(`import { Type } from "@mariozechner/pi-ai";`)
	writeLine("")

	namedTypes := gen.NamedTypes()
	typeOrder := gen.TypeOrder()
	for _, name := range typeOrder {
		if expr, ok := namedTypes[name]; ok {
			writeLine(fmt.Sprintf("export const %s = %s;", name, expr))
			writeLine("")
		}
	}

	return nil
}

// ============================================================================
// Tool file generation (Go templates)
// ============================================================================

// tplToolFile is the Go template for a per-tag tool file.
// __BT__ is a placeholder for backtick characters, replaced before parsing.
const tplToolFile = `// Code generated by piextgen from echokit route definitions. DO NOT EDIT.
// Generated: {{.Timestamp}}
// Regenerate with: go generate ./...

import { Type } from "@mariozechner/pi-ai";
import { defineTool, type ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { apiFetch } from "./client";
{{if .TypeImports}}
import { {{join .TypeImports ", "}} } from "./slicer-types";
{{end}}

{{range .Tools}}
const {{.Name}} = defineTool({
	name: __BT__{{.Name}}__BT__,{{if .Label}}
	label: __BT__{{.Label}}__BT__,{{end}}
	description: __BT__{{safeDesc .Description}}__BT__,
	parameters: Type.Object({
		{{range .PathParams}}{{.Name}}: Type.String({ description: __BT__Path parameter: {{.OrigName}}__BT__ }),
		{{end}}{{if .HasBody}}body: {{.BodyExpr}},
		{{end}}
	}),
	async execute(_toolCallId, params, _signal, _onUpdate, _ctx) {
		return apiFetch(__BT__{{.URLPath}}__BT__, __BT__{{.Method}}__BT__, {{.VarBody}}, _signal);
	},
});
{{end}}

export default function (pi: ExtensionAPI) {
{{range .Tools}}	pi.registerTool({{.Name}});
{{end}}
}`

const tplClientFile = `// Code generated by piextgen. DO NOT EDIT.
// Shared API client — edit BASE_URL and env vars for your setup.

const BASE_URL = __BT__{{.DefaultApiURL}}__BT__;

function authHeader(): string {
	const u = process.env.{{.EnvPrefix}}_API_USERNAME;
	const p = process.env.{{.EnvPrefix}}_API_PASSWORD;
	if (u && p) return "Basic " + btoa(u + ":" + p);
	return "";
}

export async function apiFetch(
	path: string,
	method: string,
	body?: string,
	signal?: AbortSignal,
): Promise<{ content: {type: string; text: string}[]; details: Record<string, never> }> {
	const headers: Record<string, string> = {
		"Accept": "application/json",
	};
	const auth = authHeader();
	if (auth) headers["Authorization"] = auth;
	if (body) headers["Content-Type"] = "application/json";

	const url = (process.env.{{.EnvPrefix}}_API_URL || BASE_URL) + path;
	const res = await fetch(url, {
		method,
		headers,
		body,
		signal,
	});
	if (!res.ok) {
		const text = await res.text().catch(() => "");
		throw new Error("{{.AppName}} API error (" + res.status + "): " + text);
	}
	const data = res.status === 204 ? null : await res.json();
	return {
		content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
		details: {},
	};
}
`

// clientFileData is the data for the client.ts template.
type clientFileData struct {
	AppName       string
	EnvPrefix     string
	DefaultApiURL string
}

var clientFileTmpl = template.Must(template.New("client").Parse(strings.ReplaceAll(tplClientFile, "__BT__", "`")))

// writeClientFile generates client.ts with the shared API client.
func writeClientFile(path, appName, defaultApiURL string) error {
	data := clientFileData{
		AppName:       appName,
		EnvPrefix:     strings.ToUpper(strings.ReplaceAll(appName, " ", "_")),
		DefaultApiURL: defaultApiURL,
	}
	var buf bytes.Buffer
	if err := clientFileTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("client template: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

// toolFileData is the data structure passed to the tool file template.
type toolFileData struct {
	Timestamp   string
	TypeImports []string
	Tools       []toolDefData
}

// toolDefData is the data for a single tool definition within the template.
type toolDefData struct {
	Name        string
	Label       string
	Description string
	PathParams  []paramDefData
	HasBody     bool
	BodyExpr    string // Inline TS type expression (Type.Object({...}) or shared type name)
	URLPath     string // Template literal ready, e.g. "/api/v1/segments/${params.buildSQL}"
	Method      string // "GET", "POST", etc.
	VarBody     string // "undefined" or "JSON.stringify(params.body)"
}

// paramDefData is the data for a single path parameter.
type paramDefData struct {
	Name     string // camelCase name, e.g. "buildSQL"
	OrigName string // original name, e.g. "BuildSQL"
}

// toolFileTmpl is the parsed template for a tool file.
var toolFileTmpl = template.Must(
	template.New("toolfile").
		Funcs(template.FuncMap{
			"join":     strings.Join,
			"safeDesc": safeDescForTemplate,
		}).
		Parse(strings.ReplaceAll(tplToolFile, "__BT__", "`")),
)

// safeDescForTemplate replaces backticks in descriptions with a safe alternative.
func safeDescForTemplate(s string) string {
	return strings.ReplaceAll(s, "`", "'")
}

// writeToolFile generates a .ts file containing tool definitions for a specific tag.
func writeToolFile(path string, tools []toolRoute, gen *echokit.TSTypeGenerator) error {
	// Determine shared type imports needed by these tools.
	refs := collectRefs(tools, gen)
	typeImports := make([]string, 0, len(refs))
	for n := range refs {
		typeImports = append(typeImports, n)
	}
	sort.Strings(typeImports)

	// Build tool definitions.
	toolDefs := make([]toolDefData, 0, len(tools))
	for _, tr := range tools {
		toolDefs = append(toolDefs, buildToolDef(tr, gen))
	}

	data := toolFileData{
		Timestamp:   time.Now().Format(time.RFC3339),
		TypeImports: typeImports,
		Tools:       toolDefs,
	}

	var buf bytes.Buffer
	if err := toolFileTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("template: %w", err)
	}

	return os.WriteFile(path, buf.Bytes(), 0644)
}

// ============================================================================
// Index file generation
// ============================================================================

const tplIndex = `// Code generated by piextgen. DO NOT EDIT.
// Generated: {{.Timestamp}}
// Regenerate with: go generate ./...

import { type ExtensionAPI } from "@mariozechner/pi-coding-agent";
{{range .Modules}}import {{.ImportName}} from "./slicer-{{.Slug}}";
{{end}}

export default function (pi: ExtensionAPI) {
{{range .Modules}}	{{.ImportName}}(pi);
{{end}}
}
`

type indexFileData struct {
	Timestamp string
	Modules   []indexModuleData
}

type indexModuleData struct {
	Slug       string
	ImportName string
}

var indexFileTmpl = template.Must(template.New("index").Parse(strings.ReplaceAll(tplIndex, "__BT__", "`")))

// writeIndex generates slicer-index.ts that imports and registers all tool modules.
func writeIndex(path string, slugs []string) error {
	modules := make([]indexModuleData, 0, len(slugs))
	for _, slug := range slugs {
		// Build a valid JS identifier from the slug (e.g. "event_table" → "eventTable").
		importName := jsModuleName(slug)
		modules = append(modules, indexModuleData{Slug: slug, ImportName: importName})
	}

	data := indexFileData{
		Timestamp: time.Now().Format(time.RFC3339),
		Modules:   modules,
	}

	var buf bytes.Buffer
	if err := indexFileTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("index template: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

// jsModuleName converts a tag slug (e.g. "event_table", "clv") into a valid JS import name.
func jsModuleName(slug string) string {
	parts := strings.Split(slug, "_")
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			result += strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return result
}

// buildToolDef converts a toolRoute into the template data structure.
func buildToolDef(tr toolRoute, gen *echokit.TSTypeGenerator) toolDefData {
	r := tr.route
	name := toolName(r)

	label := ""
	if r.OpenAPI != nil && r.OpenAPI.Summary != "" {
		label = r.OpenAPI.Summary
	} else if r.Tool.Name != "" {
		label = r.Tool.Name
	}

	desc := r.Tool.Description
	if desc == "" && r.OpenAPI != nil {
		desc = r.OpenAPI.Description
	}
	if desc == "" {
		if label != "" {
			desc = label + " endpoint."
		} else {
			desc = name + " endpoint."
		}
	}

	// Build URL path with parameter substitution.
	urlPath := normalizeEchoPath(tr.group.BasePath + r.Pattern)
	rawParams := extractPathParams(urlPath)

	var pathParams []paramDefData
	for _, pp := range rawParams {
		paramName := jsParamName(pp)
		old := "{" + pp + "}"
		urlPath = strings.ReplaceAll(urlPath, old, "${params."+paramName+"}")
		pathParams = append(pathParams, paramDefData{Name: paramName, OrigName: pp})
	}

	hasBody := r.OpenAPI != nil && r.OpenAPI.BodyType != nil

	varBody := "undefined"
	bodyExpr := ""
	if hasBody {
		varBody = "JSON.stringify(params.body)"
		bodyExpr = gen.InlineExpr(r.OpenAPI.BodyType)
	}

	return toolDefData{
		Name:        name,
		Label:       label,
		Description: desc,
		PathParams:  pathParams,
		HasBody:     hasBody,
		BodyExpr:    bodyExpr,
		URLPath:     urlPath,
		Method:      r.Method,
		VarBody:     varBody,
	}
}

// collectRefs collects all shared type names referenced by tool body types.
func collectRefs(tools []toolRoute, gen *echokit.TSTypeGenerator) map[string]bool {
	refs := map[string]bool{}
	for _, tr := range tools {
		if tr.route.OpenAPI != nil && tr.route.OpenAPI.BodyType != nil {
			name := gen.TSName(tr.route.OpenAPI.BodyType)
			if name != "" && len(gen.NamedTypes()) > 0 {
				// Check if this type name is a shared type.
				refs[name] = true
			}
		}
	}
	return refs
}

// ============================================================================
// Helpers
// ============================================================================

// normalizeEchoPath converts Echo's :param syntax to {param} syntax.
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

// extractPathParams extracts parameter names from a path template.
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

// jsParamName converts a path parameter name to camelCase.
func jsParamName(name string) string {
	if len(name) == 0 {
		return name
	}
	return strings.ToLower(name[:1]) + name[1:]
}