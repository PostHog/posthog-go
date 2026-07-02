package posthog

import (
	"errors"
	"fmt"
)

// ConfigError is returned by NewWithConfig when a Config field has an invalid value.
type ConfigError struct {

	// A human-readable message explaining why the configuration field's value
	// is invalid.
	Reason string

	// The name of the configuration field that was carrying an invalid value.
	Field string

	// The value of the configuration field that caused the error.
	Value interface{}
}

// Error returns a human-readable configuration error message.
func (e ConfigError) Error() string {
	return fmt.Sprintf("posthog.NewWithConfig: %s (posthog.Config.%s: %#v)", e.Reason, e.Field, e.Value)
}

// FieldError describes an invalid required field in a message passed to the SDK.
type FieldError struct {

	// The human-readable representation of the type of structure that wasn't
	// initialized properly.
	Type string

	// The name of the field that wasn't properly initialized.
	Name string

	// The value of the field that wasn't properly initialized.
	Value interface{}
}

// Error returns a human-readable field validation error message.
func (e FieldError) Error() string {
	return fmt.Sprintf("%s.%s: invalid field value: %#v", e.Type, e.Name, e.Value)
}

type requiredStringField struct {
	name  string
	value string
}

func validateRequiredStringFields(typeName string, fields ...requiredStringField) error {
	for _, field := range fields {
		if field.value == "" {
			return FieldError{
				Type:  typeName,
				Name:  field.name,
				Value: field.value,
			}
		}
	}
	return nil
}

var (
	// This error is returned by methods of the `Client` interface when they are
	// called after the client was already closed.
	ErrClosed = errors.New("the client was already closed")

	// This error is used to notify the application that too many requests are
	// already being sent and no more messages can be accepted.
	ErrTooManyRequests = errors.New("too many requests are already in-flight")

	// This error is used to notify the client callbacks that a message send
	// failed because the JSON representation of a message exceeded the upper
	// limit.
	ErrMessageTooBig = errors.New("the message exceeds the maximum allowed size")

	// ErrNoPersonalAPIKey is returned when a SecretKey is required for the
	// requested operation but was not configured.
	ErrNoPersonalAPIKey = errors.New("no PersonalAPIKey provided")

	// ErrNoDistinctID is returned when distinct_id is required for the requested
	// operation but was not provided.
	ErrNoDistinctID = errors.New("no distinct_id provided")

	// ErrSDKDisabled is returned when the SDK is disabled because the project API key is missing.
	ErrSDKDisabled = errors.New("posthog SDK is disabled because project API key is missing")
)
