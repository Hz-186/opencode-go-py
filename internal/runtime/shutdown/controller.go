// Package shutdown models process-wide signal escalation without owning OS
// signal registration.
package shutdown

import "sync"

// State is the operational shutdown state.
type State uint8

const (
	Running State = iota
	ShutdownRequested
	ForceRequested
)

// Action tells the signal owner what must happen for an observation.
type Action uint8

const (
	StartShutdown Action = iota + 1
	ForceShutdown
)

// Controller turns the first signal into graceful shutdown and every later
// signal into forced shutdown.
type Controller struct {
	mu       sync.Mutex
	state    State
	shutdown chan struct{}
	force    chan struct{}
}

// NewController creates a running signal controller.
func NewController() *Controller {
	return &Controller{
		shutdown: make(chan struct{}),
		force:    make(chan struct{}),
	}
}

// Observe records one shutdown signal and returns the required action.
func (c *Controller) Observe() Action {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.state {
	case Running:
		c.state = ShutdownRequested
		close(c.shutdown)
		return StartShutdown
	case ShutdownRequested:
		c.state = ForceRequested
		close(c.force)
		return ForceShutdown
	default:
		return ForceShutdown
	}
}

// State returns the current operational state.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Shutdown closes after the first observed signal.
func (c *Controller) Shutdown() <-chan struct{} {
	return c.shutdown
}

// Force closes after the second observed signal.
func (c *Controller) Force() <-chan struct{} {
	return c.force
}
