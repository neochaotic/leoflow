package domain

import "time"

// StagingVolumeState is a tracked per-run staging volume joined with its DAG
// run's state, used by the GC to decide deletion (ADR 0022). RunState is empty
// when the run row is gone (orphan); RunEndedAt is the run's terminal time, used
// for the post-terminal TTL on failed runs.
type StagingVolumeState struct {
	// PVCName is the staging PersistentVolumeClaim's name.
	PVCName string
	// RunState is the DAG run's state ("success", "failed", "running", …), or
	// empty when the run no longer exists.
	RunState string
	// RunEndedAt is when the run reached a terminal state, if known.
	RunEndedAt *time.Time
	// CreatedAt is when the volume was provisioned. The GC never deletes a volume
	// younger than the TTL when its run cannot be resolved, so a lookup miss can
	// never reclaim an active run's fresh volume.
	CreatedAt time.Time
}
