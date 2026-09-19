// Command fixture is the soak battery's local HTTP endpoint.
//
// The soak's non-native `airflow_operator` leg runs an HttpOperator. Pointing it
// at a public API would make a weekend of scheduled runs into a weekend of
// unsolicited traffic against somebody else's service, and would let their
// outage turn our resilience report red for a reason that says nothing about our
// scheduler. So the operator points here instead: one static Go binary bound to
// loopback, serving deterministic JSON.
//
// It also records what it served, so the harness can assert that the operator
// leg really executed rather than trusting the task's own exit code.
//
// Endpoints:
//
//	GET /healthz              -> 200 "ok"
//	GET /fixture/events?n=50  -> 200 application/json, n synthetic events
//	GET /fixture/stats        -> 200 application/json, request counters
//	GET /fixture/flaky?p=20   -> 503 for p% of requests, 200 otherwise
//	GET /fixture/ready?key=K&after=N
//	                          -> 404 for the first N pokes of key K, then 200.
//	                             This is what makes a reschedule-mode sensor
//	                             deterministic and repeatable without a clock or
//	                             an Admin Variable that goes stale after one run.
//
// Usage:
//
//	go run ./test/soak/fixture --addr 127.0.0.1:18701
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// maxEvents caps the `n` query parameter. The fixture is a scheduler test, not a
// bandwidth test: an unbounded n would let a typo in a DAG turn the soak into a
// disk-filling exercise through the task logs.
const maxEvents = 2000

type counters struct {
	events atomic.Int64
	flaky  atomic.Int64
	health atomic.Int64
	ready  atomic.Int64
	bytes  atomic.Int64
}

// pokeLedger counts pokes per sensor key. It is bounded: a soak creates one key
// per run, and a weekend at the battery's cadence is a few thousand keys, but a
// harness that ran for a month would otherwise leak one map entry per run, so
// the oldest entries are dropped once the ledger passes maxKeys.
type pokeLedger struct {
	mu    sync.Mutex
	seen  map[string]int
	order []string
}

// maxKeys bounds the poke ledger. 20000 keys is far past any soak cadence and
// still a trivial amount of memory.
const maxKeys = 20000

func newPokeLedger() *pokeLedger { return &pokeLedger{seen: map[string]int{}} }

// bump records one poke for key and returns the new count.
func (l *pokeLedger) bump(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seen[key]; !ok {
		l.order = append(l.order, key)
		for len(l.order) > maxKeys {
			delete(l.seen, l.order[0])
			l.order = l.order[1:]
		}
	}
	l.seen[key]++
	return l.seen[key]
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18701", "listen address (loopback by default on purpose)")
	seed := flag.Int64("seed", 1, "PRNG seed, so the served payloads are reproducible across runs")
	flag.Parse()

	c := &counters{}
	rng := rand.New(rand.NewSource(*seed)) //nolint:gosec // deterministic fixture data, not a security context
	started := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		c.health.Add(1)
		w.WriteHeader(http.StatusOK)
		reply(w, []byte("ok"))
	})
	mux.HandleFunc("/fixture/events", func(w http.ResponseWriter, r *http.Request) {
		n := clampInt(r.URL.Query().Get("n"), 50, 1, maxEvents)
		out := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, map[string]any{
				"event_id": i,
				"kind":     fmt.Sprintf("event_%d", i%23),
				"amount":   float64(rng.Intn(100000)) / 100,
			})
		}
		body, err := json.Marshal(map[string]any{"count": n, "events": out})
		if err != nil {
			http.Error(w, "fixture: cannot encode events", http.StatusInternalServerError)
			return
		}
		c.events.Add(1)
		c.bytes.Add(int64(len(body)))
		w.Header().Set("content-type", "application/json")
		reply(w, body)
	})
	mux.HandleFunc("/fixture/flaky", func(w http.ResponseWriter, r *http.Request) {
		p := clampInt(r.URL.Query().Get("p"), 20, 0, 100)
		c.flaky.Add(1)
		if rng.Intn(100) < p {
			http.Error(w, "fixture: deliberate 503", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("content-type", "application/json")
		reply(w, []byte(`{"ok":true}`))
	})
	ledger := newPokeLedger()
	mux.HandleFunc("/fixture/ready", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			key = "default"
		}
		after := clampInt(r.URL.Query().Get("after"), 3, 0, 50)
		n := ledger.bump(key)
		c.ready.Add(1)
		if n <= after {
			// Not ready yet. A 404 is what HttpSensor reads as "keep poking",
			// and in reschedule mode each of these releases the task slot.
			http.Error(w, fmt.Sprintf("not ready: poke %d of %d", n, after), http.StatusNotFound)
			return
		}
		w.Header().Set("content-type", "application/json")
		reply(w, fmt.Appendf(nil, `{"ready":true,"pokes":%d}`, n))
	})
	mux.HandleFunc("/fixture/stats", func(w http.ResponseWriter, _ *http.Request) {
		body, err := json.Marshal(map[string]any{
			"uptime_seconds": time.Since(started).Seconds(),
			"events_served":  c.events.Load(),
			"flaky_served":   c.flaky.Load(),
			"ready_served":   c.ready.Load(),
			"health_served":  c.health.Load(),
			"bytes_served":   c.bytes.Load(),
		})
		if err != nil {
			http.Error(w, "fixture: cannot encode stats", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		reply(w, body)
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("soak fixture listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		os.Exit(1)
	}
}

// reply writes b to the client. A write error means the client hung up, which a
// loopback fixture can do nothing about beyond saying so in its own log, so it is
// logged rather than propagated.
func reply(w http.ResponseWriter, b []byte) {
	if _, err := w.Write(b); err != nil {
		log.Printf("fixture: response write failed: %v", err)
	}
}

// clampInt parses s and clamps it into [lo, hi], returning def when s is absent
// or unparseable.
func clampInt(s string, def, lo, hi int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
