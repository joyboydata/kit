package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// LogCall represents one extracted log call.
type LogCall struct {
	File        string
	Line        int
	Level       string   // Info, Warn, Error, Debug, Fatal
	Message     string   // first string literal argument
	Description string   // from @log.desc
	Fields      []Field
	TypeValue   string   // value of the "type" field, if present
}

// Field represents one slog field in a log call.
type Field struct {
	Name        string
	Type        string // string, int, bool, duration, any, etc.
	Description string // from @log.field
	Value       string // value if it's a string literal, used for "type" field
}

func main() {
	out := flag.String("out", "docs/logging.md", "output markdown file path")
	flag.Parse()

	// Find all .go files in the project (excluding _test.go and _templ.go)
	var files []string
	root := "."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip hidden dirs, node_modules, .git
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			if info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") &&
			!strings.HasSuffix(path, "_test.go") &&
			!strings.HasSuffix(path, "_templ.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk: %v\n", err)
		os.Exit(1)
	}

	// Parse each file
	var calls []LogCall
	fset := token.NewFileSet()
	for _, fpath := range files {
		f, err := parser.ParseFile(fset, fpath, nil, parser.ParseComments)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", fpath, err)
			continue
		}
		calls = append(calls, extractCalls(fset, f, fpath)...)
	}

	// Sort by file then line
	sort.Slice(calls, func(i, j int) bool {
		if calls[i].File != calls[j].File {
			return calls[i].File < calls[j].File
		}
		return calls[i].Line < calls[j].Line
	})

	// Generate markdown
	md := generateMarkdown(calls)

	if err := os.WriteFile(*out, []byte(md), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
		os.Exit(1)
	}
	fmt.Printf("generated %s (%d log events)\n", *out, len(calls))

	// Validate all structured log events have a "type" field with a value
	var missingType []string
	for _, c := range calls {
		if c.Fields == nil {
			continue // unstructured log (Fatalf, Printf, etc.) — skip
		}
		if c.TypeValue == "" {
			missingType = append(missingType, fmt.Sprintf("%s:%d  msg=%q", c.File, c.Line, c.Message))
		}
	}
	if len(missingType) > 0 {
		fmt.Fprintf(os.Stderr, "ERROR: %d log event(s) missing a 'type' field:\n", len(missingType))
		for _, m := range missingType {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
		os.Exit(1)
	}
}

// attrSliceDef maps variable names to their field definitions from []slog.Attr{...}.
type attrSliceDef struct {
	fields []Field
	line   int
}

// extractCalls finds all log calls in a file.
func extractCalls(fset *token.FileSet, f *ast.File, fpath string) []LogCall {
	// Collect all comment groups keyed by line position
	comments := map[int]string{} // line → comment text
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			line := fset.Position(c.Pos()).Line
			comments[line] = strings.TrimSpace(c.Text)
		}
	}

	// Pre-process: find all []slog.Attr{...} composite literals and their variable names
	attrSlices := findAttrSliceDefs(fset, f, comments, fpath)

	var calls []LogCall

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		lc := extractLogCall(fset, call, comments, attrSlices, fpath)
		if lc != nil {
			lc.File = fpath
			calls = append(calls, *lc)
		}
		return true
	})

	return calls
}

// findAttrSliceDefs finds all []slog.Attr{...} composite literals and their variable names.
func findAttrSliceDefs(fset *token.FileSet, f *ast.File, comments map[int]string, filename string) map[string]attrSliceDef {
	defs := map[string]attrSliceDef{}

	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}

		// Check RHS is a composite literal
		cl, ok := assign.Rhs[0].(*ast.CompositeLit)
		if !ok {
			return true
		}

		// Check type is []slog.Attr
		arr, ok := cl.Type.(*ast.ArrayType)
		if !ok || arr.Len != nil {
			return true
		}
		if sel, ok := arr.Elt.(*ast.SelectorExpr); !ok || fmt.Sprintf("%s.%s", sel.X, sel.Sel) != "slog.Attr" {
			return true
		}

		// Get variable name from LHS
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}

		var fields []Field
		for _, elt := range cl.Elts {
			f := extractField(elt, comments, fset, filename)
			if f != nil {
				fields = append(fields, *f)
			}
		}

		defs[ident.Name] = attrSliceDef{
			fields: fields,
			line:   fset.Position(cl.Pos()).Line,
		}
		return true
	})

	return defs
}

