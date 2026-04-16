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

	fingerprintStr := "custom-fingerprint"

	tests := map[string]struct {
		exception       Exception
		expectKeys      map[string]interface{}
		forbiddenValues []string
	}{
		"basic without custom properties": {
			exception: Exception{DistinctId: "user-123", Timestamp: now, ExceptionList: exList},
			expectKeys: map[string]interface{}{
				"$lib":         SDKName,
				"$lib_version": getVersion(),
				"distinct_id":  "user-123",
			},
		},
		"with custom properties and fingerprint": {
			exception: Exception{
				DistinctId:           "user-123",
				Timestamp:            now,
				ExceptionFingerprint: &fingerprintStr,
				Properties:           NewProperties().Set("environment", "production").Set("retry_count", 3),
				ExceptionList:        exList,
			},
			expectKeys: map[string]interface{}{
				"$lib":                   SDKName,
				"$lib_version":           getVersion(),
				"distinct_id":            "user-123",
				"$exception_fingerprint": "custom-fingerprint",
				"environment":            "production",
				"retry_count":            float64(3),
			},
		},
		"typed fields win on collision with custom": {
			exception: Exception{
				DistinctId:    "user-123",
				Timestamp:     now,
				Properties:    NewProperties().Set("$lib", "custom-lib").Set("distinct_id", "custom-id"),
				ExceptionList: exList,
			},
			expectKeys: map[string]interface{}{
				"$lib":        SDKName,
				"distinct_id": "user-123",
			},
			forbiddenValues: []string{"custom-lib", "custom-id"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			apiMsg, ok := tc.exception.APIfy().(ExceptionInApi)
			if !ok {
				t.Fatalf("expected ExceptionInApi, got %T", tc.exception.APIfy())
			}

			jsonBytes, err := json.Marshal(apiMsg)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			var wire map[string]interface{}
			if err := json.Unmarshal(jsonBytes, &wire); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			props, ok := wire["properties"].(map[string]interface{})
			if !ok {
				t.Fatalf("properties field missing or wrong type")
			}

			for k, want := range tc.expectKeys {
				if got := props[k]; !reflect.DeepEqual(got, want) {
					t.Errorf("property %q: expected %v (%T), got %v (%T)", k, want, want, got, got)
				}
			}

			for _, forbidden := range tc.forbiddenValues {
				for k, v := range props {
					if s, ok := v.(string); ok && s == forbidden {
						t.Errorf("forbidden value %q leaked into property %q", forbidden, k)
					}
				}
			}
		})
	}
}

func TestException_JSONSerialization(t *testing.T) {
	fingerprint := "custom-fingerprint-123"
	exception := Exception{
		Type:                 "exception",
		Uuid:                 "01963f1a-3c12-7b21-8a1d-3f6d1cf49b8e",
		DistinctId:           "user-123",
		Timestamp:            time.Date(2024, 11, 17, 10, 30, 0, 0, time.UTC),
		Properties:           NewProperties().Set("environment", "production").Set("custom_key", "custom_value"),
		DisableGeoIP:         true,
		ExceptionFingerprint: &fingerprint,
		ExceptionList:        []ExceptionItem{{Type: "RuntimeError", Value: "Database connection failed"}},
	}

	jsonBytes, err := json.Marshal(exception.APIfy())
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	props, ok := result["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties field missing or wrong type")
	}

	tests := []struct {
		obj      map[string]interface{}
		field    string
		expected interface{}
	}{
		{result, "type", "exception"},
		{result, "uuid", "01963f1a-3c12-7b21-8a1d-3f6d1cf49b8e"},
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
