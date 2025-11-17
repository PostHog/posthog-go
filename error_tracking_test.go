package posthog

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestException_Validate(t *testing.T) {
	now := time.Now()

	tests := map[string]struct {
		msg           Exception
		expectedError error
	}{
		"valid: minimal with one valid item": {
			msg: Exception{
				DistinctId: "user-123",
				Timestamp:  now,
				ExceptionList: []ExceptionItem{
					{Type: "title", Value: "desc"},
				},
			},
			expectedError: nil,
		},
		"error: missing DistinctId": {
			msg: Exception{
				Timestamp: now,
				ExceptionList: []ExceptionItem{
					{Type: "title", Value: "desc"},
				},
			},
			expectedError: FieldError{
				Type:  "posthog.Exception",
				Name:  "DistinctId",
				Value: "",
			},
		},
		"error: missing ExceptionList": {
			msg: Exception{
				DistinctId: "user-123",
				Timestamp:  now,
			},
			expectedError: FieldError{
				Type:  "posthog.Exception",
				Name:  "ExceptionList",
				Value: []ExceptionItem{},
			},
		},
		"error: item missing Type": {
			msg: Exception{
				DistinctId: "user-123",
				Timestamp:  now,
				ExceptionList: []ExceptionItem{
					{Type: "", Value: "Bar"},
				},
			},
			expectedError: FieldError{
				Type:  "posthog.Exception",
				Name:  "Type",
				Value: "",
			},
		},
		"error: item missing Value": {
			msg: Exception{
				DistinctId: "user-123",
				Timestamp:  now,
				ExceptionList: []ExceptionItem{
					{Type: "Foo", Value: ""},
				},
			},
			expectedError: FieldError{
				Type:  "posthog.Exception",
				Name:  "Value",
				Value: "",
			},
		},
		"valid: full nested structure": {
			msg: Exception{
				DistinctId: "user-123",
				Timestamp:  now,
				ExceptionList: []ExceptionItem{
					{
						Type:  "MyError",
						Value: "something went wrong",
						Mechanism: &ExceptionMechanism{
							Handled:   ptrBool(true),
							Synthetic: ptrBool(false),
						},
						Stacktrace: &ExceptionStacktrace{
							Type: "raw",
							Frames: []StackFrame{
								{
									Filename:  "file.go",
									LineNo:    42,
									Function:  "package.Sample",
									InApp:     true,
									Synthetic: false,
									Platform:  "go",
								},
							},
						},
					},
				},
			},
			expectedError: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.msg.Validate()
			if !reflect.DeepEqual(err, tc.expectedError) {
				t.Errorf("expected error:\n%v \ngot:\n%v", tc.expectedError, err)
			}
		})
	}
}

func TestException_APIfy_WithCustomProperties(t *testing.T) {
	now := time.Now()
	exList := []ExceptionItem{{Type: "Error", Value: "Something went wrong"}}

	tests := map[string]struct {
		exception Exception
		expected  map[string]interface{}
	}{
		"basic without custom properties": {
			exception: Exception{DistinctId: "user-123", Timestamp: now, ExceptionList: exList},
			expected: map[string]interface{}{
				"$lib":            SDKName,
				"$lib_version":    getVersion(),
				"distinct_id":     "user-123",
				"$exception_list": exList,
			},
		},
		"with custom properties and fingerprint": {
			exception: Exception{
				DistinctId:           "user-123",
				Timestamp:            now,
				ExceptionFingerprint: ptrString("custom-fingerprint"),
				Properties:           NewProperties().Set("environment", "production").Set("retry_count", 3),
				ExceptionList:        exList,
			},
			expected: map[string]interface{}{
				"$lib":                   SDKName,
				"$lib_version":           getVersion(),
				"distinct_id":            "user-123",
				"$exception_list":        exList,
				"$exception_fingerprint": ptrString("custom-fingerprint"),
				"environment":            "production",
				"retry_count":            3,
			},
		},
		"custom properties override system properties": {
			exception: Exception{
				DistinctId:    "user-123",
				Timestamp:     now,
				Properties:    NewProperties().Set("$lib", "custom-lib").Set("distinct_id", "custom-id"),
				ExceptionList: exList,
			},
			expected: map[string]interface{}{
				"$lib":            "custom-lib",
				"$lib_version":    getVersion(),
				"distinct_id":     "custom-id",
				"$exception_list": exList,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			apiMsg, ok := tc.exception.APIfy().(ExceptionInApi)
			if !ok {
				t.Fatalf("expected ExceptionInApi, got %T", tc.exception.APIfy())
			}

			if !reflect.DeepEqual(apiMsg.Properties, Properties(tc.expected)) {
				t.Errorf("properties mismatch\nexpected: %+v\ngot: %+v", tc.expected, apiMsg.Properties)
			}
		})
	}
}

