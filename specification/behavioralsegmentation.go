package specification

import (
	"encoding/json"
	"sort"

	"gopkg.in/yaml.v3"
)

// ============================================================================
// Specification Types
// ============================================================================

// +spec root Segment
//
// BehavioralSegmentation contains the logical specification for segment inclusion and exclusion rules.
// It decouples segment metadata (e.g., name, description) from the logical segmentation
// criteria. Inclusion is mandatory, while Exclusion is optional and omitted from JSON when nil.
type BehavioralSegmentation struct {
	UserbaseID string `json:"userbase_id" yaml:"userbase_id"` // Userbase table identifier (references external metadata)
	Inclusion  Rule   `json:"inclusion" yaml:"inclusion"`     // Criteria that users must match to be included
	Exclusion  *Rule  `json:"exclusion,omitempty" yaml:"exclusion,omitempty"` // Criteria that exclude users from the segment
}

// Rule defines a logical composition of slices combined with a relation operator.
// Rules are recursive building blocks representing simple conditions or complex boolean logic.
type Rule struct {
	Relation RelationOperator `json:"relation" yaml:"relation"` // How slices are combined — "union" (OR) or "intersection" (AND)
	Slices   []Slice          `json:"slices" yaml:"slices"`     // One or more slice definitions to evaluate
}

// Slice represents a logical unit of segmentation.
type Slice struct {
	Type       string      `json:"type" yaml:"type"`                               // Slice type identifier — currently "past_action"
	PastAction *PastAction `json:"past_action,omitempty" yaml:"past_action,omitempty"` // Past-action configuration (required when type is "past_action")
}

// PastAction defines selection criteria based on a user's historical actions and profile attributes.
// User actions are combined using the relation operator, and optional user attributes act as additional filters.
type PastAction struct {
	Relation       RelationOperator     `json:"relation" yaml:"relation"`                         // How user actions are combined — "union" (OR) or "intersection" (AND)
	UserActions    []UserAction         `json:"user_actions" yaml:"user_actions"`                 // Event filters describing the actions a user must have performed
	UserAttributes []AttributeCondition `json:"user_attributes,omitempty" yaml:"user_attributes,omitempty"` // Optional filters on user profile attributes
}

// UserAction describes an event that a user may have performed, with optional time, frequency, and attribute constraints.
type UserAction struct {
	EventID      string               `json:"event_id" yaml:"event_id"`                               // Logical event identifier referencing external metadata
	Window       *TimeWindow          `json:"window,omitempty" yaml:"window,omitempty"`               // Time constraint for when the event must have occurred
	Occurrence   *Occurrence          `json:"occurrence,omitempty" yaml:"occurrence,omitempty"`       // Frequency constraint — how many times the event must occur
	Attributions []AttributeCondition `json:"attributions,omitempty" yaml:"attributions,omitempty"`   // Optional filters on event attributes
}

// TimeWindow defines time constraints for events.
// Operator must be one of TimeWindowBetween or TimeWindowInTheLast.
type TimeWindow struct {
	Operator TimeWindowOperator `json:"operator" yaml:"operator"` // Type of time constraint — "between" or "in_the_last"
	Params   []string           `json:"params" yaml:"params"`     // Parameters: ["start","end"] for between, ["30d"] for in_the_last
}

// Occurrence specifies count-based event constraints.
// Used to express constraints like "purchased more than 3 times" or "logged in at least 5 times".
type Occurrence struct {
	Operator OccurrenceOperator `json:"operator" yaml:"operator"` // Comparison operator — "=", ">", ">=", "<", or "<="
	Count    int                `json:"count" yaml:"count"`       // Threshold count for comparison
}

// AttributeCondition defines filter constraints on user attributes or event metadata.
// Supports equality, inequality, numeric comparison, and set membership checks.
type AttributeCondition struct {
	Key      string            `json:"key" yaml:"key"`         // Attribute name (e.g., "device", "tier", "age")
	Operator AttributeOperator `json:"operator" yaml:"operator"` // Comparison — "=", "!=", "in", "not_in", ">", ">=", "<", or "<="
	Value    []string          `json:"value" yaml:"value"`     // Values to compare against
}

