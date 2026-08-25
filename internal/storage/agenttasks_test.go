package storage

import "testing"

// A served task's request_id must NEVER be zero.
//
// The real client parses a zero request_id as "empty response, no work"
// (loon-agent's Poll: `if raw.RequestID == 0 { ... }`), so an auto-grab — which
// by definition has no community request behind it, and therefore a stored
// RequestID of 0 — would be silently discarded by every agent that asked for
// it. The queue would look busy and the fleet would look idle, with nothing
// logging a refusal at either end.
//
// A DB-free pin on a pure function, because this is a WIRE contract: it cannot
// be caught by anything downstream, and the mock only found it because it was
// written to mirror the same rule.
func TestWireRequestIDIsNeverZero(t *testing.T) {
	t.Run("auto-grab falls back to the task id", func(t *testing.T) {
		task := AgentTask{ID: 91, RequestID: 0}
		if got := task.WireRequestID(); got != 91 {
			t.Errorf("WireRequestID() = %d, want 91 — a zero request_id reads as "+
				"'no work' and the agent drops the task on the floor", got)
		}
	})
	t.Run("a real request keeps its own id", func(t *testing.T) {
		task := AgentTask{ID: 91, RequestID: 5000}
		if got := task.WireRequestID(); got != 5000 {
			t.Errorf("WireRequestID() = %d, want 5000 — completing the grab has to "+
				"fulfil the request that asked for it", got)
		}
	})
}
