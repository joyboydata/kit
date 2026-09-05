package specification

// ============================================================================
// Canonical Examples
//
// These examples are the single source of truth for spec documentation.
// They are extracted by the genspecdoc codegen tool and rendered in the
// specification documentation pages.
//
// Convention: the first line of the godoc comment is the example title.
// Subsequent lines form the example description.
// ============================================================================

// Gold-tier users who purchased at least 3 times in 90 days
//
// Segment of gold-tier users who completed at least 3 purchases via credit
// card on iOS or Android in the last 90 days.
var SegmentExampleGoldTier = `{
  "userbase_id": "ecommerce_users",
  "inclusion": {
    "relation": "intersection",
    "slices": [
      {
        "type": "past_action",
        "past_action": {
          "relation": "intersection",
          "user_actions": [
            {
              "event_id": "evt_purchase_completed",
              "window": {
                "operator": "in_the_last",
                "params": ["90d"]
              },
              "occurrence": {
                "operator": ">=",
                "count": 3
              },
              "attributions": [
                { "key": "device", "operator": "in", "value": ["ios", "android"] },
                { "key": "payment_method", "operator": "=", "value": ["credit_card"] }
              ]
            }
          ],
          "user_attributes": [
            { "key": "membership_tier", "operator": "=", "value": ["gold"] }
          ]
        }
      }
    ]
  }
}`

// Logged-in users excluding refunders
//
// Segment of users who logged in, excluding anyone who requested a refund.
var SegmentExampleLoggedInExcludingRefunders = `{
  "userbase_id": "ecommerce_users",
  "inclusion": {
    "relation": "union",
    "slices": [
      {
        "type": "past_action",
        "past_action": {
          "relation": "union",
          "user_actions": [
            { "event_id": "evt_user_login" }
          ]
        }
      }
    ]
  },
  "exclusion": {
    "relation": "union",
    "slices": [
      {
        "type": "past_action",
        "past_action": {
          "relation": "union",
          "user_actions": [
            { "event_id": "evt_refund_requested" }
          ]
        }
      }
    ]
  }
}`

// Purchase-based CLV with segment filter
//
// Average CLV of gold-tier users who purchased at least 3 times in the last
// year, using purchase amount as the value metric.
var CLVExamplePurchaseBased = `{
  "userbase_id": "ecommerce_users",
  "event_id": "evt_purchase",
  "window": {
    "operator": "in_the_last",
    "params": ["365d"]
  },
  "value_column": "amount",
  "per_user_agg": "sum",
  "across_users_agg": "avg",
  "segment_filter": {
    "inclusion": {
      "relation": "intersection",
      "slices": [
        {
          "type": "past_action",
          "past_action": {
            "relation": "union",
            "user_actions": [
              {
                "event_id": "evt_purchase",
                "window": {
                  "operator": "in_the_last",
                  "params": ["365d"]
                },
                "occurrence": {
                  "operator": ">=",
                  "count": 3
                }
              }
            ],
            "user_attributes": [
              { "key": "tier", "operator": "=", "value": ["gold"] }
            ]
          }
        }
      ]
    }
  }
}`

// Monthly retention of Q1 first-purchase users
//
// Retention analysis of users who made their first purchase between January
// and March 2026, measuring how many return for another purchase each month
// over 6 months.
var RetentionExampleMonthly = `{
  "userbase_id": "ecommerce_users",
  "cohort": {
    "event_id": "evt_first_purchase",
    "window": {
      "operator": "between",
      "params": ["2026-01-01", "2026-03-31"]
    },
    "attributions": [
      { "key": "channel", "operator": "=", "value": ["organic"] }
    ]
  },
  "return": {
    "same_event_as_cohort": true,
    "attributions": []
  },
  "schedule": {
    "unit": "month",
    "count": 6,
    "offset_from_cohort_end": 1
  }
}`
