// Package failurealert adapts a DAG's on-failure alert rules (#424) to the
// scheduler's Alerter seam: it resolves each rule's managed connection to an
// endpoint URL, renders the message, and sends it via the alerts notifier —
// best-effort, so a delivery failure is logged and never fails the run. It is
// the piece that wires the pure notifier (internal/alerts) and the connection
// store together, kept out of the scheduler so that package stays storage-free.
package failurealert

import (
	"context"
	"log/slog"

	"github.com/neochaotic/leoflow/internal/alerts"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/scheduler"
)

// EndpointResolver maps a managed connection id to the channel endpoint URL to
// POST to (including any secret carried by the connection). tenantID scopes the
// lookup. The connection→URL contract lives in the implementation so this
// package needs no storage dependency.
type EndpointResolver interface {
	ResolveAlertEndpoint(ctx context.Context, tenantID, connID string) (string, error)
}

// sender is the subset of *alerts.Notifier this package needs, named locally so
// it can be faked in tests.
type sender interface {
	Send(ctx context.Context, channelType, url, message string, ev alerts.Event) error
}

// Dispatcher implements scheduler.Alerter by resolving and sending a run's
// on-failure alert rules.
type Dispatcher struct {
	notifier sender
	resolver EndpointResolver
	logger   *slog.Logger
}

// New builds a Dispatcher over the given notifier, connection resolver, and
// logger.
func New(notifier sender, resolver EndpointResolver, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{notifier: notifier, resolver: resolver, logger: logger}
}

// AlertRunFailed dispatches every on_failure rule of a failed run. Each rule is
// resolved, rendered, and sent independently: a resolver or send error on one
// rule is logged and skipped so the remaining rules still fire (best-effort).
func (d *Dispatcher) AlertRunFailed(ctx context.Context, run scheduler.RunState) {
	if run.Alerts == nil {
		return
	}
	ev := alerts.Event{DagID: run.DagID, RunID: run.RunID, FailedTasks: failedTasks(run)}
	for _, rule := range run.Alerts.OnFailure {
		url, err := d.resolver.ResolveAlertEndpoint(ctx, run.TenantID, rule.Conn)
		if err != nil {
			d.logger.Error("resolving alert connection",
				"dag", run.DagID, "run", run.RunID, "conn", rule.Conn, "error", err)
			continue
		}
		message := alerts.Render(rule.Message, ev)
		if serr := d.notifier.Send(ctx, rule.Type, url, message, ev); serr != nil {
			d.logger.Error("sending on-failure alert",
				"dag", run.DagID, "run", run.RunID, "type", rule.Type, "conn", rule.Conn, "error", serr)
			continue
		}
		d.logger.Info("on-failure alert sent",
			"dag", run.DagID, "run", run.RunID, "type", rule.Type, "conn", rule.Conn)
	}
}

// failedTasks returns the task_ids that actually failed (not merely upstream-
// failed), in DAG order, so the message template names a real culprit.
func failedTasks(run scheduler.RunState) []string {
	var out []string
	for _, t := range run.Tasks {
		if run.States[t.TaskID] == domain.TaskStateFailed {
			out = append(out, t.TaskID)
		}
	}
	return out
}
