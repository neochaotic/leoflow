package api

import (
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

type dagRunDTO struct {
	DagID       string     `json:"dag_id"`
	DagRunID    string     `json:"dag_run_id"`
	LogicalDate time.Time  `json:"logical_date"`
	QueuedAt    time.Time  `json:"queued_at"`
	StartDate   *time.Time `json:"start_date"`
	EndDate     *time.Time `json:"end_date"`
	State       string     `json:"state"`
	RunType     string     `json:"run_type"`
	Note        string     `json:"note,omitempty"`
}

type dagRunCollectionDTO struct {
	DagRuns      []dagRunDTO `json:"dag_runs"`
	TotalEntries int         `json:"total_entries"`
}

func toDagRunDTO(r domain.DagRun) dagRunDTO {
	return dagRunDTO{
		DagID:       r.DagID,
		DagRunID:    r.RunID,
		LogicalDate: r.LogicalDate,
		QueuedAt:    r.QueuedAt,
		StartDate:   r.StartedAt,
		EndDate:     r.EndedAt,
		State:       string(r.State),
		RunType:     r.RunType,
		Note:        r.Note,
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
