// Package contextmarshaler supplies go-json's context-aware test fixtures.
// Its third-party MarshalJSON signature conflicts with vet's standard-library
// signature check, so the fixtures live outside the ./... package pattern.
package contextmarshaler

import "context"

type Property struct {
	Credential string
	output     string
	err        error
}

func NewProperty(credential, output string, err error) Property {
	return Property{Credential: credential, output: output, err: err}
}

func (v Property) MarshalJSON(context.Context) ([]byte, error) { return []byte(v.output), v.err }

type PointerProperty Property

func (v *PointerProperty) MarshalJSON(ctx context.Context) ([]byte, error) {
	return Property(*v).MarshalJSON(ctx)
}

type Number float64

func (Number) MarshalJSON(context.Context) ([]byte, error) { return []byte(`"context-number"`), nil }
