package posthogmcp

import (
	"reflect"
	"strings"
)

const defaultErrorType = "Error"

// wrapperErrorTypes only carry a message or wrap other errors, so they say
// nothing about what failed: the standard library's and github.com/pkg/errors'.
var wrapperErrorTypes = map[string]bool{
	"errors.errorString": true,
	"errors.joinError":   true,
	"fmt.wrapError":      true,
	"fmt.wrapErrors":     true,
	"errors.fundamental": true,
	"errors.withStack":   true,
	"errors.withMessage": true,
}

// errorTypeName names err by its Go type, like the exception class Python uses,
// looking past wrapperErrorTypes.
// The name is package-qualified, such as fs.PathError, on purpose:
// posthog.ExceptionItem has no module field. It returns "" when nothing is
// informative.
func errorTypeName(err error) string {
	if err == nil {
		return ""
	}
	name := strings.TrimLeft(reflect.TypeOf(err).String(), "*")
	if !wrapperErrorTypes[name] {
		return name
	}
	switch wrapper := err.(type) {
	case interface{ Unwrap() error }:
		return errorTypeName(wrapper.Unwrap())
	case interface{ Cause() error }:
		return errorTypeName(wrapper.Cause())
	case interface{ Unwrap() []error }:
		for _, child := range wrapper.Unwrap() {
			if name := errorTypeName(child); name != "" {
				return name
			}
		}
	}
	return ""
}
