package specification

// ============================================================================
// CLV Spec — Customer Lifetime Value Computation
// ============================================================================

// AggFunction defines how event values are combined per user.
type AggFunction string

const (
	// AggSum sums values per user (e.g., total revenue).
	AggSum AggFunction = "sum"
	// AggAvg averages values per user.
	AggAvg AggFunction = "avg"
)

// AggFunctionDef describes an aggregation function and its metadata.
type AggFunctionDef struct {
	Function      AggFunction
	DisplayName   string
	Description   string
	SyntaxExample string
}

// AggFunctionDefs is the canonical set of defined aggregation functions.
var AggFunctionDefs = map[AggFunction]AggFunctionDef{
	AggSum: {
		Function:      AggSum,
		DisplayName:   "Sum",
		Description:   "Sums values per user (e.g., total revenue)",
		SyntaxExample: `"per_user_agg": "sum"`,
	},
	AggAvg: {
		Function:      AggAvg,
		DisplayName:   "Average",
		Description:   "Averages values per user or across users",
		SyntaxExample: `"across_users_agg": "avg"`,
	},
}

// DisplayName returns a user-friendly label for the aggregation function.
func (a AggFunction) DisplayName() string {
	if d, ok := AggFunctionDefs[a]; ok {
		return d.DisplayName
	}
	return ""
}

// Description returns a description of the aggregation function.
func (a AggFunction) Description() string {
	if d, ok := AggFunctionDefs[a]; ok {
		return d.Description
	}
	return ""
}

// SyntaxExample returns an example JSON snippet for the aggregation function.
func (a AggFunction) SyntaxExample() string {
	if d, ok := AggFunctionDefs[a]; ok {
		return d.SyntaxExample
	}
	return ""
}

// IsValid returns true if the aggregation function is defined.
func (a AggFunction) IsValid() bool {
	_, ok := AggFunctionDefs[a]
	return ok
}

// AllAggFunctions returns all defined aggregation functions in sorted order.
func AllAggFunctions() []AggFunction {
	result := make([]AggFunction, 0, len(AggFunctionDefs))
	for k := range AggFunctionDefs {
		result = append(result, k)
	}
	return result
}

// +spec root CLV
//
// CLVSpec defines a customer lifetime value computation.
//
// It computes a per-user value aggregation over a set of events (identified
// by EventID), optionally filtered by time window and event attributes.
// The per-user values are then averaged across all (or a filtered subset of)
// users to produce the final CLV.
//
// To restrict computation to a behavioral segment, set SegmentFilter. It
// reuses the Rule type from BehavioralSegmentation, giving you the full
// power of union/intersection logic, occurrence counts, and user attributes.
type CLVSpec struct {
	UserbaseID string `json:"userbase_id" yaml:"userbase_id"` // Identifies the userbase table (references external metadata)
	EventID    string `json:"event_id" yaml:"event_id"`       // The event that carries monetary value

	// Window optionally restricts which events contribute value.
	// When nil, all events of this type are included.
	Window *TimeWindow `json:"window,omitempty" yaml:"window,omitempty"` // Optional time window restricting which events contribute value

	// Attributions optionally filters value events by their attributes
	// (e.g., only completed purchases: status = 'completed').
	Attributions []AttributeCondition `json:"attributions,omitempty" yaml:"attributions,omitempty"` // Optional filters on value event attributes

	// ValueColumn is the event attribute containing the monetary amount
	// (e.g., "amount", "revenue", "transaction_value").
	ValueColumn string `json:"value_column" yaml:"value_column"` // Event attribute containing the monetary amount

	// PerUserAgg defines how to aggregate the value column per user.
	// Typically AggSum (total per user). Must be one of "sum" or "avg".
	PerUserAgg AggFunction `json:"per_user_agg" yaml:"per_user_agg"` // How to aggregate per user — "sum" or "avg"

	// AcrossUsersAgg defines how to combine per-user values into the final metric.
	// Typically AggAvg (average CLV). Must be one of "sum" or "avg".
	// Defaults to "avg" when empty.
	AcrossUsersAgg AggFunction `json:"across_users_agg,omitempty" yaml:"across_users_agg,omitempty"` // How to combine per-user values — defaults to "avg"

	// SegmentFilter optionally restricts computation to users matching
	// segment criteria. When nil, CLV is computed over all users who
	// performed the value event.
	SegmentFilter *SegmentFilter `json:"segment_filter,omitempty" yaml:"segment_filter,omitempty"` // Optional segment to restrict which users are included
}

// SegmentFilter restricts a metric computation to users matching segment criteria.
// It reuses the Rule type from BehavioralSegmentation for full expressive power.
type SegmentFilter struct {
	Inclusion Rule  `json:"inclusion" yaml:"inclusion"`             // Inclusion criteria — users must match this rule
	Exclusion *Rule `json:"exclusion,omitempty" yaml:"exclusion,omitempty"` // Optional exclusion criteria
}