func TestNewDefaultException_WithProperties(t *testing.T) {
	now := time.Now()

	tests := map[string]struct {
		properties       []Properties
		expectProperties bool
		expectedProps    Properties
	}{
		"without properties (backward compatible)": {
			properties:       nil,
			expectProperties: false,
		},
		"with properties": {
			properties: []Properties{
				NewProperties().
					Set("environment", "production").
					Set("custom_key", "custom_value"),
			},
			expectProperties: true,
			expectedProps: NewProperties().
				Set("environment", "production").
				Set("custom_key", "custom_value"),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var exception Exception
			if tc.properties != nil {
				exception = NewDefaultException(now, "user-123", "Error", "Description", tc.properties...)
			} else {
				exception = NewDefaultException(now, "user-123", "Error", "Description")
			}

			if tc.expectProperties {
				if exception.Properties == nil {
					t.Error("expected Properties to be set")
				} else if !reflect.DeepEqual(exception.Properties, tc.expectedProps) {
					t.Errorf("properties mismatch\nexpected: %+v\ngot: %+v",
						tc.expectedProps, exception.Properties)
				}
			} else {
				if exception.Properties != nil {
					t.Errorf("expected Properties to be nil, got %+v", exception.Properties)
				}
			}
		})
	}
}

func ptrString(s string) *string {
	return &s
}

// marshalAndParseJSON is a helper that marshals an exception to JSON and returns the parsed structure
func marshalAndParseJSON(t *testing.T, exception Exception) (result map[string]interface{}, props map[string]interface{}) {
	t.Helper()

	jsonBytes, err := json.Marshal(exception.APIfy())
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	props, ok := result["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties field missing or wrong type")
	}

	return result, props
}

func TestException_JSONSerialization(t *testing.T) {
	fingerprint := "custom-fingerprint-123"
	exception := Exception{
		Type:                 "exception",
		DistinctId:           "user-123",
		Timestamp:            time.Date(2024, 11, 17, 10, 30, 0, 0, time.UTC),
		Properties:           NewProperties().Set("environment", "production").Set("custom_key", "custom_value"),
		DisableGeoIP:         true,
		ExceptionFingerprint: &fingerprint,
		ExceptionList:        []ExceptionItem{{Type: "RuntimeError", Value: "Database connection failed"}},
	}

	result, props := marshalAndParseJSON(t, exception)

	tests := []struct {
		obj      map[string]interface{}
		field    string
		expected interface{}
	}{
		{result, "type", "exception"},
		{result, "event", "$exception"},
		{result, "library", SDKName},
		{result, "library_version", getVersion()},
		{props, "$lib", SDKName},
		{props, "$lib_version", getVersion()},
		{props, "distinct_id", "user-123"},
		{props, "$geoip_disable", true},
		{props, "$exception_fingerprint", "custom-fingerprint-123"},
		{props, "environment", "production"},
		{props, "custom_key", "custom_value"},
	}

	for _, tc := range tests {
		if tc.obj[tc.field] != tc.expected {
			t.Errorf("%s: expected %v, got %v", tc.field, tc.expected, tc.obj[tc.field])
		}
	}

	exList, ok := props["$exception_list"].([]interface{})
	if !ok {
		t.Errorf("$exception_list: expected []interface{}, got %T", props["$exception_list"])
	}
	if len(exList) == 0 {
		t.Errorf("$exception_list: expected non-empty list, got empty")
	}
}
