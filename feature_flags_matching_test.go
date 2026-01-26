package posthog

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMatchPropertyValue(t *testing.T) {
	property := FlagProperty{
		Key:      "Browser",
		Value:    "Chrome",
		Operator: "exact",
	}

	properties := NewProperties().Set("Browser", "Chrome")

	isMatch, err := matchProperty(property, properties)

	if err != nil || !isMatch {
		t.Error("Value is not a match")
	}

}

func TestMatchPropertyInvalidOperator(t *testing.T) {
	property := FlagProperty{
		Key:      "Browser",
		Value:    "Chrome",
		Operator: "is_unknown",
	}

	properties := NewProperties().Set("Browser", "Chrome")

	isMatch, err := matchProperty(property, properties)

	if isMatch == true {
		t.Error("Should not match")
	}

	var inconclusiveErr *InconclusiveMatchError
	if !errors.As(err, &inconclusiveErr) {
		t.Error("Error type is not a match")
	}

}

func TestMatchPropertySlice(t *testing.T) {
	property := FlagProperty{
		Key:      "Browser",
		Value:    []interface{}{"Chrome", "Firefox"},
		Operator: "exact",
	}

	for _, tt := range []struct {
		name       string
		properties Properties
		expected   bool
		err        error
	}{
		{
			name:       "match with Chrome",
			properties: NewProperties().Set("Browser", "Chrome"),
			expected:   true,
		},
		{
			name:       "match with Firefox",
			properties: NewProperties().Set("Browser", "Firefox"),
			expected:   true,
		},
		{
			name:       "no match with Explorer",
			properties: NewProperties().Set("Browser", "Explorer"),
		},
		{
			name:       "no match with unknown key",
			properties: NewProperties().Set("Car", "Chrome"),
			err:        errors.New(""),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isMatch, err := matchProperty(property, tt.properties)
			if tt.err != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.expected, isMatch)
		})
	}
}

func TestMatchPropertySliceExact(t *testing.T) {
	property := FlagProperty{
		Key:      "Browser",
		Value:    []interface{}{"Chrome", "Firefox"},
		Operator: "exact",
	}

	isMatch, err := matchProperty(property, NewProperties().Set("Browser", "Chrome"))
	require.NoError(t, err)
	require.True(t, isMatch)

	isMatch, err = matchProperty(property, NewProperties().Set("Browser", "Firefox"))
	require.NoError(t, err)
	require.True(t, isMatch)

	isMatch, err = matchProperty(property, NewProperties().Set("Browser", "Explorer"))
	require.NoError(t, err)
	require.False(t, isMatch)

	isMatch, err = matchProperty(property, NewProperties().Set("Car", "Fiat"))
	require.Error(t, err)
	require.False(t, isMatch)
}

func TestMatchPropertyNumber(t *testing.T) {
	property := FlagProperty{
		Key:      "Number",
		Value:    5,
		Operator: "gt",
	}

	properties := NewProperties().Set("Number", 7)

	isMatch, err := matchProperty(property, properties)

	if err != nil {
		t.Error(err)
	}

	if !isMatch {
		t.Error("Value is not a match")
	}

	property = FlagProperty{
		Key:      "Number",
		Value:    5,
		Operator: "lt",
	}

	properties = NewProperties().Set("Number", 4)

	isMatch, err = matchProperty(property, properties)

	if err != nil {
		t.Error(err)
	}

	if !isMatch {
		t.Error("Value is not a match")
	}

	property = FlagProperty{
		Key:      "Number",
		Value:    5,
		Operator: "gte",
	}

	properties = NewProperties().Set("Number", 5)

	isMatch, err = matchProperty(property, properties)

	if err != nil {
		t.Error(err)
	}

	if !isMatch {
		t.Error("Value is not a match")
	}

	property = FlagProperty{
		Key:      "Number",
		Value:    5,
		Operator: "lte",
	}

	properties = NewProperties().Set("Number", 4)

	isMatch, err = matchProperty(property, properties)

	if err != nil {
		t.Error(err)
	}

	if !isMatch {
		t.Error("Value is not a match")
	}
}

