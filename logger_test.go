package posthog

import (
	"bytes"
	"errors"
	"log"
	"testing"
)

type loggerFromTest testing.T

func (l *loggerFromTest) Debugf(format string, args ...interface{}) {
	(*testing.T)(l).Logf(format, args...)
}

func (l *loggerFromTest) Logf(format string, args ...interface{}) {
	(*testing.T)(l).Logf(format, args...)
}

func (l *loggerFromTest) Warnf(format string, args ...interface{}) {
	(*testing.T)(l).Logf(format, args...)
}

func (l *loggerFromTest) Errorf(format string, args ...interface{}) {
	(*testing.T)(l).Errorf(format, args...)
}

func toLogger(t *testing.T) Logger {
	return Logger((*loggerFromTest)(t))
}

// This test ensures that the interface doesn't get changed and stays compatible
// with the *testing.T type.
// If someone were to modify the interface in backward incompatible manner this
// test would break.
func TestTestingLogger(t *testing.T) {
	_ = Logger((*loggerFromTest)(t))
}

// This test ensures the standard logger shim to the Logger interface is working
// as expected.
func TestStdLogger(t *testing.T) {
	var buffer bytes.Buffer
	var logger = StdLogger(log.New(&buffer, "test ", 0), false)

	logger.Logf("Hello World!")
	logger.Logf("The answer is %d", 42)
	logger.Errorf("%s", errors.New("something went wrong!"))

	const ref = `test INFO: Hello World!
test INFO: The answer is 42
test ERROR: something went wrong!
`

	if res := buffer.String(); ref != res {
		t.Errorf("invalid logs from standard logger:\n- expected: %s\n- found: %s", ref, res)
	}
}
