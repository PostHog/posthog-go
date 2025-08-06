package posthog

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestCalculateHash(t *testing.T) {
	for _, tt := range []struct {
		ident string
		want  float64
	}{
		{"some_distinct_id", 0.7270002403585725},
		{"test-identifier", 0.4493881716040236},
		{"example_id", 0.9402003475831224},
		{"example_id2", 0.6292740389966519},
	} {
		t.Run(tt.ident, func(t *testing.T) {
			got := calculateHash("holdout-", tt.ident, "")
			if math.Abs(got-tt.want) > 0.000001 {
				t.Logf("got: %.16f, want: %f", got, tt.want)
				t.Fail()
			}
		})
	}
}

func TestArgumentOrderableConversion(t *testing.T) {
	for _, tt := range []struct {
		input    interface{}
		expected float64
		error    bool
	}{
		{42, 42.0, false},
		{3.14, 3.14, false},
		{"123.456", 123.456, false},
		{"not_a_number", 0.0, true}, // This should fail
	} {
		t.Run(fmt.Sprintf("Converting %v...", tt.input), func(t *testing.T) {
			result, err := interfaceToFloat(tt.input)
			if tt.error != (err != nil) {
				t.Errorf("Expected error: %v, got: %v", tt.error, err)
			}
			if !tt.error && result != tt.expected {
				t.Errorf("Expected: %f, got: %f", tt.expected, result)
			}
		})
	}
}

func TestArgumentDateTimeConversion(t *testing.T) {
	now := time.Now().UTC()
	for _, tt := range []struct {
		input    interface{}
		expected time.Time
		error    bool
	}{
		{now, now, false},
		{"2024-02-01T01:33:00Z", time.Date(2024, 2, 1, 1, 33, 0, 0, time.UTC), false},
		{"2024-02-01", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), false},
		{"none", time.Time{}, true},
	} {
		t.Run(fmt.Sprintf("Converting %v...", tt.input), func(t *testing.T) {
			result, err := convertToDateTime(tt.input)
			if tt.error != (err != nil) {
				t.Errorf("Expected error: %v, got: %v", tt.error, err)
			}
			if !tt.error && result != tt.expected {
				t.Errorf("Expected: %v, got: %v", tt.expected, result)
			}
		})
	}
}

func TestMatchPropertyDate(t *testing.T) {
	shouldMatchA := []interface{}{"2022-04-30T00:00:00+00:00", "2022-04-30T00:00:00+02:00", time.Date(2022, 3, 20, 20, 34, 58, 651387237, time.UTC)}
	shouldNotMatchA := NewProperties().Set("key", "2022-05-30T00:00:00+00:00")
	propertyA := FlagProperty{
		Key:      "key",
		Value:    "2022-05-01",
		Operator: "is_date_before",
		Type:     "person",
	}
	for _, val := range shouldMatchA {
		isMatch, err := matchProperty(propertyA, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if !isMatch {
			t.Error(("Value is not a match"))
		}
	}
	isMatchA, errA := matchProperty(propertyA, shouldNotMatchA)
	if errA != nil {
		t.Error(errA)
	}
	if isMatchA {
		t.Error("Value is not a match")
	}

	shouldMatchB := []interface{}{"2022-05-30T00:00:00+00:00", time.Date(2022, 5, 30, 20, 34, 58, 651387237, time.UTC)}
	shouldNotMatchB := NewProperties().Set("key", "2022-04-29T00:00:00+00:00")
	propertyB := FlagProperty{
		Key:      "key",
		Value:    "2022-05-01T00:00:00+00:00",
		Operator: "is_date_after",
		Type:     "person",
	}
	for _, val := range shouldMatchB {
		isMatch, err := matchProperty(propertyB, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if !isMatch {
			t.Error(("Value is not a match"))
		}
	}
	isMatchB, errB := matchProperty(propertyB, shouldNotMatchB)
	if errB != nil {
		t.Error(errB)
	}
	if isMatchB {
		t.Error("Value is not a match")
	}
}
