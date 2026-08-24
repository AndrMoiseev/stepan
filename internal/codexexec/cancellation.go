package codexexec

import "sync"

type cancelController struct {
	once     sync.Once
	mu       sync.Mutex
	close    func() error
	reason   TerminationReason
	closeErr error
}

func newCancelController(close func() error) *cancelController {
	return &cancelController{close: close}
}

func (c *cancelController) Cancel(reason TerminationReason) {
	c.once.Do(func() {
		c.mu.Lock()
		c.reason = reason
		c.closeErr = c.close()
		c.mu.Unlock()
	})
}

func (c *cancelController) Close() {
	c.once.Do(func() {
		c.mu.Lock()
		c.closeErr = c.close()
		c.mu.Unlock()
	})
}

func (c *cancelController) Reason() TerminationReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

func (c *cancelController) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}
