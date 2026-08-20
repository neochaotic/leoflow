package agentrpc

import (
	"sync"
	"time"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

// ReclaimReason names why an assignment was reclaimed (ADR 0058 N1b, H1).
type ReclaimReason int

const (
	// ReclaimLeaseExpired means the worker did not ack the assignment as started
	// before its lease elapsed.
	ReclaimLeaseExpired ReclaimReason = iota
	// ReclaimRefused means the worker acked with started=false (it cannot or will
	// not run it).
	ReclaimRefused
	// ReclaimWorkerGone means the worker's stream ended while it held an unacked
	// assignment (a disconnected worker can never ack it).
	ReclaimWorkerGone
)

// ReclaimEvent is emitted when a handed-out assignment must be re-placed. The
// future placement layer (N1b1-place) consumes it to re-dispatch the attempt on
// the infra budget. It deliberately carries no attempt_token.
type ReclaimEvent struct {
	AssignmentID string
	DagVersionID string
	Reason       ReclaimReason
}

// registeredWorker is one warm worker's registry entry. send is the handler's
// outbound assignment channel; the handler owns Send()ing what lands on it.
type registeredWorker struct {
	identity   string
	dagVersion string
	send       chan *agentv1.WorkAssignment
	// busy is set once the worker acks an assignment as started; it is cleared
	// when the worker signals SlotFree.
	busy bool
}

// leaseState tracks one in-flight assignment awaiting an ack.
type leaseState struct {
	worker     *registeredWorker
	dagVersion string
	timer      *time.Timer
}

// workerRegistry is the concurrency-safe home of the warm-worker fleet and the
// H1 ack/lease machine (ADR 0058 N1b).
//
// Data structures and complexity:
//   - workers: identity -> entry. Register/Deregister/MarkFree are O(1).
//   - free:    dag_version -> (identity -> entry). Assign grabs one free worker
//     of a dag_version in O(1) (a single map-range that breaks on the first
//     element), then removes it in O(1).
//   - leases:  assignment_id -> in-flight lease. Ack and lease-expiry are O(1).
type workerRegistry struct {
	mu      sync.Mutex
	workers map[string]*registeredWorker
	free    map[string]map[string]*registeredWorker
	leases  map[string]*leaseState

	onReclaim func(ReclaimEvent)
	// leaseFor computes an assignment's ack deadline. Injectable so tests drive
	// reclaim deterministically with a tiny lease.
	leaseFor func(*agentv1.WorkAssignment) time.Duration
}

// newWorkerRegistry builds a registry whose reclaim events are delivered to
// onReclaim (may be nil).
func newWorkerRegistry(onReclaim func(ReclaimEvent)) *workerRegistry {
	return &workerRegistry{
		workers:   map[string]*registeredWorker{},
		free:      map[string]map[string]*registeredWorker{},
		leases:    map[string]*leaseState{},
		onReclaim: onReclaim,
		leaseFor: func(a *agentv1.WorkAssignment) time.Duration {
			return time.Duration(a.GetLeaseSeconds()) * time.Second
		},
	}
}

// Register records a warm worker under its authenticated identity, ready to take
// work for dagVersion. Idempotent: a reconnect with the same identity replaces
// the prior entry (never adds a second), and the fresh entry starts free. It
// returns the entry so the caller can Deregister exactly the entry it created.
func (r *workerRegistry) Register(identity, dagVersion string, send chan *agentv1.WorkAssignment) *registeredWorker {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.workers[identity]; ok {
		// Drop the superseded entry from the free set; any lease still bound to it
		// keeps its own timer and reclaims independently.
		r.removeFreeLocked(old)
	}
	w := &registeredWorker{identity: identity, dagVersion: dagVersion, send: send}
	r.workers[identity] = w
	r.addFreeLocked(w)
	return w
}

// Deregister removes a worker's entry, but only if the registry still points at
// exactly this entry — a reconnect under the same identity installs a new entry,
// and the stale stream's later Deregister must not evict the live one. Any
// in-flight leases the worker still held are reclaimed: a gone worker can never
// ack them.
func (r *workerRegistry) Deregister(w *registeredWorker) {
	if w == nil {
		return
	}
	r.mu.Lock()
	if r.workers[w.identity] != w {
		r.mu.Unlock()
		return
	}
	delete(r.workers, w.identity)
	r.removeFreeLocked(w)
	var reclaims []ReclaimEvent
	for aid, ls := range r.leases {
		if ls.worker == w {
			ls.timer.Stop()
			delete(r.leases, aid)
			reclaims = append(reclaims, ReclaimEvent{AssignmentID: aid, DagVersionID: ls.dagVersion, Reason: ReclaimWorkerGone})
		}
	}
	r.mu.Unlock()
	for _, ev := range reclaims {
		r.emitReclaim(ev)
	}
}

// Assign hands a WorkAssignment to some free worker of dagVersion by pushing it
// onto that worker's outbound channel and starting its lease. It returns false
// when no free worker of that dag_version exists (nothing was handed out and no
// lease was started).
func (r *workerRegistry) Assign(dagVersion string, a *agentv1.WorkAssignment) bool {
	r.mu.Lock()
	w := r.takeFreeLocked(dagVersion)
	if w == nil {
		r.mu.Unlock()
		return false
	}
	// The worker was just removed from the free set, so nothing else races for
	// its channel; a non-blocking send cannot lose the assignment unless the
	// channel is already occupied (a caller Assigned twice without an ack). In
	// that case put it back and refuse.
	select {
	case w.send <- a:
	default:
		r.addFreeLocked(w)
		r.mu.Unlock()
		return false
	}
	aid := a.GetAssignmentId()
	ls := &leaseState{worker: w, dagVersion: dagVersion}
	ls.timer = time.AfterFunc(r.leaseFor(a), func() { r.onLeaseExpire(aid) })
	r.leases[aid] = ls
	r.mu.Unlock()
	return true
}

// Ack settles the lease for assignmentID. started=true marks the worker busy and
// cancels the lease (no reclaim). started=false reclaims the assignment. An ack
// for an unknown assignment (already expired or already settled) is ignored.
func (r *workerRegistry) Ack(assignmentID string, started bool) {
	r.mu.Lock()
	ls, ok := r.leases[assignmentID]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.leases, assignmentID)
	ls.timer.Stop()
	if started {
		ls.worker.busy = true
		r.mu.Unlock()
		return
	}
	dagVersion := ls.dagVersion
	r.mu.Unlock()
	r.emitReclaim(ReclaimEvent{AssignmentID: assignmentID, DagVersionID: dagVersion, Reason: ReclaimRefused})
}

