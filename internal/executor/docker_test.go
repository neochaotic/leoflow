package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fakeDocker struct {
	created   *container.Config
	startedID string
	createErr error
}

func (f *fakeDocker) ContainerCreate(_ context.Context, config *container.Config, _ *container.HostConfig,
	_ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	if f.createErr != nil {
		return container.CreateResponse{}, f.createErr
	}
	f.created = config
	return container.CreateResponse{ID: "c123"}, nil
}

func (f *fakeDocker) ContainerStart(_ context.Context, containerID string, _ container.StartOptions) error {
	f.startedID = containerID
	return nil
}

func TestContainerConfig(t *testing.T) {
	cfg := containerConfig(Request{
		Image: "img:v1", DagID: "etl", TaskID: "extract", RunID: "r1", TaskInstanceID: "ti-1",
		ControlPlaneAddr: "cp:9000", AgentToken: "tok",
	})
	if cfg.Image != "img:v1" {
		t.Errorf("image = %q", cfg.Image)
	}
	if cfg.Labels["leoflow.io/task-instance-id"] != "ti-1" {
		t.Errorf("labels = %v", cfg.Labels)
	}
	var hasAddr bool
	for _, e := range cfg.Env {
		if e == "LEOFLOW_CONTROL_PLANE_ADDR=cp:9000" {
			hasAddr = true
		}
	}
	if !hasAddr {
		t.Errorf("agent env not injected: %v", cfg.Env)
	}
}

func TestDockerExecutorCreatesAndStarts(t *testing.T) {
	f := &fakeDocker{}
	if err := NewDockerExecutor(f).Execute(context.Background(), Request{Image: "img:v1", TaskID: "t"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if f.created == nil || f.created.Image != "img:v1" {
		t.Error("container not created with the task image")
	}
	if f.startedID != "c123" {
		t.Errorf("started container = %q, want c123", f.startedID)
	}
}

func TestDockerExecutorCreateError(t *testing.T) {
	f := &fakeDocker{createErr: errors.New("daemon down")}
	if err := NewDockerExecutor(f).Execute(context.Background(), Request{Image: "img:v1", TaskID: "t"}); err == nil {
		t.Error("create failure should surface as an error")
	}
}
