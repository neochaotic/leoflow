package api

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

// synthID derives a stable synthetic identifier from composite key parts, for UI
// objects (task instances) Leoflow keys by composite rather than a UUID.
func synthID(parts ...any) string {
	h := fnv.New64a()
	for _, p := range parts {
		if _, err := fmt.Fprintf(h, "%v|", p); err != nil {
			return "" // hash.Write never errors; satisfy the linter without ignoring it.
		}
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

type tagDTO struct {
	Name string `json:"name"`
}

type scheduleIntervalDTO struct {
	Type  string `json:"__type"` //nolint:tagliatelle // Airflow API contract field name
	Value string `json:"value"`
}

type dagDTO struct {
	DagID            string               `json:"dag_id"`
	DagDisplayName   string               `json:"dag_display_name"`
	Description      string               `json:"description"`
	IsPaused         bool                 `json:"is_paused"`
	IsActive         bool                 `json:"is_active"`
	Owners           []string             `json:"owners"`
	Tags             []tagDTO             `json:"tags"`
	ScheduleInterval *scheduleIntervalDTO `json:"schedule_interval"`
	MaxActiveRuns    int                  `json:"max_active_runs"`
	Catchup          bool                 `json:"catchup"`
}

type dagCollectionDTO struct {
	Dags         []dagDTO `json:"dags"`
	TotalEntries int      `json:"total_entries"`
}

func toDagDTO(d domain.DAG) dagDTO {
	tags := make([]tagDTO, 0, len(d.Tags))
	for _, t := range d.Tags {
		tags = append(tags, tagDTO{Name: t})
	}
	owners := []string{}
	if d.Owner != "" {
		owners = append(owners, d.Owner)
	}
	var schedule *scheduleIntervalDTO
	if d.Schedule != nil && *d.Schedule != "" {
		schedule = &scheduleIntervalDTO{Type: "CronExpression", Value: *d.Schedule}
	}
	return dagDTO{
		DagID:            d.DagID,
		DagDisplayName:   d.DagID,
		Description:      d.Description,
		IsPaused:         d.IsPaused,
		IsActive:         d.IsActive,
		Owners:           owners,
		Tags:             tags,
		ScheduleInterval: schedule,
		MaxActiveRuns:    d.MaxActiveRuns,
		Catchup:          d.Catchup,
	}
}

// dagRunDTO is the Airflow 3.2.1 DAGRunResponse. Every spec-required field is
// present — notably dag_versions (a required array the UI maps over; omitting it
// crashes the run view with "undefined.map"). Fields Leoflow does not model are
// null/defaults.
type dagRunDTO struct {
	DagID              string          `json:"dag_id"`
	DagRunID           string          `json:"dag_run_id"`
	DagDisplayName     string          `json:"dag_display_name"`
	LogicalDate        time.Time       `json:"logical_date"`
	QueuedAt           time.Time       `json:"queued_at"`
	StartDate          *time.Time      `json:"start_date"`
	EndDate            *time.Time      `json:"end_date"`
	RunAfter           time.Time       `json:"run_after"`
	DataIntervalStart  *time.Time      `json:"data_interval_start"`
	DataIntervalEnd    *time.Time      `json:"data_interval_end"`
	LastSchedulingDec  *time.Time      `json:"last_scheduling_decision"`
	State              string          `json:"state"`
	RunType            string          `json:"run_type"`
	TriggeredBy        *string         `json:"triggered_by"`
	TriggeringUserName *string         `json:"triggering_user_name"`
	Conf               json.RawMessage `json:"conf"`
	Note               *string         `json:"note"`
	DagVersions        []any           `json:"dag_versions"`
	BundleVersion      *string         `json:"bundle_version"`
	Duration           *float64        `json:"duration"`
	PartitionKey       *string         `json:"partition_key"`
}

type dagRunCollectionDTO struct {
	DagRuns      []dagRunDTO `json:"dag_runs"`
	TotalEntries int         `json:"total_entries"`
}

func toDagRunDTO(r domain.DagRun) dagRunDTO {
	var dur *float64
	if r.StartedAt != nil {
		end := time.Now().UTC()
		if r.EndedAt != nil {
			end = *r.EndedAt
		}
		d := end.Sub(*r.StartedAt).Seconds()
		dur = &d
	}
	return dagRunDTO{
		DagID:          r.DagID,
		DagRunID:       r.RunID,
		DagDisplayName: r.DagID,
		LogicalDate:    r.LogicalDate,
		QueuedAt:       r.QueuedAt,
		StartDate:      r.StartedAt,
		EndDate:        r.EndedAt,
		RunAfter:       r.LogicalDate,
		State:          string(r.State),
		RunType:        r.RunType,
		Conf:           json.RawMessage("{}"),
		Note:           strPtrOrNil(r.Note),
		DagVersions:    []any{},
		Duration:       dur,
	}
}

// taskInstanceDTO is the Airflow 3.2.1 TaskInstanceResponse. Every spec-required
// field is present; values Leoflow does not track are null/defaults. id is a
// stable synthetic key (Leoflow keys task instances by composite, not a UUID).
type taskInstanceDTO struct {
	ID               string     `json:"id"`
	TaskID           string     `json:"task_id"`
	DagID            string     `json:"dag_id"`
	DagRunID         string     `json:"dag_run_id"`
	MapIndex         int        `json:"map_index"`
	LogicalDate      *time.Time `json:"logical_date"`
	RunAfter         *time.Time `json:"run_after"`
	StartDate        *time.Time `json:"start_date"`
	EndDate          *time.Time `json:"end_date"`
	Duration         *float64   `json:"duration"`
	State            *string    `json:"state"`
	TryNumber        int        `json:"try_number"`
	MaxTries         int        `json:"max_tries"`
	TaskDisplayName  string     `json:"task_display_name"`
	DagDisplayName   string     `json:"dag_display_name"`
	Hostname         *string    `json:"hostname"`
	Unixname         *string    `json:"unixname"`
	Pool             string     `json:"pool"`
	PoolSlots        int        `json:"pool_slots"`
	Queue            *string    `json:"queue"`
	PriorityWeight   *int       `json:"priority_weight"`
	Operator         *string    `json:"operator"`
	OperatorName     *string    `json:"operator_name"`
	QueuedWhen       *time.Time `json:"queued_when"`
	ScheduledWhen    *time.Time `json:"scheduled_when"`
	Pid              *int       `json:"pid"`
	Executor         *string    `json:"executor"`
	ExecutorConfig   string     `json:"executor_config"`
	Note             *string    `json:"note"`
	RenderedMapIndex *string    `json:"rendered_map_index"`
	Trigger          *string    `json:"trigger"`
	TriggererJob     *string    `json:"triggerer_job"`
	DagVersion       *string    `json:"dag_version"`
}

type taskInstanceCollectionDTO struct {
	TaskInstances []taskInstanceDTO `json:"task_instances"`
	TotalEntries  int               `json:"total_entries"`
}

func toTaskInstanceDTO(ti domain.TaskInstance) taskInstanceDTO {
	var state *string
	if ti.State != "" && ti.State != domain.TaskStateNone {
		s := string(ti.State)
		state = &s
	}
	op := strPtrOrNil(ti.Operator)
	return taskInstanceDTO{
		ID:               synthID(ti.RunID, ti.TaskID, ti.MapIndex),
		TaskID:           ti.TaskID,
		DagID:            ti.DagID,
		DagRunID:         ti.RunID,
		MapIndex:         ti.MapIndex,
		StartDate:        ti.StartedAt,
		EndDate:          ti.EndedAt,
		Duration:         ti.Duration,
		State:            state,
		TryNumber:        ti.TryNumber,
		MaxTries:         ti.MaxTries,
		TaskDisplayName:  ti.TaskID,
		DagDisplayName:   ti.DagID,
		Hostname:         strPtrOrNil(ti.Hostname),
		Pool:             "default_pool",
		PoolSlots:        1,
		Operator:         op,
		OperatorName:     op,
		ExecutorConfig:   "{}",
		RenderedMapIndex: renderedMapIndex(ti.MapIndex),
		Note:             strPtrOrNil(ti.Note),
	}
}

// renderedMapIndex returns nil for an unmapped task (-1), else the index string.
func renderedMapIndex(idx int) *string {
	if idx < 0 {
		return nil
	}
	s := strconv.Itoa(idx)
	return &s
}
