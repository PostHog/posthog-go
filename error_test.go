package posthog

import "testing"

func TestErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "config",
			err: ConfigError{
				Reason: "testing",
				Field:  "Answer",
				Value:  42,
			},
			want: "posthog.NewWithConfig: testing (posthog.Config.Answer: 42)",
		},
		{
			name: "field",
			err: FieldError{
				Type:  "testing.T",
				Name:  "Answer",
				Value: 42,
			},
			want: "testing.T.Answer: invalid field value: 42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Error("invalid error message returned:", got)
			}
		})
	}
}
