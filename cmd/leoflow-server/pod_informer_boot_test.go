package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/neochaotic/leoflow/internal/config"
)

// TestPodInformer_BootSurvivesACacheThatCannotSync is #1083. The pod informer's
// cache warm-up is an optimization: PodInformer.WaitForCacheSync documents that a
// false return "is not fatal", and CachedPodActive gates on HasSynced so consumers
// fall back to live reads until the cache warms. That design is only reachable if
// boot can reach it.
//
// It could not. buildPodInformer passed the PROCESS-lifetime context, and
// cache.WaitForCacheSync returns only on sync or on that context's cancel — which
// happens at shutdown. So a cache that cannot sync held the boot goroutine before
// startAPISide returned, and the HTTP and metrics listeners never bound. On a real
// cluster that is a ServiceAccount without list/watch on pods: the control plane
// serves gRPC, never binds /readyz, fails its liveness probe on the same port, and
// is killed and restarted about every 70s with each cycle recorded as
// `Completed exit=0` — a dependency failure that reports itself as success.
//
// Forbidden is the faithful shape: the reflector retries it forever, so the cache
// never syncs while every call still returns promptly.
func TestPodInformer_BootSurvivesACacheThatCannotSync(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "pods"}, "", errors.New("RBAC: no list on pods"))
	})

	// Shorten the production budget so the suite does not pay it. The assertion is
	// that a deadline EXISTS and is honored — without one the call never returns,
	// whatever this is set to.
	restore := podInformerSyncTimeout
	podInformerSyncTimeout = 150 * time.Millisecond
	t.Cleanup(func() { podInformerSyncTimeout = restore })

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	cfg := &config.ServerConfig{}
	cfg.Server.Role = config.RoleAll
	cfg.Executor.Type = "kubernetes"
	cfg.Executor.TaskNamespace = "leoflow-tasks"

	done := make(chan struct{})
	go func() {
		defer close(done)
		buildPodInformer(context.Background(), cfg, cs, log)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("buildPodInformer did not return: boot is blocked behind a cache that cannot sync (#1083)")
	}

	// Diagnosability is the other half. A bounded wait that stays silent leaves the
	// operator with a probe failure and no cause, which is the state #1083 describes.
	got := logBuf.String()
	for _, want := range []string{"leoflow-tasks", "list"} {
		if !strings.Contains(got, want) {
			t.Errorf("the timeout log does not name %q, so it cannot point at the cause:\n%s", want, got)
		}
	}
}
