package scheduler

import (
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// podsGR is the GroupResource a pod CREATE is rejected against, matching what the
// apiserver puts in a real ResourceQuota / RBAC Forbidden status.
var podsGR = schema.GroupResource{Group: "", Resource: "pods"}

// quota403 builds the Forbidden error the ResourceQuota admission plugin returns
// when a pod CREATE would breach a namespace quota: reason=Forbidden, message
// carrying the canonical "exceeded quota:" marker.
func quota403() error {
	return apierrors.NewForbidden(podsGR, "etl-a-0-abcd1234", errors.New(
		"exceeded quota: compute-resources, requested: requests.cpu=1, used: requests.cpu=4, limited: requests.cpu=4"))
}

// rbac403 builds the Forbidden error the apiserver returns when RBAC denies the
// pod CREATE: reason=Forbidden as well, but the message is an authorization
// denial with no quota marker — the case the classifier must NOT conflate.
func rbac403() error {
	return apierrors.NewForbidden(podsGR, "", errors.New(
		`User "system:serviceaccount:leoflow:executor" cannot create resource "pods" in API group "" in the namespace "prod"`))
}

// admissionDenied builds a validating-admission-webhook rejection. The apiserver
// surfaces it as Forbidden with the canonical "denied the request" phrasing.
func admissionDenied() error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    403,
		Reason:  metav1.StatusReasonForbidden,
		Message: `admission webhook "validate.example.com" denied the request: pods using hostNetwork are not allowed`,
	}}
}

// TestClassifyDispatchError pins the classification each dispatch-failure shape
// gets: only genuine cluster backpressure (quota 403, APF 429) is
// retriable-forever; RBAC denials, admission-webhook rejections, and every
// unrecognized error keep the permanent (bounded → dispatch_failed) path.
func TestClassifyDispatchError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want dispatchErrorClass
	}{
		{"quota_403_retriable", quota403(), dispatchRetriableForever},
		{"quota_403_wrapped_retriable", fmt.Errorf("creating pod for task a: %w", quota403()), dispatchRetriableForever},
		{"apf_429_retriable", apierrors.NewTooManyRequests("Priority and Fairness: request rejected", 1), dispatchRetriableForever},
		{"apf_429_wrapped_retriable", fmt.Errorf("creating pod for task a: %w", apierrors.NewTooManyRequests("APF", 1)), dispatchRetriableForever},
		{"rbac_403_permanent", rbac403(), dispatchPermanent},
		{"admission_denied_permanent", admissionDenied(), dispatchPermanent},
		{"invalid_spec_permanent", apierrors.NewInvalid(schema.GroupKind{Kind: "Pod"}, "a", nil), dispatchPermanent},
		{"generic_error_permanent", errors.New("kube-apiserver unreachable"), dispatchPermanent},
		// A plain (non-apiserver) error whose text happens to mention quota must
		// stay permanent: the quota verdict keys on the Forbidden status, never on
		// message text alone, so a Lite subprocess error can never trip it.
		{"non_apierror_quota_text_permanent", errors.New("job failed: exceeded quota on disk"), dispatchPermanent},
		{"nil_permanent", nil, dispatchPermanent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDispatchError(tc.err); got != tc.want {
				t.Errorf("classifyDispatchError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
