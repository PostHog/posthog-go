// Package errors mirrors the type names of github.com/pkg/errors, whose package
// name is also errors, and adds an application error type, so tests can
// exercise both kinds of errors.* type name.
package errors

type fundamental struct{ msg string }

func (e *fundamental) Error() string { return e.msg }

// New returns a leaf error, like pkg/errors' New.
func New(msg string) error { return &fundamental{msg: msg} }

type withStack struct{ err error }

func (e *withStack) Error() string { return e.err.Error() }
func (e *withStack) Cause() error  { return e.err }

// WithStack wraps through Cause only, like pkg/errors before it added Unwrap.
func WithStack(err error) error { return &withStack{err: err} }

type withMessage struct {
	err error
	msg string
}

func (e *withMessage) Error() string { return e.msg + ": " + e.err.Error() }
func (e *withMessage) Unwrap() error { return e.err }

// WithMessage wraps through Unwrap, like pkg/errors' WithMessage.
func WithMessage(err error, msg string) error { return &withMessage{err: err, msg: msg} }

// NotFound is an application error from a package that happens to be named
// errors, such as a service's internal/errors.
type NotFound struct{ Resource string }

func (e *NotFound) Error() string { return e.Resource + " not found" }