// extractLogCall checks if a call expression is a log call and extracts info.
func extractLogCall(fset *token.FileSet, call *ast.CallExpr, comments map[int]string, attrSlices map[string]attrSliceDef, fpath string) *LogCall {
	fn, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	// Determine the receiver (e.g., "slog", "log", "logger", "s.logger")
	var receiver string
	switch x := fn.X.(type) {
	case *ast.Ident:
		receiver = x.Name
	case *ast.SelectorExpr:
		receiver = fmt.Sprintf("%s.%s", x.X, x.Sel.Name)
	default:
		return nil
	}

	method := fn.Sel.Name

	// Determine log level
	level := logLevel(receiver, method)
	if level == "" {
		return nil
	}

	pos := fset.Position(call.Pos())
	line := pos.Line

	// Extract message from first string literal argument
	msg := extractMessage(call, method)

	// Extract @log.desc from comments preceding this line
	desc := extractDesc(comments, line)

	// Extract fields
	fields := extractFields(call, comments, line, attrSlices, fset, fpath)

	// Post-process: extract "type" field value from fields
	typeVal := ""
	for _, f := range fields {
		if f.Name == "type" && f.Value != "" {
			typeVal = f.Value
			break
		}
	}

	return &LogCall{
		Line:        line,
		Level:       level,
		Message:     msg,
		Description: desc,
		Fields:      fields,
		TypeValue:   typeVal,
	}
}

// logLevel returns the log level string if the call is a log function.
func logLevel(receiver, method string) string {
	// Only accept known log receivers
	if !isKnownLogReceiver(receiver) {
		return ""
	}

	switch method {
	case "Info":
		return "INFO"
	case "Warn":
		return "WARN"
	case "Error":
		return "ERROR"
	case "Debug":
		return "DEBUG"
	case "LogAttrs":
		return "INFO / WARN / ERROR" // level is dynamic
	case "Fatalf":
		return "FATAL"
	case "Printf":
		return "INFO"
	case "Println":
		return "INFO"
	}
	return ""
}

// isKnownLogReceiver checks if the receiver is a known logger variable or package.
func isKnownLogReceiver(receiver string) bool {
	switch receiver {
	case "slog", // slog.Info(...)
		"log", // log.Fatalf(...), log.Printf(...)
		"logger", // logger.Info(...)
		"httpLogger", // httpLogger.Info(...)
		"reqLogger", // reqLogger.Debug(...)
		"s.logger", // s.logger.Info(...)
		"r.logger", // r.logger.Debug(...)
		"s.httpLogger": // s.httpLogger.Debug(...)
		return true
	}
	return false
}

// extractMessage gets the first string literal argument of a log call.
func extractMessage(call *ast.CallExpr, method string) string {
	if method == "LogAttrs" {
		// LogAttrs(ctx, level, msg, attrs...) — msg is 3rd arg
		if len(call.Args) >= 3 {
			if lit, ok := call.Args[2].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				return trimQuotes(lit.Value)
			}
		}
		return ""
	}

	// For Fatalf/Printf, the first arg is a format string
	if method == "Fatalf" || method == "Printf" {
		if len(call.Args) >= 1 {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				return trimQuotes(lit.Value)
			}
		}
		return ""
	}

	// For Println, first arg is the message
	if method == "Println" {
		if len(call.Args) >= 1 {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				return trimQuotes(lit.Value)
			}
		}
		return ""
	}

	// For Info/Warn/Error/Debug: first arg is msg
	if len(call.Args) >= 1 {
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return trimQuotes(lit.Value)
		}
	}
	return ""
}

