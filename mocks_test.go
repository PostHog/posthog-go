package posthog

import (
	"context"
	"log/slog"
)

type fakeEnqueueClient struct {
	enqueuedMsgs []Message
}

func (f *fakeEnqueueClient) Enqueue(m Message) error {
	f.enqueuedMsgs = append(f.enqueuedMsgs, m)
	return nil
}

type fakeNextSlogHandler struct {
	isEnabled      bool
	handledRecords []slog.Record
}

func (n *fakeNextSlogHandler) Enabled(context.Context, slog.Level) bool {
	return n.isEnabled
}
func (n *fakeNextSlogHandler) Handle(_ context.Context, r slog.Record) error {
	n.handledRecords = append(n.handledRecords, r)
	return nil
}
func (n *fakeNextSlogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return n
}
func (n *fakeNextSlogHandler) WithGroup(string) slog.Handler {
	return n
}

type wrappedError struct{ err error }

func (w wrappedError) Error() string { return "wrap: " + w.err.Error() }
func (w wrappedError) Unwrap() error { return w.err }

type errValuer struct{ err error }

func (e errValuer) LogValue() slog.Value {
	return slog.AnyValue(e.err)
}
