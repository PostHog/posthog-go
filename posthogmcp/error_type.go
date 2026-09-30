package posthogmcp

import (
	"reflect"
	"strings"
)

const defaultErrorType = "Error"

// wrapperErrorTypes are the standard library types outside package errors that
// only wrap other errors.
var wrapperErrorTypes = map[string]bool{
	"fmt.wrapError":  true,
	"fmt.wrapErrors": true,
}

// errorTypeName names err by its Go type, like the exception class Python uses,
// looking past types that only carry a message or wrap other errors: those of
// any package named errors (the standard library's, pkg/errors') and fmt's.
// The name is package-qualified, such as fs.PathError, on purpose:
// posthog.ExceptionItem has no module field. It returns "" when nothing is
// informative.
func errorTypeName(err error) string {
	if err == nil {
		return ""
	}
	name := strings.TrimLeft(reflect.TypeOf(err).String(), "*")
	if !strings.HasPrefix(name, "errors.") && !wrapperErrorTypes[name] {
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
