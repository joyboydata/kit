package specification

// ============================================================================
// Retention Spec — Metrics Engine Building Blocks
// ============================================================================

// +spec root Retention
//
// RetentionSpec defines a retention analysis: what fraction of a cohort
// performs a return action in subsequent time periods.
//
// It reuses existing primitives (UserAction, TimeWindow, AttributeCondition)
// but adds a time-series dimension — the cohort anchors T₀, and the schedule
// generates T₁, T₂, ... Tₙ as relative offsets from the cohort's window.
type RetentionSpec struct {
	UserbaseID string     `json:"userbase_id" yaml:"userbase_id"` // Identifies the userbase table
	Cohort     CohortDef  `json:"cohort" yaml:"cohort"`           // Defines who enters the cohort
	Return     ReturnDef  `json:"return" yaml:"return"`           // Defines what counts as returning
	Schedule   TimeSchedule `json:"schedule" yaml:"schedule"`     // Defines the measurement periods
}

// CohortDef defines who enters the cohort.
// It's a stripped-down UserAction: event + time window + optional attributions.
type CohortDef struct {
	EventID      string               `json:"event_id" yaml:"event_id"`                               // Event that triggers cohort entry
	Window       TimeWindow           `json:"window" yaml:"window"`                                   // Time window for the cohort event
	Attributions []AttributeCondition `json:"attributions,omitempty" yaml:"attributions,omitempty"`   // Optional filters on cohort event attributes
}

// ReturnDef defines what counts as "coming back."
// No Window here — return windows are derived from the schedule + cohort anchor.
// If SameEventAsCohort is true, the return event is the same as the cohort event,
// and EventID is ignored.
type ReturnDef struct {
	EventID           string               `json:"event_id,omitempty" yaml:"event_id,omitempty"`       // Return event ID (ignored if same_event_as_cohort is true)
	SameEventAsCohort bool                 `json:"same_event_as_cohort" yaml:"same_event_as_cohort"` // If true, the return event matches the cohort event
	Attributions      []AttributeCondition `json:"attributions,omitempty" yaml:"attributions,omitempty"` // Optional filters on return event attributes
}

// PeriodUnit defines the time granularity of each retention period.
type PeriodUnit string

const (
	PeriodUnitDay     PeriodUnit = "day"
	PeriodUnitWeek    PeriodUnit = "week"
	PeriodUnitMonth   PeriodUnit = "month"
	PeriodUnitQuarter PeriodUnit = "quarter"
)

// PeriodUnitDef describes a period unit and its metadata.
type PeriodUnitDef struct {
	Unit          PeriodUnit
	DisplayName   string
	Description   string
	SyntaxExample string
}

// PeriodUnitDefs is the canonical set of defined period units.
var PeriodUnitDefs = map[PeriodUnit]PeriodUnitDef{
	PeriodUnitDay: {
		Unit:          PeriodUnitDay,
		DisplayName:   "Day",
		Description:   "Daily periods",
		SyntaxExample: `"unit": "day"`,
	},
	PeriodUnitWeek: {
		Unit:          PeriodUnitWeek,
		DisplayName:   "Week",
		Description:   "Weekly periods",
		SyntaxExample: `"unit": "week"`,
	},
	PeriodUnitMonth: {
		Unit:          PeriodUnitMonth,
		DisplayName:   "Month",
		Description:   "Monthly periods",
		SyntaxExample: `"unit": "month"`,
	},
	PeriodUnitQuarter: {
		Unit:          PeriodUnitQuarter,
		DisplayName:   "Quarter",
		Description:   "Quarterly periods",
		SyntaxExample: `"unit": "quarter"`,
	},
}

// DisplayName returns a user-friendly label for the period unit.
func (p PeriodUnit) DisplayName() string {
	if d, ok := PeriodUnitDefs[p]; ok {
		return d.DisplayName
	}
	return ""
}

// Description returns a description of the period unit.
func (p PeriodUnit) Description() string {
	if d, ok := PeriodUnitDefs[p]; ok {
		return d.Description
	}
	return ""
}

// SyntaxExample returns an example JSON snippet for the period unit.
func (p PeriodUnit) SyntaxExample() string {
	if d, ok := PeriodUnitDefs[p]; ok {
		return d.SyntaxExample
	}
	return ""
}

// IsValid returns true if the period unit is defined.
func (p PeriodUnit) IsValid() bool {
	_, ok := PeriodUnitDefs[p]
	return ok
}

// AllPeriodUnits returns all defined period units in sorted order.
func AllPeriodUnits() []PeriodUnit {
	result := make([]PeriodUnit, 0, len(PeriodUnitDefs))
	for k := range PeriodUnitDefs {
		result = append(result, k)
	}
	return result
}

// TimeSchedule defines the periodic structure for measuring retention.
// The cohort's time window defines T₀ (period 0). Each subsequent period
// is a contiguous window of the same length, offset forward by Unit × Offset.
//
// For the user's example:
//
//	Unit: "month", Count: 1, OffsetFromCohortEnd: 1
//	Cohort window: 2026-01-01 to 2026-01-31
//	Period 1: 2026-02-01 to 2026-02-28
//
// For a 12-month retention curve:
//
//	Unit: "month", Count: 12, OffsetFromCohortEnd: 1
//	Periods: Feb, Mar, Apr, ... Jan of next year
type TimeSchedule struct {
	Unit  PeriodUnit `json:"unit" yaml:"unit"`   // Time granularity — "day", "week", "month", or "quarter"
	Count int        `json:"count" yaml:"count"` // Number of periods to measure

	// OffsetFromCohortEnd sets where period 1 starts.
	// 1 = first full period after the cohort window ends.
	// 0 = the cohort period itself (cohort retention — always 100%).
	OffsetFromCohortEnd int `json:"offset_from_cohort_end" yaml:"offset_from_cohort_end"` // Where period 1 starts relative to cohort end
}

// ============================================================================
// Retention Result
// ============================================================================

// RetentionResult is the output shape of a retention computation.
type RetentionResult struct {
	CohortCount int               `json:"cohort_count"`
	CohortLabel string            `json:"cohort_label"` // e.g., "2026-01"
	Periods     []RetentionPeriod `json:"periods"`
}

// RetentionPeriod is one point on the retention curve.
type RetentionPeriod struct {
	Label        string  `json:"label"`         // e.g., "M1", "2026-02"
	Users        int     `json:"users"`         // users who returned
	RetentionPct float64 `json:"retention_pct"` // 0.0 to 1.0
}
