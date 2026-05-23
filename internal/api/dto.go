package api

import (
	"encoding/json"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

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

type taskInstanceDTO struct {
	DagID     string     `json:"dag_id"`
	DagRunID  string     `json:"dag_run_id"`
	TaskID    string     `json:"task_id"`
	MapIndex  int        `json:"map_index"`
	TryNumber int        `json:"try_number"`
	MaxTries  int        `json:"max_tries"`
	State     string     `json:"state"`
	Operator  string     `json:"operator"`
	StartDate *time.Time `json:"start_date"`
	EndDate   *time.Time `json:"end_date"`
	Duration  *float64   `json:"duration"`
	Hostname  string     `json:"hostname"`
}

type taskInstanceCollectionDTO struct {
	TaskInstances []taskInstanceDTO `json:"task_instances"`
	TotalEntries  int               `json:"total_entries"`
}

func toTaskInstanceDTO(ti domain.TaskInstance) taskInstanceDTO {
	return taskInstanceDTO{
		DagID:     ti.DagID,
		DagRunID:  ti.RunID,
		TaskID:    ti.TaskID,
		MapIndex:  ti.MapIndex,
		TryNumber: ti.TryNumber,
		MaxTries:  ti.MaxTries,
		State:     string(ti.State),
		Operator:  ti.Operator,
		StartDate: ti.StartedAt,
		EndDate:   ti.EndedAt,
		Duration:  ti.Duration,
		Hostname:  ti.Hostname,
	}
}