// MarshalJSON ensures proper serialization of AttributeCondition
func (a AttributeCondition) MarshalJSON() ([]byte, error) {
	type Alias AttributeCondition
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(&a),
	})
}

// UnmarshalJSON ensures proper deserialization of AttributeCondition
func (a *AttributeCondition) UnmarshalJSON(data []byte) error {
	type Alias AttributeCondition
	aux := &Alias{}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	*a = AttributeCondition(*aux)
	return nil
}

// ============================================================================
// Operator Types — Type-safe constants for struct fields.
// Metadata is defined in data-driven defs arrays below.
// ============================================================================

// TimeWindowOperator defines the operator for time window constraints.
type TimeWindowOperator string

const (
	// TimeWindowBetween selects events between two dates.
	TimeWindowBetween TimeWindowOperator = "between"
	// TimeWindowInTheLast selects events within the last N days.
	TimeWindowInTheLast TimeWindowOperator = "in_the_last"
)

// TimeWindowOperatorDef describes a time window operator and its metadata.
type TimeWindowOperatorDef struct {
	Operator      TimeWindowOperator
	DisplayName   string
	Description   string
	SyntaxExample string
	ParamCount    int
	ParamHints    []string
	ParamTypes    []string
}

// TimeWindowOperatorDefs is the canonical set of defined time window operators.
var TimeWindowOperatorDefs = map[TimeWindowOperator]TimeWindowOperatorDef{
	TimeWindowInTheLast: {
		DisplayName:   "In the last...",
		Description:   "Selects events within a relative duration. Suffixes: h (hours), d (days), w (weeks), mo (months)",
		SyntaxExample: `{ "operator": "in_the_last", "params": ["30d"] }`,
		ParamCount:    1,
		ParamHints:    []string{"e.g., 30d"},
		ParamTypes:    []string{"duration"},
	},
	TimeWindowBetween: {
		DisplayName:   "Between...",
		Description:   "Selects events between two fixed dates",
		SyntaxExample: `{ "operator": "between", "params": ["2026-01-01", "2026-06-30"] }`,
		ParamCount:    2,
		ParamHints:    []string{"Start date", "End date"},
		ParamTypes:    []string{"date", "date"},
	},
}

// DisplayName returns a user-friendly label for the operator.
func (o TimeWindowOperator) DisplayName() string {
	if d, ok := TimeWindowOperatorDefs[o]; ok {
		return d.DisplayName
	}
	return ""
}

// Description returns a description of the operator.
func (o TimeWindowOperator) Description() string {
	if d, ok := TimeWindowOperatorDefs[o]; ok {
		return d.Description
	}
	return ""
}

// SyntaxExample returns an example JSON snippet for the operator.
func (o TimeWindowOperator) SyntaxExample() string {
	if d, ok := TimeWindowOperatorDefs[o]; ok {
		return d.SyntaxExample
	}
	return ""
}

// ParamCount returns the number of parameters required by this operator.
func (o TimeWindowOperator) ParamCount() int {
	if d, ok := TimeWindowOperatorDefs[o]; ok {
		return d.ParamCount
	}
	return 0
}

// ParamHints returns hints for each parameter.
func (o TimeWindowOperator) ParamHints() []string {
	if d, ok := TimeWindowOperatorDefs[o]; ok {
		return d.ParamHints
	}
	return nil
}

// ParamTypes returns type hints for each parameter.
func (o TimeWindowOperator) ParamTypes() []string {
	if d, ok := TimeWindowOperatorDefs[o]; ok {
		return d.ParamTypes
	}
	return nil
}

// IsValid returns true if the operator is defined.
func (o TimeWindowOperator) IsValid() bool {
	_, ok := TimeWindowOperatorDefs[o]
	return ok
}

