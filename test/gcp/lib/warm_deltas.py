#!/usr/bin/env python3
"""Turn one arm's API responses into a series of per-attempt observations.

WHY THIS INTERVAL. The obvious series, and the one this runner used to collect,
is the pod's own PodScheduled -> state.running.startedAt, which is what
pod-per-task.sh measures and is correct there. That interval is defined per
attempt in arms A and A', where dispatch is pod-per-task. It is NOT defined per
attempt in arm B: a warm attempt is handed to a worker WarmPoolReconciler
created earlier to hold EffectiveMinIdle, so the pods arm B leaves behind carry
the pool fill's startup, sampled once per worker, not the latency of the
attempts that ran on them. Comparing the two arms on it would time different
events and report the difference as a speedup (#1203).

run.queued_at -> task.start_date is defined identically under both arms, is what
an operator actually waits through, and is where warm pools should show their
difference: no pod create, no image pull, no kubelet start.

RESOLUTION. These stamps are microsecond-precision with a numeric offset
("2026-09-20T11:15:00.557941-03:00"), verified against a running control plane.
pod-per-task.sh's rfc3339_to_epoch pins the format to "%Y-%m-%dT%H:%M:%SZ" and
returns EMPTY for every one of them, which inside a pipeline is an empty series
rather than an error. That is why this parses here and not there.

Usage:
  warm_deltas.py ids    <since-epoch> < dagRuns.json
  warm_deltas.py deltas <since-epoch> <dagRuns.json> <taskInstances.jsonl>
  warm_deltas.py --self-test
"""
from __future__ import annotations

import json
import sys


def to_epoch(value):
    """RFC3339 (Z or numeric offset, with or without fractional seconds) -> float.

    Returns None rather than raising: a missing or malformed stamp is a fact
    about that attempt, and the caller drops the attempt and says so.
    """
    import datetime

    if not value:
        return None
    v = value.strip()
    if v.endswith("Z"):
        v = v[:-1] + "+00:00"
    try:
        return datetime.datetime.fromisoformat(v).timestamp()
    except ValueError:
        return None


def runs_since(doc, since):
    """dag_run_id -> queued_at epoch, for runs queued at or after `since`.

    The window filter is queued_at and not wall-clock ordering, because attempts
    from an earlier arm are still in the API and must not be counted twice.
    """
    out = {}
    for r in doc.get("dag_runs", []):
        q = to_epoch(r.get("queued_at"))
        if q is not None and q >= since and r.get("dag_run_id"):
            out[r["dag_run_id"]] = q
    return out


def deltas(queued, ti_docs):
    """One observation per attempt: task start_date minus its run's queued_at.

    Returns (series, notes). An attempt whose task never started has no
    start_date and is DROPPED with a note rather than counted as zero: a warm
    pool that never filled drops all of them, and that is a finding, not a
    series of zeros. A negative delta is a clock or a bug, never a fast task,
    and is dropped the same way.
    """
    # EARLIEST start per run, not first-seen. gcp_probe has one task, so the two
    # agree there, but "whichever task instance the API happened to list first"
    # is not a definition, and this file is reused the moment somebody points it
    # at a DAG with two tasks. When work BEGAN on a run is the earliest start.
    earliest, notes = {}, []
    for doc in ti_docs:
        for ti in doc.get("task_instances", []):
            rid = ti.get("dag_run_id")
            if rid not in queued:
                continue
            st = to_epoch(ti.get("start_date"))
            if st is None:
                notes.append("%s/%s: no start_date; the task never started"
                             % (rid, ti.get("task_id") or "?"))
                continue
            if rid not in earliest or st < earliest[rid]:
                earliest[rid] = st
    series, seen = [], set()
    for rid, st in earliest.items():
        d = st - queued[rid]
        if d < 0:
            notes.append("%s: start_date precedes queued_at by %.3fs" % (rid, -d))
            continue
        seen.add(rid)
        series.append(d)
    unobserved = len(queued) - len(seen)
    if unobserved > 0:
        notes.append(
            "observed %d of %d attempts in this window; %d produced no usable start_date"
            % (len(seen), len(queued), unobserved)
        )
    return series, notes


def _read_jsonl(path):
    docs = []
    with open(path) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                docs.append(json.loads(line))
            except ValueError:
                continue
    return docs