// extractFields extracts fields from a log call.
func extractFields(call *ast.CallExpr, comments map[int]string, callLine int, attrSlices map[string]attrSliceDef, fset *token.FileSet, fpath string) []Field {
	var fields []Field

	method := ""
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		method = sel.Sel.Name
	}

	// These don't have structured fields
	if method == "Fatalf" || method == "Printf" || method == "Println" {
		return nil
	}

	var fieldArgs []ast.Expr

	if method == "LogAttrs" {
		// LogAttrs(ctx, level, msg, args...)
		// Args after the first 3 are the field args (inline or slice)
		if len(call.Args) >= 4 {
			remaining := call.Args[3:]
			if len(remaining) == 1 {
				// Could be a composite literal or variable reference
				if cl, ok := remaining[0].(*ast.CompositeLit); ok {
					// []slog.Attr{...} inline
					fieldArgs = cl.Elts
				} else if ident, ok := remaining[0].(*ast.Ident); ok {
					// Variable reference like `attrs...` — look up in pre-processed defs
					if def, ok := attrSlices[ident.Name]; ok {
						fields = append(fields, def.fields...)
						return fields
					}
				}
			} else {
				// Inline slog.Xxx() args
				fieldArgs = remaining
			}
		}
	} else {
		// Info/Warn/Error/Debug: args[1:] are the fields
		// They could be slog.Xxx() calls OR key-value pairs
		if len(call.Args) > 1 {
			fieldArgs = call.Args[1:]
		}
	}

	// First pass: try to extract as slog.Xxx() calls
	for _, arg := range fieldArgs {
		f := extractField(arg, comments, fset, fpath)
		if f != nil {
			fields = append(fields, *f)
		}
	}

	// If no slog.Xxx() fields found, try key-value pairs
	if len(fields) == 0 && len(fieldArgs) > 0 {
		fields = extractKeyValuePairs(fieldArgs)
	}

	return fields
}

// extractKeyValuePairs extracts fields from alternating key-value arguments.
func extractKeyValuePairs(args []ast.Expr) []Field {
	var fields []Field
	for i := 0; i < len(args)-1; i += 2 {
		keyLit, ok := args[i].(*ast.BasicLit)
		if !ok || keyLit.Kind != token.STRING {
			continue
		}
		name := trimQuotes(keyLit.Value)
		valExpr := args[i+1]
		valType := inferValueType(valExpr)

		// Capture value if it's a string literal
		var val string
		if valLit, ok := valExpr.(*ast.BasicLit); ok && valLit.Kind == token.STRING {
			val = trimQuotes(valLit.Value)
		}

		fields = append(fields, Field{
			Name:  name,
			Type:  valType,
			Value: val,
		})
	}
	return fields
}

// inferValueType tries to determine the type of a value expression.
func inferValueType(expr ast.Expr) string {
	if call, ok := expr.(*ast.CallExpr); ok {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			switch sel.Sel.Name {
			case "Method", "URL", "Path", "Host", "RequestURI", "Header", "Get":
				return "string"
			case "Status":
				return "int"
			}
		}
		return "string"
	}
	if lit, ok := expr.(*ast.BasicLit); ok {
		switch lit.Kind {
		case token.INT:
			return "int"
		case token.FLOAT:
			return "float"
		case token.STRING:
			return "string"
		}
	}
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		switch sel.Sel.Name {
		case "Method", "Path", "Host", "URL", "RequestURI":
			return "string"
		case "Status", "Size":
			return "int"
		}
		return "any"
	}
	if _, ok := expr.(*ast.Ident); ok {
		return "any"
	}
	return "any"
}

// extractField extracts a single Field from an AST expression.
// It handles slog.Xxx(key, val) calls.
func extractField(expr ast.Expr, comments map[int]string, fset *token.FileSet, filename string) *Field {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil
	}

	fn, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	fieldType := slogType(fn.Sel.Name)
	if fieldType == "" {
		return nil
	}

	if len(call.Args) < 1 {
		return nil
	}
	nameLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || nameLit.Kind != token.STRING {
		return nil
	}
	name := trimQuotes(nameLit.Value)

	// Extract value if second arg is a string literal (for "type" field)
	var val string
	if len(call.Args) >= 2 {
		if valLit, ok := call.Args[1].(*ast.BasicLit); ok && valLit.Kind == token.STRING {
			val = trimQuotes(valLit.Value)
		}
	}

	line := fset.Position(call.Pos()).Line
	desc := extractFieldDescFromFile(filename, line)

	return &Field{
		Name:        name,
		Type:        fieldType,
		Description: desc,
		Value:       val,
	}
}

// slogType maps a slog function name to the field type string.
func slogType(funcName string) string {
	switch funcName {
	case "String":
		return "string"
	case "Int":
		return "int"
	case "Int64":
		return "int64"
	case "Bool":
		return "bool"
	case "Float64":
		return "float64"
	case "Duration":
		return "duration"
	case "Time":
		return "time"
	case "Any":
		return "any"
	case "Uint64":
		return "uint64"
	case "Group":
		return "group"
	default:
		return ""
	}
}

