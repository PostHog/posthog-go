package posthog

// executor manages concurrent task execution with a bounded capacity.
// It uses a channel-based semaphore pattern for lock-free operation.
type executor struct {
	// sem is a buffered channel that acts as a counting semaphore.
	// Each slot in the channel represents permission to run a task.
	sem chan struct{}

	// done signals that the executor has been closed and should reject new tasks.
	done chan struct{}
}

// newExecutor creates a new executor with the given maximum concurrency.
func newExecutor(cap int) *executor {
	return &executor{
		sem:  make(chan struct{}, cap),
		done: make(chan struct{}),
	}
}

// do attempts to execute a task asynchronously if capacity is available.
// Returns true if the task was accepted and will be executed, false otherwise.
// The task will not be executed if:
// - The executor is closed
// - All capacity slots are currently in use
func (e *executor) do(task func()) bool {
	select {
	case e.sem <- struct{}{}: // Acquire a semaphore slot
		go func() {
			defer func() { <-e.sem }() // Release slot when done
			task()
		}()
		return true
	case <-e.done:
		// Executor is closed
		return false
	default:
		// No capacity available (non-blocking)
		return false
	}
}

// close marks the executor as closed. New tasks will be rejected.
// Note: This does not wait for running tasks to complete.
func (e *executor) close() {
	close(e.done)
}
