package agent

import (
	"os"
	"strconv"
	"strings"
)

// OOM detection, by counting the kernel's own kills rather than guessing from an
// exit code.
//
// The agent runs the task as a CHILD process, so the container's PID 1 is the
// agent and not the task. When the cgroup hits its memory limit the kernel picks
// the biggest consumer, which is the task. The container therefore does NOT die
// of OOM: the task takes SIGKILL, the agent collects exit 137 and reports
// normally, and Kubernetes never marks the container OOMKilled. The reconciler's
// good message ("raise the task's memory limit") keys on that mark, so in the
// common case it never fires and the operator is told "task failed (exit 137)"
// (#1216).
//
// 137 alone cannot carry the answer: it is SIGKILL, which is also what an
// external kill looks like. So this reads the evidence the kernel already keeps.
//
//	cgroup v2  /sys/fs/cgroup/memory.events              field oom_kill
//	cgroup v1  /sys/fs/cgroup/memory/memory.oom_control  field oom_kill
//
// Sampled either side of the run, a RISE is proof, not inference. Unreadable is
// not zero: a counter that cannot be read must not let a later sample look like
// a rise, so the reader reports whether it knows at all.

const (
	cgroupV2Events = "/sys/fs/cgroup/memory.events"
	cgroupV1OOM    = "/sys/fs/cgroup/memory/memory.oom_control"
)

// oomCounter is the kernel's count of OOM kills in this cgroup, plus whether it
// could be read. The bool is not a nicety: without it an unreadable "before" of
// 0 and a readable "after" of 1 reads as a kill that may not have happened.
type oomCounter struct {
	kills int64
	known bool
}

// readOOMKills reads the counter from whichever cgroup version is mounted.
func readOOMKills() oomCounter {
	if c, ok := parseOOMField(cgroupV2Events, "oom_kill"); ok {
		return oomCounter{kills: c, known: true}
	}
	if c, ok := parseOOMField(cgroupV1OOM, "oom_kill"); ok {
		return oomCounter{kills: c, known: true}
	}
	return oomCounter{known: false}
}

// parseOOMField pulls one "<name> <value>" field out of a cgroup stat file.
// Both files are line oriented with space separated pairs, so one parser serves
// both and there is no second place for the format to be assumed differently.
func parseOOMField(path, field string) (int64, bool) {
	b, err := os.ReadFile(path) //nolint:gosec // a fixed cgroup path, or a test fixture
	if err != nil {
		return 0, false
	}
	return parseOOMFieldFrom(string(b), field)
}

// parseOOMFieldFrom is the pure half, so the shapes can be tested without a
// cgroup mounted: this runs on macOS, where neither path exists.
func parseOOMFieldFrom(content, field string) (int64, bool) {
	for _, line := range strings.Split(content, "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found || name != field {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// oomKilledBetween reports whether the kernel killed something in this cgroup
// between the two samples.
//
// It answers false when EITHER sample is unknown. A "probably" here becomes a
// failure message telling someone to raise a memory limit that was never the
// problem, which is worse than the generic message it replaces.
func oomKilledBetween(before, after oomCounter) bool {
	if !before.known || !after.known {
		return false
	}
	return after.kills > before.kills
}