// extractDesc extracts the @log.desc from comments preceding a given line.
func extractDesc(comments map[int]string, callLine int) string {
	var lines []string
	// Scan backwards from callLine-1
	for line := callLine - 1; line >= callLine-10 && line > 0; line-- {
		text, ok := comments[line]
		if !ok {
			continue
		}
		// Check for @log.desc:
		re := regexp.MustCompile(`@log\.desc:\s*(.*)`)
		matches := re.FindStringSubmatch(text)
		if matches != nil {
			lines = append([]string{strings.TrimSpace(matches[1])}, lines...)
		} else {
			// Stop at first non-@log.desc comment line
			if strings.HasPrefix(text, "//") || strings.HasPrefix(text, "/*") {
				// Check if there's a blank line (empty comment) between
				// This is a non-@log.desc comment, stop
				break
			}
		}
	}
	return strings.Join(lines, " ")
}

// extractFieldDescFromFile reads a specific line from the source file
// and looks for @log.field: comment on that line.
var logFieldRe = regexp.MustCompile(`@log\.field:\s*(.*)`)

func extractFieldDescFromFile(filename string, line int) string {
	if line == 0 {
		return ""
	}
	f, err := os.Open(filename)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	current := 0
	for scanner.Scan() {
		current++
		if current == line {
			matches := logFieldRe.FindStringSubmatch(scanner.Text())
			if len(matches) >= 2 {
				return strings.TrimSpace(matches[1])
			}
			return ""
		}
	}
	return ""
}

// trimQuotes removes surrounding quotes from a string literal.
func trimQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '`' && s[len(s)-1] == '`') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// generateMarkdown produces the markdown output.
func generateMarkdown(calls []LogCall) string {
	var b strings.Builder

	b.WriteString("# Log Schema\n\n")
	b.WriteString("> Auto-generated by `x/logkit/cmd`. Do not edit manually.\n")
	b.WriteString("> Regenerate: `go generate ./...`\n\n")
	b.WriteString("Each log event is annotated with `@log.desc` (event description) and\n")
	b.WriteString("`@log.field` (field description) tags in the source code.\n\n")

	// Type Index — one row per unique type value
	b.WriteString("## Type Index\n\n")
	b.WriteString("| Type | Event | Source |\n")
	b.WriteString("|---|---|---|\n")
	for _, c := range calls {
		if c.TypeValue != "" {
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | `%s:%d` |\n", c.TypeValue, c.Message, c.File, c.Line))
		}
	}
	b.WriteString("\n---\n\n")

	var currentFile string
	for _, c := range calls {
		if c.File != currentFile {
			currentFile = c.File
			b.WriteString(fmt.Sprintf("## `%s`\n\n", c.File))
		}

		// Level badges
		levelBadge := levelToBadge(c.Level)

		b.WriteString(fmt.Sprintf("### `%s` (line %d) %s\n\n", c.Message, c.Line, levelBadge))

		if c.Description != "" {
			b.WriteString(fmt.Sprintf("%s\n\n", c.Description))
		}

		if len(c.Fields) > 0 {
			b.WriteString("| Field | Type | Description |\n")
			b.WriteString("|---|---|---|\n")
			for _, f := range c.Fields {
				desc := f.Description
				if desc == "" {
					desc = "—"
				}
				b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s |\n", f.Name, f.Type, desc))
			}
			b.WriteString("\n")
		} else {
			b.WriteString("_No structured fields._\n\n")
		}

		b.WriteString("---\n\n")
	}

	return b.String()
}

// levelToBadge converts a level string to a markdown badge.
func levelToBadge(level string) string {
	switch level {
	case "INFO":
		return "![level:info](https://img.shields.io/badge/INFO-4ade80?style=flat-square)"
	case "WARN":
		return "![level:warn](https://img.shields.io/badge/WARN-facc15?style=flat-square)"
	case "ERROR":
		return "![level:error](https://img.shields.io/badge/ERROR-f87171?style=flat-square)"
	case "FATAL":
		return "![level:fatal](https://img.shields.io/badge/FATAL-dc2626?style=flat-square)"
	case "DEBUG":
		return "![level:debug](https://img.shields.io/badge/DEBUG-94a3b8?style=flat-square)"
	case "INFO / WARN / ERROR":
		return "![level:dynamic](https://img.shields.io/badge/INFO%2FWARN%2FERROR-cyan?style=flat-square)"
	default:
		return ""
	}
}
