package posthog

import (
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
		"error: frame missing RawID": {
			msg: Exception{
				DistinctId: "user-123",
				Timestamp:  now,
				ExceptionList: []ExceptionItem{
					{
						Type:  "t",
						Value: "v",
						Stacktrace: &ExceptionStacktrace{
							Type: "raw",
							Frames: []StackFrame{
								{RawID: "", MangledName: "fn", ResolvedName: "fn", Language: "go", Source: "file.go", Line: 1},
							},
						},
					},
				},
			},
			expectedError: FieldError{
				Type:  "posthog.ExceptionItem",
				Name:  "Stacktrace.Frame",
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
							Type: "resolved",
							Frames: []StackFrame{
								{
									RawID:        "abc123",
									MangledName:  "fn",
									InApp:        ptrBool(true),
									ResolvedName: "fn",
									Language:     "go",
									Resolved:     ptrBool(true),
									Source:       "file.go",
									Line:         42,
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
