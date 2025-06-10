package posthog

type executor struct {
	queue chan func()
}

func newExecutor(cap int) *executor {
	e := &executor{
		queue: make(chan func()),
	}

	for i := 0; i < cap; i++ {
		go e.loop()
	}

	return e
}

func (e *executor) loop() {
	for task := range e.queue {
		task()
	}
}

func (e *executor) do(task func()) bool {
	select {
	case e.queue <- task:
		// task is enqueued successfully
		return true
	default:
		// buffer was full; inform the caller rather than blocking
	}

	return false
}

func (e *executor) close() {
	close(e.queue)
}
