package scheduler

import (
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// dispatchErrorClass tells the planner why a synchronous dispatch failed, so it
// can hold transient cluster backpressure apart from a permanent condition. See
// ADR 0053.
type dispatchErrorClass int

const (
	// dispatchPermanent is a dispatch failure that will not clear on its own: an
	// invalid image, an RBAC denial, an admission-webhook rejection, a bad spec,
	// or any error the classifier does not recognize (including every error a Lite
	// subprocess executor can return). It keeps the historical bounded-backoff →
	// dispatch_failed behavior (ADR 0031 Amendment A).
	dispatchPermanent dispatchErrorClass = iota
	// dispatchRetriableForever is a dispatch failure caused by transient cluster
	// backpressure from the Kubernetes apiserver — a ResourceQuota 403 or an API
	// Priority & Fairness 429 — that clears once the cluster has headroom again.
	// It is backed off and re-offered indefinitely, never counts against the
	// dispatch-attempt budget, and never drives a task to dispatch_failed: the
	// cluster asking Leoflow to slow down is not the user's task failing.
	dispatchRetriableForever
)

// classifyDispatchError decides whether a synchronous dispatch failure is
// transient cluster backpressure (retry forever, off the attempt budget) or a
// permanent condition (bounded retry → dispatch_failed). Only Kubernetes
// apiserver error types can yield the retriable-forever verdict; every other
// error is permanent, which preserves today's path exactly for the Lite
// subprocess executor — its errors are never apiserver StatusErrors, so its
// dispatch handling is unreachable by the retriable-forever branch (ADR 0053).
//
// The two backpressure signals:
//   - 429 Too Many Requests: API Priority & Fairness shed the request. Always
//     transient.
//   - 403 Forbidden carrying the ResourceQuota admission plugin's "exceeded
//     quota" marker: the namespace quota is full. Transient — it clears as the
//     namespace's running pods complete.
//
// A 403 Forbidden WITHOUT the quota marker is an RBAC denial: permanent, and
// deliberately not conflated with quota (apierrors.IsForbidden covers both — the
// message is the only discriminator). An admission-webhook rejection ("denied the
// request") also arrives as Forbidden and is likewise treated as permanent: a
// webhook that rejects a spec rejects every re-offer of that same spec, so
// retrying forever would loop silently — a bounded retry surfaces it as
// dispatch_failed instead. A genuinely transient webhook (call timeout under a
// Fail policy) is not special-cased; it falls to the permanent default, matching
// today's behavior, rather than risking an infinite re-offer loop on a poison
// spec.
func classifyDispatchError(err error) dispatchErrorClass {
	if err == nil {
		// Not a real input: handleDispatchFailure is only called on a non-nil
		// dispatch error. Guarded so the classifier is total and defaults to the
		// safe (bounded) path.
		return dispatchPermanent
	}
	if apierrors.IsTooManyRequests(err) {
		return dispatchRetriableForever
	}
	if apierrors.IsForbidden(err) && isQuotaExceeded(err) {
		return dispatchRetriableForever
	}
	return dispatchPermanent
}

// isQuotaExceeded reports whether a Forbidden error is a ResourceQuota rejection
// rather than an RBAC denial. Both surface as reason=Forbidden, so the message is
// the discriminator: the ResourceQuota admission plugin formats its rejection as
// "exceeded quota: <name>, requested: ...", while an RBAC denial reads "... cannot
// create resource ...", which lacks the marker. The caller gates this on
// IsForbidden, so a non-apiserver error whose text merely mentions quota can
// never reach it.
func isQuotaExceeded(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "exceeded quota")
}