// AllTimeWindowOperators returns all defined time window operators in sorted order.
func AllTimeWindowOperators() []TimeWindowOperator {
	result := make([]TimeWindowOperator, 0, len(TimeWindowOperatorDefs))
	for k := range TimeWindowOperatorDefs {
		result = append(result, k)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// OccurrenceOperator defines the operator for occurrence constraints.
type OccurrenceOperator string

const (
	// OccurrenceEQ selects events occurring exactly N times.
	OccurrenceEQ OccurrenceOperator = "="
	// OccurrenceGT selects events occurring more than N times.
	OccurrenceGT OccurrenceOperator = ">"
	// OccurrenceGTE selects events occurring N or more times.
	OccurrenceGTE OccurrenceOperator = ">="
	// OccurrenceLT selects events occurring fewer than N times.
	OccurrenceLT OccurrenceOperator = "<"
	// OccurrenceLTE selects events occurring at most N times.
	OccurrenceLTE OccurrenceOperator = "<="
)

// OccurrenceOperatorDef describes an occurrence operator and its metadata.
type OccurrenceOperatorDef struct {
	Operator      OccurrenceOperator
	DisplayName   string
	Description   string
	SyntaxExample string
	ParamCount    int
	ParamType     string
}

// OccurrenceOperatorDefs is the canonical set of defined occurrence operators.
var OccurrenceOperatorDefs = map[OccurrenceOperator]OccurrenceOperatorDef{
	OccurrenceEQ: {
		DisplayName:   "exactly",
		Description:   "Events occurred exactly N times",
		SyntaxExample: `{ "operator": "=", "count": 1 }`,
		ParamCount:    1,
		ParamType:     "integer",
	},
	OccurrenceGT: {
		DisplayName:   "more than",
		Description:   "Events occurred more than N times",
		SyntaxExample: `{ "operator": ">", "count": 3 }`,
		ParamCount:    1,
		ParamType:     "integer",
	},
	OccurrenceGTE: {
		DisplayName:   "at least",
		Description:   "Events occurred N or more times",
		SyntaxExample: `{ "operator": ">=", "count": 5 }`,
		ParamCount:    1,
		ParamType:     "integer",
	},
	OccurrenceLT: {
		DisplayName:   "less than",
		Description:   "Events occurred fewer than N times",
		SyntaxExample: `{ "operator": "<", "count": 2 }`,
		ParamCount:    1,
		ParamType:     "integer",
	},
	OccurrenceLTE: {
		DisplayName:   "at most",
		Description:   "Events occurred at most N times",
		SyntaxExample: `{ "operator": "<=", "count": 5 }`,
		ParamCount:    1,
		ParamType:     "integer",
	},
}

// DisplayName returns a user-friendly label for the operator.
func (o OccurrenceOperator) DisplayName() string {
	if d, ok := OccurrenceOperatorDefs[o]; ok {
		return d.DisplayName
	}
	return ""
}

// Description returns a description of the operator.
func (o OccurrenceOperator) Description() string {
	if d, ok := OccurrenceOperatorDefs[o]; ok {
		return d.Description
	}
	return ""
}

// SyntaxExample returns an example JSON snippet for the operator.
func (o OccurrenceOperator) SyntaxExample() string {
	if d, ok := OccurrenceOperatorDefs[o]; ok {
		return d.SyntaxExample
	}
	return ""
}

// ParamCount returns the number of parameters required by this operator.
func (o OccurrenceOperator) ParamCount() int {
	if d, ok := OccurrenceOperatorDefs[o]; ok {
		return d.ParamCount
	}
	return 0
}

// ParamType returns the type hint for the parameter.
func (o OccurrenceOperator) ParamType() string {
	if d, ok := OccurrenceOperatorDefs[o]; ok {
		return d.ParamType
	}
	return ""
}

// IsValid returns true if the operator is defined.
func (o OccurrenceOperator) IsValid() bool {
	_, ok := OccurrenceOperatorDefs[o]
	return ok
}

// AllOccurrenceOperators returns all defined occurrence operators in sorted order.
func AllOccurrenceOperators() []OccurrenceOperator {
	result := make([]OccurrenceOperator, 0, len(OccurrenceOperatorDefs))
	for k := range OccurrenceOperatorDefs {
		result = append(result, k)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// AttributeOperator defines the operator for attribute conditions.
type AttributeOperator string

const (
	// AttrEq selects where attribute equals value.
	AttrEq AttributeOperator = "="
	// AttrNeq selects where attribute does not equal value.
	AttrNeq AttributeOperator = "!="
	// AttrIn selects where attribute is in a list of values.
	AttrIn AttributeOperator = "in"
	// AttrNotIn selects where attribute is not in a list of values.
	AttrNotIn AttributeOperator = "not_in"
	// AttrGT selects where attribute is greater than value.
	AttrGT AttributeOperator = ">"
	// AttrGTE selects where attribute is greater than or equal to value.
	AttrGTE AttributeOperator = ">="
	// AttrLT selects where attribute is less than value.
	AttrLT AttributeOperator = "<"
	// AttrLTE selects where attribute is less than or equal to value.
	AttrLTE AttributeOperator = "<="
)

// AttributeOperatorDef describes an attribute operator and its metadata.
type AttributeOperatorDef struct {
	DisplayName      string
	Description      string
	SyntaxExample    string
	ParamCount       int
	SupportsMultiple bool
}

// AttributeOperatorDefs is the canonical set of defined attribute operators.
var AttributeOperatorDefs = map[AttributeOperator]AttributeOperatorDef{
	AttrEq: {
		DisplayName: "equals",
		Description: "Attribute equals the given value",
		SyntaxExample: `{ "key": "tier", "operator": "=", "value": ["gold"] }`,
		ParamCount:       1,
		SupportsMultiple: false,
	},
	AttrNeq: {
		DisplayName: "not equals",
		Description: "Attribute does not equal the given value",
		SyntaxExample: `{ "key": "tier", "operator": "!=", "value": ["free"] }`,
		ParamCount:       1,
		SupportsMultiple: false,
	},
	AttrGT: {
		DisplayName: "greater than",
		Description: "Attribute is greater than the given value",
		SyntaxExample: `{ "key": "age", "operator": ">", "value": ["18"] }`,
		ParamCount:       1,
		SupportsMultiple: false,
	},
	AttrGTE: {
		DisplayName: "greater than or equal",
		Description: "Attribute is greater than or equal to the given value",
		SyntaxExample: `{ "key": "age", "operator": ">=", "value": ["21"] }`,
		ParamCount:       1,
		SupportsMultiple: false,
	},
	AttrLT: {
		DisplayName: "less than",
		Description: "Attribute is less than the given value",
		SyntaxExample: `{ "key": "revenue", "operator": "<", "value": ["50"] }`,
		ParamCount:       1,
		SupportsMultiple: false,
	},
	AttrLTE: {
		DisplayName: "less than or equal",
		Description: "Attribute is less than or equal to the given value",
		SyntaxExample: `{ "key": "spend", "operator": "<=", "value": ["500"] }`,
		ParamCount:       1,
		SupportsMultiple: false,
	},
	AttrIn: {
		DisplayName: "is one of",
		Description: "Attribute is any of the listed values (supports multiple)",
		SyntaxExample: `{ "key": "device", "operator": "in", "value": ["ios", "android"] }`,
		ParamCount:       1,
		SupportsMultiple: true,
	},
	AttrNotIn: {
		DisplayName: "is not one of",
		Description: "Attribute is none of the listed values (supports multiple)",
		SyntaxExample: `{ "key": "country", "operator": "not_in", "value": ["US", "CA"] }`,
		ParamCount:       1,
		SupportsMultiple: true,
	},
}

// DisplayName returns a user-friendly label for the operator.
func (o AttributeOperator) DisplayName() string {
	if d, ok := AttributeOperatorDefs[o]; ok {
		return d.DisplayName
	}
	return ""
}

// Description returns a description of the operator.
func (o AttributeOperator) Description() string {
	if d, ok := AttributeOperatorDefs[o]; ok {
		return d.Description
	}
	return ""
}

// SyntaxExample returns an example JSON snippet for the operator.
func (o AttributeOperator) SyntaxExample() string {
	if d, ok := AttributeOperatorDefs[o]; ok {
		return d.SyntaxExample
	}
	return ""
}

// ParamCount returns the number of values required by this operator.
func (o AttributeOperator) ParamCount() int {
	if d, ok := AttributeOperatorDefs[o]; ok {
		return d.ParamCount
	}
	return 0
}

// SupportsMultiple returns whether the operator supports multiple values (e.g., "in", "not_in").
func (o AttributeOperator) SupportsMultiple() bool {
	if d, ok := AttributeOperatorDefs[o]; ok {
		return d.SupportsMultiple
	}
	return false
}

// IsValid returns true if the operator is defined.
func (o AttributeOperator) IsValid() bool {
	_, ok := AttributeOperatorDefs[o]
	return ok
}

// AllAttributeOperators returns all defined attribute operators in sorted order.
func AllAttributeOperators() []AttributeOperator {
	result := make([]AttributeOperator, 0, len(AttributeOperatorDefs))
	for k := range AttributeOperatorDefs {
		result = append(result, k)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// RelationOperator defines how slices or actions are combined.
type RelationOperator string

const (
	// RelationUnion combines with OR logic.
	RelationUnion RelationOperator = "union"
	// RelationIntersection combines with AND logic.
	RelationIntersection RelationOperator = "intersection"
)

// RelationOperatorDef describes a relation operator and its metadata.
type RelationOperatorDef struct {
	Operator      RelationOperator
	DisplayName   string
	Description   string
	SyntaxExample string
}

// RelationOperatorDefs is the canonical set of defined relation operators.
var RelationOperatorDefs = map[RelationOperator]RelationOperatorDef{
	RelationUnion: {
		Operator:      RelationUnion,
		DisplayName:   "any of",
		Description:   "OR logic — user matches if they satisfy any of the conditions",
		SyntaxExample: `"relation": "union"`,
	},
	RelationIntersection: {
		Operator:      RelationIntersection,
		DisplayName:   "all of",
		Description:   "AND logic — user matches only if they satisfy all conditions",
		SyntaxExample: `"relation": "intersection"`,
	},
}

// DisplayName returns a user-friendly label for the operator.
func (o RelationOperator) DisplayName() string {
	if d, ok := RelationOperatorDefs[o]; ok {
		return d.DisplayName
	}
	return ""
}

// Description returns a description of the operator.
func (o RelationOperator) Description() string {
	if d, ok := RelationOperatorDefs[o]; ok {
		return d.Description
	}
	return ""
}

// SyntaxExample returns an example JSON snippet for the operator.
func (o RelationOperator) SyntaxExample() string {
	if d, ok := RelationOperatorDefs[o]; ok {
		return d.SyntaxExample
	}
	return ""
}

// IsValid returns true if the operator is defined.
func (o RelationOperator) IsValid() bool {
	_, ok := RelationOperatorDefs[o]
	return ok
}

// AllRelationOperators returns all defined relation operators in sorted order.
func AllRelationOperators() []RelationOperator {
	result := make([]RelationOperator, 0, len(RelationOperatorDefs))
	for k := range RelationOperatorDefs {
		result = append(result, k)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// ============================================================================
// YAML Marshaling/Unmarshaling
// ============================================================================

// MarshalYAML serializes the BehavioralSegmentation into YAML bytes.
func MarshalYAML(s *BehavioralSegmentation) ([]byte, error) {
	return yaml.Marshal(s)
}

// UnmarshalYAML deserializes YAML bytes into a BehavioralSegmentation.
func UnmarshalYAML(data []byte) (*BehavioralSegmentation, error) {
	var s BehavioralSegmentation
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
