package agentkit

import (
	"fmt"
	"reflect"
	"strings"
)

// injectOutputFormat appends a JSON schema description of T and output
// instructions to the prompt. The caller only needs to provide domain logic;
// the output contract is derived from T at compile time.
func injectOutputFormat[T any](prompt string) string {
	var t T
	schema := describeSchema(reflect.TypeOf(t), 0)
	return prompt + fmt.Sprintf(`


Output ONLY a valid JSON object matching this schema:

%s

Do not include markdown code fences, explanations, or any other text. Only the raw JSON object.
`, schema)
}

// describeSchema returns a human-readable JSON schema description for a Go type
// at the given indentation depth.
func describeSchema(t reflect.Type, depth int) string {
	// Dereference pointer types
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Struct:
		return describeStruct(t, depth)
	case reflect.Slice:
		elemSchema := describeSchema(t.Elem(), depth)
		return fmt.Sprintf("[%s]", elemSchema)
	case reflect.Map:
		valueSchema := describeSchema(t.Elem(), depth)
		return fmt.Sprintf(`{string: %s}`, valueSchema)
	default:
		return jsonTypeName(t)
	}
}

// describeStruct describes a struct type with fields at the given depth.
func describeStruct(t reflect.Type, depth int) string {
	indent := strings.Repeat("  ", depth)
	fieldIndent := strings.Repeat("  ", depth+1)

	var fields []string
	for i := range t.NumField() {
		f := t.Field(i)

		if !f.IsExported() {
			continue
		}

		jsonName, skip, optional := parseJSONTag(f)
		if skip {
			continue
		}

		// For struct and slice-of-struct fields, the closing bracket/brace
		// uses parent-level indentation. We pass depth+1 so the inner
		// elements are indented one level deeper.
		fieldSchema := describeSchema(f.Type, depth+1)

		// Collect comments from tags
		var comments []string
		if desc := f.Tag.Get("description"); desc != "" {
			comments = append(comments, desc)
		}
		if optional {
			comments = append(comments, "optional")
		}

		if len(comments) > 0 {
			fields = append(fields, fmt.Sprintf(`%s"%s": %s, // %s`, fieldIndent, jsonName, fieldSchema, strings.Join(comments, ", ")))
		} else {
			fields = append(fields, fmt.Sprintf(`%s"%s": %s`, fieldIndent, jsonName, fieldSchema))
		}
	}

	if len(fields) == 0 {
		return "{}"
	}

	return fmt.Sprintf("{\n%s\n%s}", strings.Join(fields, "\n"), indent)
}

// parseJSONTag extracts the JSON field name, whether to skip the field ("-"),
// and whether the field is optional ("omitempty").
func parseJSONTag(f reflect.StructField) (name string, skip bool, optional bool) {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		if tag == "-" {
			return "", true, false
		}
		return f.Name, false, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, p := range parts[1:] {
		if p == "omitempty" {
			optional = true
		}
	}
	return name, false, optional
}

// jsonTypeName returns the JSON type name for a Go kind.
func jsonTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "number"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice:
		elemSchema := jsonTypeName(t.Elem())
		return fmt.Sprintf("[%s]", elemSchema)
	case reflect.Map:
		return fmt.Sprintf(`{string: %s}`, jsonTypeName(t.Elem()))
	default:
		return "any"
	}
}