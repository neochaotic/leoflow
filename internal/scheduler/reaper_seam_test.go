package scheduler

import (
	"context"
	"reflect"
	"testing"
)

// reapOncer is the shape of the execution reaper's entrypoint. The scheduler
// used to expose a seam of this shape and drive it from every 1s leader tick.
type reapOncer interface {
	ReapOnce(context.Context) error
}

// TestSchedulerTickHasNoReaperHook pins that the scheduling tick cannot drive
// the execution reaper: the Scheduler carries no field that satisfies the
// reaper's ReapOnce shape and no method that accepts one. The reapers run from
// the leader's maintenance loop, ordered after the pod reconciler's sweep, at
// that loop's cadence — not at 1s from the tick. Re-adding a seam here would
// re-open the class of race this closes (a reaper judging state the reconciler
// has not yet recovered), so its absence is asserted rather than assumed.
func TestSchedulerTickHasNoReaperHook(t *testing.T) {
	reaperType := reflect.TypeOf((*reapOncer)(nil)).Elem()
	st := reflect.TypeOf(Scheduler{})
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.Type.Kind() == reflect.Interface && f.Type.Implements(reaperType) {
			t.Errorf("Scheduler.%s holds a ReapOnce-shaped value; the tick must not be able to reap", f.Name)
		}
	}
	pt := reflect.TypeOf(&Scheduler{})
	for i := 0; i < pt.NumMethod(); i++ {
		m := pt.Method(i)
		for j := 1; j < m.Type.NumIn(); j++ { // 0 is the receiver
			in := m.Type.In(j)
			if in.Kind() == reflect.Interface && in.Implements(reaperType) {
				t.Errorf("Scheduler.%s accepts a ReapOnce-shaped value; the tick must not be able to reap", m.Name)
			}
		}
	}
}
