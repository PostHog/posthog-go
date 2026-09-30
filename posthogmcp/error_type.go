package posthogmcp

import (
	"reflect"
	"strings"
)

const defaultErrorType = "Error"

// genericErrorTypes are the standard library types that only carry a message or
// wrap other errors, so they say nothing about what failed.
var genericErrorTypes = map[string]bool{
	"errors.errorString": true,
	"errors.joinError":   true,
	"fmt.wrapError":      true,
	"fmt.wrapErrors":     true,
}

// errorTypeName names err by its Go type, like the exception class Python uses,
// looking past generic wrappers. It returns "" when nothing is informative.
func errorTypeName(err error) string {
	if err == nil {
		return ""
	}
	name := strings.TrimLeft(reflect.TypeOf(err).String(), "*")
	if !genericErrorTypes[name] {
		return name
	}
	switch wrapper := err.(type) {
	case interface{ Unwrap() error }:
		return errorTypeName(wrapper.Unwrap())
	case interface{ Unwrap() []error }:
		for _, child := range wrapper.Unwrap() {
			if name := errorTypeName(child); name != "" {
				return name
			}
		}
	}
	return ""
}
