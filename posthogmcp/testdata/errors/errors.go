// Package errors mimics the wrapper types of github.com/pkg/errors, whose
// package name is also errors, so tests can exercise their Go type names.
package errors

// Fundamental is a leaf error, like pkg/errors' fundamental.
type Fundamental struct{ Msg string }

func (e *Fundamental) Error() string { return e.Msg }

// WithStack wraps through Cause only, like pkg/errors before it added Unwrap.
type WithStack struct{ Err error }

func (e *WithStack) Error() string { return e.Err.Error() }
func (e *WithStack) Cause() error  { return e.Err }

// WithMessage wraps through Unwrap, like pkg/errors' withMessage.
type WithMessage struct {
	Err error
	Msg string
}

func (e *WithMessage) Error() string { return e.Msg + ": " + e.Err.Error() }
func (e *WithMessage) Unwrap() error { return e.Err }