// MarkFree records a worker's SlotFree signal: it clears busy and returns the
// worker to the free set so it can take new work. Unknown identities are ignored.
func (r *workerRegistry) MarkFree(identity string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[identity]
	if !ok {
		return
	}
	w.busy = false
	r.addFreeLocked(w)
}

// onLeaseExpire reclaims an assignment whose lease elapsed with no ack. If the
// lease was already settled (acked or the worker deregistered), it is a no-op.
func (r *workerRegistry) onLeaseExpire(assignmentID string) {
	r.mu.Lock()
	ls, ok := r.leases[assignmentID]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.leases, assignmentID)
	dagVersion := ls.dagVersion
	r.mu.Unlock()
	r.emitReclaim(ReclaimEvent{AssignmentID: assignmentID, DagVersionID: dagVersion, Reason: ReclaimLeaseExpired})
}

// emitReclaim delivers a reclaim event to the observer, always OUTSIDE the lock
// so the observer may re-enter the registry without deadlocking.
func (r *workerRegistry) emitReclaim(ev ReclaimEvent) {
	if r.onReclaim != nil {
		r.onReclaim(ev)
	}
}

// ── free-set helpers (all require r.mu held) ────────────────────────────────

func (r *workerRegistry) addFreeLocked(w *registeredWorker) {
	set := r.free[w.dagVersion]
	if set == nil {
		set = map[string]*registeredWorker{}
		r.free[w.dagVersion] = set
	}
	set[w.identity] = w
}

func (r *workerRegistry) removeFreeLocked(w *registeredWorker) {
	set := r.free[w.dagVersion]
	if set == nil {
		return
	}
	delete(set, w.identity)
	if len(set) == 0 {
		delete(r.free, w.dagVersion)
	}
}

func (r *workerRegistry) takeFreeLocked(dagVersion string) *registeredWorker {
	set := r.free[dagVersion]
	for id, w := range set {
		delete(set, id)
		if len(set) == 0 {
			delete(r.free, dagVersion)
		}
		return w
	}
	return nil
}

// ── test/introspection helpers ──────────────────────────────────────────────

func (r *workerRegistry) registered(identity string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.workers[identity]
	return ok
}

func (r *workerRegistry) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.workers)
}

func (r *workerRegistry) busy(identity string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[identity]
	return ok && w.busy
}

func (r *workerRegistry) dagVersionOf(identity string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.workers[identity]; ok {
		return w.dagVersion
	}
	return ""
}
