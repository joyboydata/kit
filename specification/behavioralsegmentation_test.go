package specification

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSegmentJSONMarshalling(t *testing.T) {
	tests := []struct {
		name        string
		input       BehavioralSegmentation
		expectError bool
	}{
		{
			name: "basic inclusion with user actions and attributes",
			input: BehavioralSegmentation{
				UserbaseID: "userbase_123",
				Inclusion: Rule{
					Relation: "intersection",
					Slices: []Slice{
						{
							Type: "past_action",
							PastAction: &PastAction{
								Relation: "intersection",
								UserActions: []UserAction{
									{
										EventID: "evt_purchase_completed",
										Window: &TimeWindow{
											Operator: "in_the_last",
											Params:   []string{"90d"},
										},
										Occurrence: &Occurrence{
											Operator: ">=",
											Count:    3,
										},
										Attributions: []AttributeCondition{
											{Key: "device", Operator: "in", Value: []string{"ios", "android"}},
											{Key: "payment_method", Operator: "=", Value: []string{"credit_card"}},
										},
									},
								},
								UserAttributes: []AttributeCondition{
									{Key: "membership_tier", Operator: "=", Value: []string{"gold"}},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "inclusion and exclusion both defined",
			input: BehavioralSegmentation{
				UserbaseID: "userbase_456",
				Inclusion: Rule{
					Relation: "union",
					Slices: []Slice{
						{
							Type: "past_action",
							PastAction: &PastAction{
								Relation: "union",
								UserActions: []UserAction{
									{EventID: "evt_user_login"},
								},
							},
						},
					},
				},
				Exclusion: &Rule{
					Relation: "union",
					Slices: []Slice{
						{
							Type: "past_action",
							PastAction: &PastAction{
								Relation: "union",
								UserActions: []UserAction{
									{EventID: "evt_refund_requested"},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			var output BehavioralSegmentation
			err = json.Unmarshal(data, &output)
			assert.NoError(t, err)

			// Round-trip validation
			assert.Equal(t, tt.input.UserbaseID, output.UserbaseID)
			assert.Equal(t, tt.input.Inclusion.Relation, output.Inclusion.Relation)
			assert.Equal(t, len(tt.input.Inclusion.Slices), len(output.Inclusion.Slices))

			// Slice ordering
			for i := range tt.input.Inclusion.Slices {
				assert.Equal(t, tt.input.Inclusion.Slices[i].Type, output.Inclusion.Slices[i].Type)
			}

			if tt.input.Exclusion != nil {
				assert.NotNil(t, output.Exclusion)
				assert.Equal(t, tt.input.Exclusion.Relation, output.Exclusion.Relation)
			}
		})
	}
}

func TestAttributeConditionMarshalling(t *testing.T) {
	tests := []struct {
		name  string
		input AttributeCondition
	}{
		{
			name:  "single value",
			input: AttributeCondition{Key: "tier", Operator: "=", Value: []string{"gold"}},
		},
		{
			name:  "multiple values",
			input: AttributeCondition{Key: "device", Operator: "in", Value: []string{"ios", "android"}},
		},
		{
			name:  "empty values",
			input: AttributeCondition{Key: "country", Operator: "in", Value: []string{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			assert.NoError(t, err)

			var decoded AttributeCondition
			err = json.Unmarshal(data, &decoded)
			assert.NoError(t, err)

			assert.Equal(t, tt.input.Key, decoded.Key)
			assert.Equal(t, tt.input.Operator, decoded.Operator)
			assert.Equal(t, tt.input.Value, decoded.Value)
		})
	}
}

func TestSegmentYAMLMarshalling(t *testing.T) {
	tests := []struct {
		name        string
		input       BehavioralSegmentation
		expectError bool
	}{
		{
			name: "basic inclusion with user actions and attributes",
			input: BehavioralSegmentation{
				UserbaseID: "userbase_123",
				Inclusion: Rule{
					Relation: "intersection",
					Slices: []Slice{
						{
							Type: "past_action",
							PastAction: &PastAction{
								Relation: "intersection",
								UserActions: []UserAction{
									{
										EventID: "evt_purchase_completed",
										Window: &TimeWindow{
											Operator: "in_the_last",
											Params:   []string{"90d"},
										},
										Occurrence: &Occurrence{
											Operator: ">=",
											Count:    3,
										},
										Attributions: []AttributeCondition{
											{Key: "device", Operator: "in", Value: []string{"ios", "android"}},
											{Key: "payment_method", Operator: "=", Value: []string{"credit_card"}},
										},
									},
								},
								UserAttributes: []AttributeCondition{
									{Key: "membership_tier", Operator: "=", Value: []string{"gold"}},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "inclusion and exclusion both defined",
			input: BehavioralSegmentation{
				UserbaseID: "userbase_456",
				Inclusion: Rule{
					Relation: "union",
					Slices: []Slice{
						{
							Type: "past_action",
							PastAction: &PastAction{
								Relation: "union",
								UserActions: []UserAction{
									{EventID: "evt_user_login"},
								},
							},
						},
					},
				},
				Exclusion: &Rule{
					Relation: "union",
					Slices: []Slice{
						{
							Type: "past_action",
							PastAction: &PastAction{
								Relation: "union",
								UserActions: []UserAction{
									{EventID: "evt_refund_requested"},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := MarshalYAML(&tt.input)
			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, data)

			// Verify YAML output is valid by parsing it back
			output, err := UnmarshalYAML(data)
			require.NoError(t, err)
			require.NotNil(t, output)

			// Round-trip validation
			assert.Equal(t, tt.input.UserbaseID, output.UserbaseID)
			assert.Equal(t, tt.input.Inclusion.Relation, output.Inclusion.Relation)
			assert.Equal(t, len(tt.input.Inclusion.Slices), len(output.Inclusion.Slices))

			for i := range tt.input.Inclusion.Slices {
				assert.Equal(t, tt.input.Inclusion.Slices[i].Type, output.Inclusion.Slices[i].Type)
				if tt.input.Inclusion.Slices[i].PastAction != nil {
					require.NotNil(t, output.Inclusion.Slices[i].PastAction)
					assert.Equal(t, tt.input.Inclusion.Slices[i].PastAction.Relation, output.Inclusion.Slices[i].PastAction.Relation)
					assert.Equal(t, len(tt.input.Inclusion.Slices[i].PastAction.UserActions), len(output.Inclusion.Slices[i].PastAction.UserActions))
				}
			}

			if tt.input.Exclusion != nil {
				require.NotNil(t, output.Exclusion)
				assert.Equal(t, tt.input.Exclusion.Relation, output.Exclusion.Relation)
				assert.Equal(t, len(tt.input.Exclusion.Slices), len(output.Exclusion.Slices))
			} else {
				assert.Nil(t, output.Exclusion)
			}
		})
	}
}

func TestYAMLJSONRoundTrip(t *testing.T) {
	input := BehavioralSegmentation{
		UserbaseID: "userbase_789",
		Inclusion: Rule{
			Relation: "intersection",
			Slices: []Slice{
				{
					Type: "past_action",
					PastAction: &PastAction{
						Relation: "union",
						UserActions: []UserAction{
							{
								EventID: "evt_purchase_completed",
								Window: &TimeWindow{
									Operator: "between",
									Params:   []string{"2026-01-01", "2026-06-30"},
								},
								Occurrence: &Occurrence{
									Operator: ">",
									Count:    5,
								},
								Attributions: []AttributeCondition{
									{Key: "country", Operator: "not_in", Value: []string{"US", "CA"}},
								},
							},
						},
						UserAttributes: []AttributeCondition{
							{Key: "age", Operator: ">=", Value: []string{"21"}},
							{Key: "tier", Operator: "!=", Value: []string{"free"}},
						},
					},
				},
			},
		},
	}

	// Marshal to YAML
	yamlData, err := MarshalYAML(&input)
	require.NoError(t, err)
	require.NotEmpty(t, yamlData)

	// Unmarshal from YAML
	yamlOutput, err := UnmarshalYAML(yamlData)
	require.NoError(t, err)
	require.NotNil(t, yamlOutput)

	// Marshal YAML-unmarshalled result to JSON
	jsonFromYAML, err := json.Marshal(yamlOutput)
	require.NoError(t, err)

	// Marshal original to JSON
	jsonOriginal, err := json.Marshal(input)
	require.NoError(t, err)

	// Both JSON representations should be identical
	assert.JSONEq(t, string(jsonOriginal), string(jsonFromYAML))
}

func TestYAMLAttributeCondition(t *testing.T) {
	tests := []struct {
		name  string
		input AttributeCondition
	}{
		{name: "single value", input: AttributeCondition{Key: "tier", Operator: "=", Value: []string{"gold"}}},
		{name: "multiple values", input: AttributeCondition{Key: "device", Operator: "in", Value: []string{"ios", "android"}}},
		{name: "not equals", input: AttributeCondition{Key: "status", Operator: "!=", Value: []string{"banned"}}},
		{name: "not in", input: AttributeCondition{Key: "country", Operator: "not_in", Value: []string{"US", "CA", "MX"}}},
		{name: "empty values", input: AttributeCondition{Key: "country", Operator: "in", Value: []string{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := yaml.Marshal(tt.input)
			require.NoError(t, err)

			var decoded AttributeCondition
			err = yaml.Unmarshal(data, &decoded)
			require.NoError(t, err)

			assert.Equal(t, tt.input.Key, decoded.Key)
			assert.Equal(t, tt.input.Operator, decoded.Operator)
			assert.Equal(t, tt.input.Value, decoded.Value)
		})
	}
}

func TestYAMLExclusionOmitted(t *testing.T) {
	// When Exclusion is nil, it should be omitted from YAML output
	input := BehavioralSegmentation{
		UserbaseID: "userbase_001",
		Inclusion: Rule{
			Relation: "union",
			Slices: []Slice{
				{Type: "past_action", PastAction: &PastAction{Relation: "union", UserActions: []UserAction{{EventID: "evt_signup"}}}},
			},
		},
	}

	data, err := MarshalYAML(&input)
	require.NoError(t, err)

	// Unmarshal back and verify Exclusion is nil
	output, err := UnmarshalYAML(data)
	require.NoError(t, err)
	assert.Nil(t, output.Exclusion)
}

func TestYAMLMinimalSegment(t *testing.T) {
	input := BehavioralSegmentation{
		UserbaseID: "userbase_min",
		Inclusion: Rule{
			Relation: "union",
			Slices: []Slice{
				{Type: "past_action", PastAction: &PastAction{Relation: "union", UserActions: []UserAction{{EventID: "evt_visit"}}}},
			},
		},
	}

	data, err := MarshalYAML(&input)
	require.NoError(t, err)

	output, err := UnmarshalYAML(data)
	require.NoError(t, err)
	require.NotNil(t, output)

	assert.Equal(t, input.UserbaseID, output.UserbaseID)
	assert.Equal(t, input.Inclusion.Relation, output.Inclusion.Relation)
	assert.Len(t, output.Inclusion.Slices, 1)
	assert.Equal(t, "past_action", output.Inclusion.Slices[0].Type)
	assert.Equal(t, "evt_visit", output.Inclusion.Slices[0].PastAction.UserActions[0].EventID)
}

func TestYAMLInvalidData(t *testing.T) {
	_, err := UnmarshalYAML([]byte("invalid: [[[: not yaml"))
	assert.Error(t, err)
}

func TestYAMLOutputFormat(t *testing.T) {
	input := BehavioralSegmentation{
		UserbaseID: "userbase_fmt",
		Inclusion: Rule{
			Relation: "intersection",
			Slices: []Slice{
				{Type: "past_action", PastAction: &PastAction{Relation: "union", UserActions: []UserAction{{EventID: "evt_a"}, {EventID: "evt_b"}}}},
			},
		},
	}

	data, err := MarshalYAML(&input)
	require.NoError(t, err)

	// Verify the YAML output is well-formed (can be parsed back)
	output, err := UnmarshalYAML(data)
	require.NoError(t, err)
	require.NotNil(t, output)

	// Verify structure via JSON round-trip for deep equality
	jsonOrig, _ := json.Marshal(input)
	jsonOut, _ := json.Marshal(output)
	assert.JSONEq(t, string(jsonOrig), string(jsonOut))
}
