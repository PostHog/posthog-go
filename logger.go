package posthog

import (
	"log"
	"os"
)

// Logger defines the logging interface used by PostHog clients.
type Logger interface {
	// Debugf is called by PostHog client to log debug messages about the
	// operations they perform. Messages logged by this method are usually
	// tagged with an `DEBUG` log level in common logging libraries.
	Debugf(format string, args ...interface{})

	// Logf is called by PostHog client to log regular messages about the
	// operations they perform. Messages logged by this method are usually
	// tagged with an `INFO` log level in common logging libraries.
	Logf(format string, args ...interface{})

	// Warnf is called by PostHog client to log warning messages about
	// the operations they perform. Messages logged by this method are usually
	// tagged with an `WARN` log level in common logging libraries.
	Warnf(format string, args ...interface{})

	// Errorf is called by PostHog clients call this method to log errors
	// they encounter while sending events to the backend servers.
	// Messages logged by this method are usually tagged with an `ERROR` log
	// level in common logging libraries.
	Errorf(format string, args ...interface{})
}

// StdLogger wraps a standard log.Logger as a PostHog Logger.
// The verbose parameter controls whether Debugf messages are emitted.
func StdLogger(logger *log.Logger, verbose bool) Logger {
	return stdLogger{
		logger:  logger,
		verbose: verbose,
	}
}

type stdLogger struct {
	logger  *log.Logger
	verbose bool
}

func (l stdLogger) Debugf(format string, args ...interface{}) {
	if l.verbose {
		l.logger.Printf("DEBUG: "+format, args...)
	}
}

func (l stdLogger) Logf(format string, args ...interface{}) {
	l.logger.Printf("INFO: "+format, args...)
}

func (l stdLogger) Warnf(format string, args ...interface{}) {
	l.logger.Printf("WARN: "+format, args...)
}

func (l stdLogger) Errorf(format string, args ...interface{}) {
	l.logger.Printf("ERROR: "+format, args...)
}

func newDefaultLogger(verbose bool) Logger {
	return StdLogger(log.New(os.Stderr, "posthog ", log.LstdFlags), verbose)
}