func TestMatchPropertyRegex(t *testing.T) {

	shouldMatch := []interface{}{"value.com", "value2.com"}

	property := FlagProperty{
		Key:      "key",
		Value:    "\\.com$",
		Operator: "regex",
	}

	for _, val := range shouldMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if !isMatch {
			t.Error("Value is not a match")
		}
	}

	shouldNotMatch := []interface{}{".com343tfvalue5", "Alakazam", 123}

	for _, val := range shouldNotMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if isMatch {
			t.Error("Value is not a match")
		}
	}

	// invalid regex
	property = FlagProperty{
		Key:      "key",
		Value:    "?*",
		Operator: "regex",
	}

	shouldNotMatch = []interface{}{"value", "valu2"}
	for _, val := range shouldNotMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if isMatch {
			t.Error("Value is not a match")
		}
	}

	// non string value

	property = FlagProperty{
		Key:      "key",
		Value:    4,
		Operator: "regex",
	}

	shouldMatch = []interface{}{"4", 4}
	for _, val := range shouldMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if !isMatch {
			t.Error("Value is not a match")
		}
	}
}

func TestMatchPropertyContains(t *testing.T) {
	shouldMatch := []interface{}{"value", "value2", "value3", "value4", "343tfvalue5"}

	property := FlagProperty{
		Key:      "key",
		Value:    "valUe",
		Operator: "icontains",
	}

	for _, val := range shouldMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if !isMatch {
			t.Error("Value is not a match")
		}
	}

	shouldNotMatch := []interface{}{"Alakazam", 123}

	for _, val := range shouldNotMatch {
		isMatch, err := matchProperty(property, NewProperties().Set("key", val))
		if err != nil {
			t.Error(err)
		}

		if isMatch {
			t.Error("Value is not a match")
		}
	}
}