def self_test():
    fails = []

    def eq(got, want, name):
        if got == want:
            print("  ok   %s" % name)
        else:
            print("  FAIL %s: %r != %r" % (name, got, want))
            fails.append(name)

    # The shape this API really answers with, copied from a running control
    # plane rather than imagined: microseconds and a numeric offset.
    # Checked against calendar.timegm on the hand-converted UTC equivalent, which
    # is a different code path from fromisoformat: a constant typed from memory
    # proves the parser agrees with whoever typed it, and nothing else.
    import calendar
    want = calendar.timegm((2026, 9, 20, 14, 15, 0)) + 0.557941
    eq(round(to_epoch("2026-09-20T11:15:00.557941-03:00"), 6), round(want, 6),
       "a microsecond stamp with a numeric offset parses, and the offset is applied")
    eq(round(to_epoch("2026-09-20T11:15:00.557941-03:00")
             - to_epoch("2026-09-20T11:15:00-03:00"), 6), 0.557941,
       "the fractional part survives")
    eq(to_epoch("2026-09-20T14:15:00Z"), to_epoch("2026-09-20T11:15:00-03:00"),
       "Z and an equivalent offset are the same instant")
    eq(to_epoch("2026-09-20T14:15:00Z"), to_epoch("2026-09-20T14:15:00+00:00"),
       "Z and +00:00 agree")
    eq(to_epoch(""), None, "an empty stamp is None, not an exception")
    eq(to_epoch("not a date"), None, "a malformed stamp is None, not an exception")
    eq(to_epoch(None), None, "a missing stamp is None")
    # The format pod-per-task.sh's parser expects still has to work, since a
    # control plane configured for UTC answers in exactly that shape.
    eq(to_epoch("2026-09-20T14:15:00Z") is not None, True,
       "the whole-second UTC form still parses")

    runs = {"dag_runs": [
        {"dag_run_id": "old", "queued_at": "2026-09-20T14:00:00Z"},
        {"dag_run_id": "a", "queued_at": "2026-09-20T14:10:00Z"},
        {"dag_run_id": "b", "queued_at": "2026-09-20T14:11:00Z"},
        {"dag_run_id": "nostamp", "queued_at": None},
    ]}
    since = to_epoch("2026-09-20T14:05:00Z")
    q = runs_since(runs, since)
    eq(sorted(q), ["a", "b"], "a run queued before the arm started is excluded")
    eq("nostamp" in q, False, "a run with no queued_at is excluded, not treated as zero")

    tis = [
        {"task_instances": [{"dag_run_id": "a", "start_date": "2026-09-20T14:10:04Z"}]},
        {"task_instances": [{"dag_run_id": "b", "start_date": "2026-09-20T14:11:10Z"}]},
        {"task_instances": [{"dag_run_id": "old", "start_date": "2026-09-20T14:00:01Z"}]},
    ]
    series, notes = deltas(q, tis)
    eq(sorted(series), [4.0, 10.0], "one delta per in-window attempt, in seconds")
    eq(any("old" in n for n in notes), False, "an out-of-window attempt is not a note, it is simply not ours")
    eq(notes, [], "a complete window produces no notes")

    # The failure this whole runner exists to stop: an arm that measured nothing
    # must not look like an arm that measured zeros.
    empty, notes = deltas(q, [{"task_instances": [
        {"dag_run_id": "a", "start_date": None},
        {"dag_run_id": "b", "start_date": None},
    ]}])
    eq(empty, [], "attempts that never started produce NO observations, not zeros")
    eq(len(notes) >= 2, True, "and every one of them is named")

    # Warm pools are the thing under test, so the arm-B shape gets its own case:
    # the same task instance returned twice must not be counted twice.
    dup, _ = deltas(q, [
        {"task_instances": [{"dag_run_id": "a", "start_date": "2026-09-20T14:10:04Z"}]},
        {"task_instances": [{"dag_run_id": "a", "start_date": "2026-09-20T14:10:04Z"}]},
    ])
    eq(dup, [4.0], "a repeated task instance is counted once")

    # A two-task run: the observation is when work BEGAN, not whichever instance
    # the API listed first. Deliberately ordered late-first in the fixture.
    two, _ = deltas({"a": to_epoch("2026-09-20T14:10:00Z")}, [{"task_instances": [
        {"dag_run_id": "a", "task_id": "late", "start_date": "2026-09-20T14:10:09Z"},
        {"dag_run_id": "a", "task_id": "early", "start_date": "2026-09-20T14:10:02Z"},
    ]}])
    eq(two, [2.0], "a multi-task run reports its EARLIEST start, not the first listed")

    neg, notes = deltas(q, [{"task_instances": [
        {"dag_run_id": "a", "start_date": "2026-09-20T14:09:00Z"}]}])
    eq(neg, [], "a start before its queue is dropped, never folded into a percentile")
    eq(any("precedes" in n for n in notes), True, "and it says why")

    print("warm_deltas self-test: %s" % ("ok" if not fails else "FAILED: %s" % ", ".join(fails)))
    return 1 if fails else 0


def main(argv):
    if len(argv) >= 2 and argv[1] == "--self-test":
        return self_test()
    if len(argv) == 3 and argv[1] == "ids":
        q = runs_since(json.load(sys.stdin), float(argv[2]))
        for rid in q:
            print(rid)
        return 0
    if len(argv) == 5 and argv[1] == "deltas":
        q = runs_since(json.load(open(argv[3])), float(argv[2]))
        series, notes = deltas(q, _read_jsonl(argv[4]))
        for n in notes:
            print(n, file=sys.stderr)
        for d in series:
            print("%.6f" % d)
        return 0
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
