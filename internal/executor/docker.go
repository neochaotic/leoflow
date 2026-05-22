package executor

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// dockerClient is the subset of the Docker SDK that DockerExecutor uses. The
// real *client.Client satisfies it (asserted below), and tests use a fake.
type dockerClient interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig,
		networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
}

var _ dockerClient = (*client.Client)(nil)

// DockerExecutor runs each task as an ephemeral Docker container running the
// agent. Same shape as the Kubernetes executor (ADR 0002), via the Docker SDK.
type DockerExecutor struct {
	cli dockerClient
}

// NewDockerExecutor builds a DockerExecutor over a Docker API client.
func NewDockerExecutor(cli dockerClient) *DockerExecutor {
	return &DockerExecutor{cli: cli}
}

func containerConfig(req Request) *container.Config {
	return &container.Config{
		Image: req.Image,
		Env:   agentEnv(req),
		Labels: map[string]string{
			"leoflow.io/dag-id":           sanitizeLabel(req.DagID),
			"leoflow.io/task-id":          sanitizeLabel(req.TaskID),
			"leoflow.io/run-id":           sanitizeLabel(req.RunID),
			"leoflow.io/task-instance-id": req.TaskInstanceID,
		},
	}
}

// Execute creates and starts the task container; the agent inside reports state.
func (e *DockerExecutor) Execute(ctx context.Context, req Request) error {
	resp, err := e.cli.ContainerCreate(ctx, containerConfig(req), &container.HostConfig{}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("creating container for task %s: %w", req.TaskID, err)
	}
	if err := e.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("starting container %s: %w", resp.ID, err)
	}
	return nil
}