func TestMatchPropertyDateComparison(t *testing.T) {
	t.Run("RFC3339 dates", func(t *testing.T) {
		// Test is_date_before
		property := FlagProperty{
			Key:      "created_at",
			Value:    "2024-12-31T23:59:59Z",
			Operator: "is_date_before",
		}

		// Should match: 2024-06-15 is before 2024-12-31
		isMatch, err := matchProperty(property, NewProperties().Set("created_at", "2024-06-15T10:00:00Z"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Should not match: 2025-01-01 is after 2024-12-31
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2025-01-01T00:00:00Z"))
		require.NoError(t, err)
		require.False(t, isMatch)

		// Test is_date_after
		property.Operator = "is_date_after"

		// Should match: 2025-01-01 is after 2024-12-31
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2025-01-01T00:00:00Z"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Should not match: 2024-06-15 is before 2024-12-31
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2024-06-15T10:00:00Z"))
		require.NoError(t, err)
		require.False(t, isMatch)
	})

	t.Run("ISO 8601 date formats", func(t *testing.T) {
		// Test date-only format (YYYY-MM-DD)
		property := FlagProperty{
			Key:      "created_at",
			Value:    "2024-12-31",
			Operator: "is_date_before",
		}

		// Should match: 2024-06-15 is before 2024-12-31
		isMatch, err := matchProperty(property, NewProperties().Set("created_at", "2024-06-15"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Should not match: 2025-01-01 is after 2024-12-31
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2025-01-01"))
		require.NoError(t, err)
		require.False(t, isMatch)

		// Test datetime without timezone (YYYY-MM-DDTHH:MM:SS)
		property.Value = "2024-12-31T23:59:59"

		// Should match: 2024-06-15T10:00:00 is before 2024-12-31T23:59:59
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2024-06-15T10:00:00"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Should not match: 2025-01-01T00:00:00 is after 2024-12-31T23:59:59
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2025-01-01T00:00:00"))
		require.NoError(t, err)
		require.False(t, isMatch)

		// Test is_date_after with date-only format
		property.Operator = "is_date_after"
		property.Value = "2024-06-15"

		// Should match: 2025-01-01 is after 2024-06-15
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2025-01-01"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Should not match: 2024-01-01 is before 2024-06-15
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2024-01-01"))
		require.NoError(t, err)
		require.False(t, isMatch)

		// Test mixing formats (RFC3339 value with date-only property)
		property.Value = "2024-06-15"
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2025-01-01T00:00:00Z"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Test mixing formats (date-only value with datetime property)
		property.Value = "2024-06-15T12:00:00"
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2025-01-01"))
		require.NoError(t, err)
		require.True(t, isMatch)
	})

	t.Run("ISO 8601 fractional seconds and timezone offsets", func(t *testing.T) {
		// Test fractional seconds with Z timezone
		property := FlagProperty{
			Key:      "created_at",
			Value:    "2024-12-31T23:59:59.999Z",
			Operator: "is_date_before",
		}

		// Should match: earlier fractional time
		isMatch, err := matchProperty(property, NewProperties().Set("created_at", "2024-06-15T10:30:00.123Z"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Should not match: later time
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2025-01-01T00:00:00.001Z"))
		require.NoError(t, err)
		require.False(t, isMatch)

		// Test timezone offsets (positive)
		property.Value = "2024-06-15T10:00:00+05:30"

		// Should match: earlier time with different timezone
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2024-06-15T03:00:00Z"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Should not match: later time
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2024-06-15T12:00:00+05:30"))
		require.NoError(t, err)
		require.False(t, isMatch)

		// Test timezone offsets (negative)
		property.Value = "2024-06-15T10:00:00-08:00"

		// Should match: earlier time
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2024-06-15T08:00:00-08:00"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Test combined fractional seconds + timezone offset
		property.Value = "2024-12-31T23:59:59.999+01:00"

		// Should match: earlier fractional time with timezone
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2024-12-31T22:59:59.998+01:00"))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Should not match: later time
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2025-01-01T00:00:00.000+01:00"))
		require.NoError(t, err)
		require.False(t, isMatch)

		// Test various fractional second precisions
		testCases := []struct {
			name     string
			dateStr  string
			expected bool
		}{
			{"1 digit fractional", "2024-01-15T10:30:00.1Z", true},
			{"2 digit fractional", "2024-01-15T10:30:00.12Z", true},
			{"3 digit fractional", "2024-01-15T10:30:00.123Z", true},
			{"6 digit fractional", "2024-01-15T10:30:00.123456Z", true},
			{"9 digit fractional", "2024-01-15T10:30:00.123456789Z", true},
			{"fractional with +00:00", "2024-01-15T10:30:00.123+00:00", true},
		}

		property.Operator = "is_date_after"
		property.Value = "2024-01-01T00:00:00Z"

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				isMatch, err := matchProperty(property, NewProperties().Set("created_at", tc.dateStr))
				require.NoError(t, err)
				require.Equal(t, tc.expected, isMatch, "Failed for date: %s", tc.dateStr)
			})
		}

		// Test mixing fractional seconds with non-fractional
		property.Value = "2024-06-15T10:30:00Z"
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2024-06-15T10:30:00.999Z"))
		require.NoError(t, err)
		require.True(t, isMatch) // .999 is after :00 (operator is still is_date_after)

		// Test +00:00 equivalent to Z
		property.Value = "2024-06-15T10:30:00+00:00"
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "2024-06-15T10:30:00Z"))
		require.NoError(t, err)
		require.False(t, isMatch) // Equal times, not after
	})

	t.Run("Relative dates", func(t *testing.T) {
		now := time.Now()

		// Test is_date_after with relative date
		property := FlagProperty{
			Key:      "created_at",
			Value:    "-7d", // 7 days ago
			Operator: "is_date_after",
		}

		// Should match: 3 days ago is after 7 days ago
		threeDaysAgo := now.AddDate(0, 0, -3).Format(time.RFC3339)
		isMatch, err := matchProperty(property, NewProperties().Set("created_at", threeDaysAgo))
		require.NoError(t, err)
		require.True(t, isMatch)

		// Should not match: 10 days ago is before 7 days ago
		tenDaysAgo := now.AddDate(0, 0, -10).Format(time.RFC3339)
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", tenDaysAgo))
		require.NoError(t, err)
		require.False(t, isMatch)

		// Test is_date_before with relative date
		property.Operator = "is_date_before"

		// Should match: 10 days ago is before 7 days ago
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", tenDaysAgo))
		require.NoError(t, err)
		require.True(t, isMatch)
	})

	t.Run("Various relative date formats", func(t *testing.T) {
		testCases := []struct {
			name         string
			relativeDate string
			shouldParse  bool
		}{
			{"1 hour", "1h", true},
			{"7 days", "7d", true},
			{"2 weeks", "2w", true},
			{"3 months", "3m", true},
			{"1 year", "1y", true},
			{"large number rejected", "10000d", false},
			{"invalid format", "invalid", false},
			{"no number", "d", false},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := parseRelativeDate(tc.relativeDate)
				if tc.shouldParse {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	})

	t.Run("Error handling", func(t *testing.T) {
		property := FlagProperty{
			Key:      "created_at",
			Value:    "2024-12-31T23:59:59Z",
			Operator: "is_date_before",
		}

		// Test with non-string value
		isMatch, err := matchProperty(property, NewProperties().Set("created_at", 12345))
		require.Error(t, err)
		require.False(t, isMatch)

		// Test with invalid date format
		isMatch, err = matchProperty(property, NewProperties().Set("created_at", "invalid-date"))
		require.Error(t, err)
		require.False(t, isMatch)
	})
}
