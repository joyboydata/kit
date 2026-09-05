package agentkit

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// ----- test types -----

type schemaFlat struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Flag  bool   `json:"flag"`
}

type schemaWithOptional struct {
	Label string  `json:"label"`
	Value string  `json:"value,omitempty"`
	Score float64 `json:"score,omitempty"`
}

type schemaSkipped struct {
	ID    string `json:"-"`
	Name  string `json:"name"`
}

type schemaNested struct {
	ID      string       `json:"id"`
	Profile schemaFlat   `json:"profile"`
	Tags    []string     `json:"tags"`
}

type schemaSliceOfStructs struct {
	Items []schemaFlat `json:"items"`
}

type schemaMapField struct {
	Labels map[string]string `json:"labels"`
}

type schemaDescribed struct {
	Name  string `json:"name" description:"The user's display name"`
	Count int    `json:"count" description:"Number of visits"`
}

type schemaDescribedOptional struct {
	Label string `json:"label,omitempty" description:"Short label for display"`
	Value string `json:"value,omitempty" description:"The computed value"`
}

type schemaDescribedNested struct {
	ID      string                `json:"id" description:"Unique identifier"`
	Profile schemaDescribed       `json:"profile" description:"User profile details"`
}

type schemaMixed struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty" description:"Optional display title"`
	Count    int    `json:"count" description:"Total count"`
	Internal string `json:"-"`
}

type schemaAllNumbers struct {
	A int   `json:"a"`
	B int8  `json:"b"`
	C int16 `json:"c"`
	D int32 `json:"d"`
	E int64 `json:"e"`
}

type schemaAllUnsigned struct {
	A uint   `json:"a"`
	B uint8  `json:"b"`
	C uint16 `json:"c"`
	D uint32 `json:"d"`
	E uint64 `json:"e"`
}

type schemaFloats struct {
	A float32 `json:"a"`
	B float64 `json:"b"`
}

type schemaEmpty struct{}

type schemaNoTag struct {
	Name  string
	Value int
}

type schemaUnexported struct {
	Public  string `json:"public"`
	private string
}

type schemaPointerField struct {
	Name *string `json:"name,omitempty"`
	Meta *schemaFlat `json:"meta,omitempty"`
}

// ----- parseJSONTag tests -----

func TestParseJSONTag(t *testing.T) {
	tests := []struct {
		name        string
		tag         string
		wantName    string
		wantSkip    bool
		wantOptional bool
	}{
		{name: "empty tag", tag: "", wantName: "Field", wantSkip: false, wantOptional: false},
		{name: "skip tag", tag: "-", wantSkip: true},
		{name: "simple name", tag: "my_name", wantName: "my_name", wantSkip: false, wantOptional: false},
		{name: "with omitempty", tag: "value,omitempty", wantName: "value", wantOptional: true},
		{name: "with extras", tag: "field_name,omitempty,other", wantName: "field_name", wantOptional: true},
		{name: "skip with extras", tag: "-", wantSkip: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a reflect.StructField with the given tag
			f := reflect.StructField{Name: "Field", Tag: reflect.StructTag(fmt.Sprintf(`json:"%s"`, tt.tag))}
			if tt.tag == "" {
				f = reflect.StructField{Name: "Field", Tag: ""}
			}
			name, skip, optional := parseJSONTag(f)
			if name != tt.wantName {
				t.Errorf("parseJSONTag() name = %q, want %q", name, tt.wantName)
			}
			if skip != tt.wantSkip {
				t.Errorf("parseJSONTag() skip = %v, want %v", skip, tt.wantSkip)
			}
			if optional != tt.wantOptional {
				t.Errorf("parseJSONTag() optional = %v, want %v", optional, tt.wantOptional)
			}
		})
	}
}

// ----- jsonTypeName tests -----

