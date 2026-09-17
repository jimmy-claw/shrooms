package mobile

import "testing"

// StopNode is the second half of a disconnect, after StopForRestart has taken
// the tunnel down. On a device that never built a node — a disconnect before
// the first connect finished, or one after a failed start — there is nothing to
// stop, and it must return rather than dereference the missing node.
func TestStopNodeWithoutANode(t *testing.T) {
	lifecycle.Lock()
	mu.Lock()
	if running != nil || node != nil {
		mu.Unlock()
		lifecycle.Unlock()
		t.Skip("another test left a session or node behind")
	}
	mu.Unlock()
	lifecycle.Unlock()

	StopNode()
	if err := StopForRestart(); err != nil {
		t.Fatalf("stopping with no session: %v", err)
	}
	StopNode()
}
