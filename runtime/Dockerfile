# Leoflow task base image.
#
# A DAG image is built FROM this base (ADR 0003): it provides the leoflow-agent
# (PID 1, talks gRPC to the control plane) and the leoflow_runtime Python helper
# (runs the user callable, bridges XCom). The DAG author adds their code and
# Python dependencies on top.
#
# Build from the repository root so both the Go source and runtime/python are in
# context:
#   docker build -f runtime/Dockerfile \
#     --build-arg PYTHON_VERSION=3.11 -t leoflow-base:py3.11 .
#
# PYTHON_VERSION selects the interpreter (3.10 / 3.11 / 3.12).

ARG GO_VERSION=1.26.3
ARG PYTHON_VERSION=3.11

# ── Stage 1: build the static agent binary ───────────────────────────────────
FROM golang:${GO_VERSION}-bookworm AS agent-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/leoflow-agent ./cmd/leoflow-agent

# ── Stage 2: runtime base ─────────────────────────────────────────────────────
FROM python:${PYTHON_VERSION}-slim-bookworm

LABEL org.opencontainers.image.source="https://github.com/neochaotic/leoflow"
LABEL org.opencontainers.image.description="Leoflow task base image (agent + Python runtime)"
LABEL org.opencontainers.image.licenses="Apache-2.0"

# Run as a non-root user; user code runs under this account.
RUN useradd --create-home --uid 1000 leoflow

COPY --from=agent-build /out/leoflow-agent /usr/local/bin/leoflow-agent
COPY runtime/python /opt/leoflow-runtime
RUN pip install --no-cache-dir /opt/leoflow-runtime

# Leoflow DAGs are authored with the Apache Airflow Task SDK (`from airflow.sdk
# import DAG, task`), so the task container must be able to import it to load the
# user's dag.py. This is the lightweight Task SDK, not airflow-core, pinned to
# the 3.2.x line Leoflow targets (ADR 0003 / parser SDK version).
ARG AIRFLOW_TASK_SDK_VERSION=1.2.1
RUN pip install --no-cache-dir "apache-airflow-task-sdk==${AIRFLOW_TASK_SDK_VERSION}"

USER leoflow
WORKDIR /home/leoflow

# The agent is PID 1; it fetches the task spec and runs the user code.
ENTRYPOINT ["leoflow-agent"]