func TestJSONTypeName(t *testing.T) {
	tests := []struct {
		name     string
		value    interface{}
		want     string
	}{
		{name: "string", value: "", want: "string"},
		{name: "bool", value: false, want: "boolean"},
		{name: "int", value: 0, want: "number"},
		{name: "int8", value: int8(0), want: "number"},
		{name: "int16", value: int16(0), want: "number"},
		{name: "int32", value: int32(0), want: "number"},
		{name: "int64", value: int64(0), want: "number"},
		{name: "uint", value: uint(0), want: "number"},
		{name: "uint8", value: uint8(0), want: "number"},
		{name: "uint16", value: uint16(0), want: "number"},
		{name: "uint32", value: uint32(0), want: "number"},
		{name: "uint64", value: uint64(0), want: "number"},
		{name: "float32", value: float32(0), want: "number"},
		{name: "float64", value: float64(0), want: "number"},
		{name: "[]string", value: []string{}, want: "[string]"},
		{name: "map", value: map[string]int{}, want: "{string: number}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jsonTypeName(reflect.TypeOf(tt.value))
			if got != tt.want {
				t.Errorf("jsonTypeName(%T) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// ----- describeSchema tests -----

func TestDescribeSchema_FlatStruct(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaFlat{}), 0)
	want := `{
  "name": string
  "count": number
  "flag": boolean
}`
	if schema != want {
		t.Errorf("describeSchema(flat) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_WithOptional(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaWithOptional{}), 0)
	want := `{
  "label": string
  "value": string, // optional
  "score": number, // optional
}`
	if schema != want {
		t.Errorf("describeSchema(optional) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_SkippedField(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaSkipped{}), 0)
	// The ID field with json:"-" must be absent
	want := `{
  "name": string
}`
	if schema != want {
		t.Errorf("describeSchema(skipped) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_NestedStruct(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaNested{}), 0)
	want := `{
  "id": string
  "profile": {
    "name": string
    "count": number
    "flag": boolean
  }
  "tags": [string]
}`
	if schema != want {
		t.Errorf("describeSchema(nested) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_SliceOfStructs(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaSliceOfStructs{}), 0)
	want := `{
  "items": [{
    "name": string
    "count": number
    "flag": boolean
  }]
}`
	if schema != want {
		t.Errorf("describeSchema(sliceOfStructs) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_MapField(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaMapField{}), 0)
	want := `{
  "labels": {string: string}
}`
	if schema != want {
		t.Errorf("describeSchema(mapField) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_AllNumberTypes(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaAllNumbers{}), 0)
	want := `{
  "a": number
  "b": number
  "c": number
  "d": number
  "e": number
}`
	if schema != want {
		t.Errorf("describeSchema(allNumbers) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_AllUnsigned(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaAllUnsigned{}), 0)
	want := `{
  "a": number
  "b": number
  "c": number
  "d": number
  "e": number
}`
	if schema != want {
		t.Errorf("describeSchema(allUnsigned) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_Floats(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaFloats{}), 0)
	want := `{
  "a": number
  "b": number
}`
	if schema != want {
		t.Errorf("describeSchema(floats) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_EmptyStruct(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaEmpty{}), 0)
	want := `{}`
	if schema != want {
		t.Errorf("describeSchema(empty) = %q, want %q", schema, want)
	}
}

func TestDescribeSchema_NoTag(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaNoTag{}), 0)
	want := `{
  "Name": string
  "Value": number
}`
	if schema != want {
		t.Errorf("describeSchema(noTag) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_UnexportedFields(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaUnexported{}), 0)
	want := `{
  "public": string
}`
	if schema != want {
		t.Errorf("describeSchema(unexported) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_PointerFields(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaPointerField{}), 0)
	want := `{
  "name": string, // optional
  "meta": {
    "name": string
    "count": number
    "flag": boolean
  }, // optional
}`
	if schema != want {
		t.Errorf("describeSchema(pointerField) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_WithDescription(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaDescribed{}), 0)
	want := `{
  "name": string, // The user's display name
  "count": number, // Number of visits
}`
	if schema != want {
		t.Errorf("describeSchema(described) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_WithDescriptionAndOptional(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaDescribedOptional{}), 0)
	want := `{
  "label": string, // Short label for display, optional
  "value": string, // The computed value, optional
}`
	if schema != want {
		t.Errorf("describeSchema(describedOptional) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_DescribedNested(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaDescribedNested{}), 0)
	want := `{
  "id": string, // Unique identifier
  "profile": {
    "name": string, // The user's display name
    "count": number, // Number of visits
  }, // User profile details
}`
	if schema != want {
		t.Errorf("describeSchema(describedNested) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_Mixed(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaMixed{}), 0)
	want := `{
  "id": string
  "title": string, // Optional display title, optional
  "count": number, // Total count
}`
	if schema != want {
		t.Errorf("describeSchema(mixed) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestInjectOutputFormat_DescribedType(t *testing.T) {
	result := injectOutputFormat[schemaDescribed]("Do analysis.")
	if !strings.Contains(result, "The user's display name") {
		t.Errorf("injectOutputFormat should include field descriptions")
	}
	if !strings.Contains(result, "Number of visits") {
		t.Errorf("injectOutputFormat should include all field descriptions")
	}
}

func TestDescribeSchema_IndentedAtDepth1(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaFlat{}), 1)
	want := `{
    "name": string
    "count": number
    "flag": boolean
  }`
	if schema != want {
		t.Errorf("describeSchema(depth=1) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

func TestDescribeSchema_IndentedAtDepth2(t *testing.T) {
	schema := describeSchema(reflect.TypeOf(schemaFlat{}), 2)
	want := `{
      "name": string
      "count": number
      "flag": boolean
    }`
	if schema != want {
		t.Errorf("describeSchema(depth=2) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}

// ----- injectOutputFormat tests -----

func TestInjectOutputFormat(t *testing.T) {
	prompt := "Analyze the data and return results."
	result := injectOutputFormat[schemaFlat](prompt)

	// The result must contain the original prompt
	if !strings.Contains(result, prompt) {
		t.Errorf("injectOutputFormat should keep the original prompt")
	}

	// The result must contain the schema description
	if !strings.Contains(result, `"name": string`) {
		t.Errorf("injectOutputFormat should include schema fields")
	}
	if !strings.Contains(result, `"count": number`) {
		t.Errorf("injectOutputFormat should include number fields")
	}
	if !strings.Contains(result, `"flag": boolean`) {
		t.Errorf("injectOutputFormat should include boolean fields")
	}

	// The result must contain the output instructions
	if !strings.Contains(result, "Output ONLY a valid JSON object") {
		t.Errorf("injectOutputFormat should include output instructions")
	}
	if !strings.Contains(result, "Do not include markdown code fences") {
		t.Errorf("injectOutputFormat should include markdown prohibition")
	}

	// The prompt must come before the schema (ordering)
	promptIdx := strings.Index(result, prompt)
	schemaIdx := strings.Index(result, `"name":`)
	if promptIdx > schemaIdx {
		t.Errorf("prompt should appear before schema in the output")
	}
}

func TestInjectOutputFormat_EmptyPrompt(t *testing.T) {
	result := injectOutputFormat[schemaEmpty]("")
	if !strings.Contains(result, "{}") {
		t.Errorf("injectOutputFormat with empty struct should still work")
	}
}

func TestInjectOutputFormat_NestedType(t *testing.T) {
	result := injectOutputFormat[schemaNested]("Do analysis.")
	if !strings.Contains(result, `"profile"`) {
		t.Errorf("injectOutputFormat should include nested struct fields")
	}
	if !strings.Contains(result, `"tags": [string]`) {
		t.Errorf("injectOutputFormat should include slice fields")
	}
}

// ----- Integration: describeSchema with real use-case type -----

func TestDescribeSchema_Integration_OpportunityOutput(t *testing.T) {
	// Simulates the Output type from internal/opportunity
	type OpportunityMetric struct {
		Label string `json:"label"`
		Value string `json:"value"`
	}
	type Opportunity struct {
		Number     int                 `json:"number"`
		Title      string              `json:"title"`
		HighImpact bool                `json:"high_impact"`
		Body       string              `json:"body"`
		Metrics    []OpportunityMetric `json:"metrics,omitempty"`
	}
	type Output struct {
		Opportunities []Opportunity `json:"opportunities"`
	}

	schema := describeSchema(reflect.TypeOf(Output{}), 0)
	want := `{
  "opportunities": [{
    "number": number
    "title": string
    "high_impact": boolean
    "body": string
    "metrics": [{
      "label": string
      "value": string
    }], // optional
  }]
}`
	if schema != want {
		t.Errorf("describeSchema(opportunity) mismatch:\ngot:\n%s\n\nwant:\n%s", schema, want)
	}
}