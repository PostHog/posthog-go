package posthog

import "sync"

type executor struct {
	m         sync.Mutex
	available int
	closed    bool
}

func newExecutor(cap int) *executor {
	e := &executor{
		available: cap,
	}

	return e
}

func (e *executor) do(task func()) bool {
	e.m.Lock()
	defer e.m.Unlock()

	if e.closed || e.available <= 0 {
		return false
	}

	e.available--
	go func() {
		defer func() {
			e.m.Lock()
			defer e.m.Unlock()
			e.available++
		}()
		task()
	}()
	return true
}

func (e *executor) close() {
	e.m.Lock()
	defer e.m.Unlock()
	e.closed = true
}
