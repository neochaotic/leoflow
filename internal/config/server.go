package config

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// ServerConfig is the full configuration for the leoflow-server control plane.
// It mirrors the nested YAML described in the Phase 2 prompt.
type ServerConfig struct {
	Server        ServerSection        `mapstructure:"server"`
	Database      DatabaseSection      `mapstructure:"database"`
	Redis         RedisSection         `mapstructure:"redis"`
	Auth          AuthSection          `mapstructure:"auth"`
	Scheduler     SchedulerSection     `mapstructure:"scheduler"`
	Executor      ExecutorSection      `mapstructure:"executor"`
	Logs          LogsSection          `mapstructure:"logs"`
	Observability ObservabilitySection `mapstructure:"observability"`
	UI            UISection            `mapstructure:"ui"`
	// SecretKey (LEOFLOW_SECRET_KEY) is the 32-byte key encrypting connection
	// secrets at rest (ADR 0019). Raw 32 chars, 64-char hex, or base64. Empty
	// disables connection writes.
	SecretKey string `mapstructure:"secret_key"`
}

// LogsSection configures task log shipping.
type LogsSection struct {
	// Dir is the root directory for the disk log sink (the default backend).
	Dir string `mapstructure:"dir"`
	// Backend selects the durable task-log store: "disk" (default) writes files
	// under Dir; "object" ships each attempt to an S3-compatible bucket. Object
	// storage is opt-in — Lite and every deployment that does not set this keep
	// the exact on-disk path unchanged.
	Backend string `mapstructure:"backend"`
	// Object configures the object-store backend; read only when Backend is
	// "object".
	Object ObjectLogSection `mapstructure:"object"`
}

// ObjectLogSection configures the S3-compatible object-store log backend. It
// targets AWS S3, Google Cloud Storage (via its S3 interop endpoint), and any
// S3-compatible store (MinIO, Ceph RGW). Auth is keyless-first (ADR 0035):
// leave the key fields empty to use the ambient credential chain (IRSA /
// instance profile / GKE Workload Identity + HMAC / env). Static keys are a
// discouraged escape hatch for dev and stores without an identity broker.
type ObjectLogSection struct {
	// Bucket is the target bucket. Required when Backend is "object".
	Bucket string `mapstructure:"bucket"`
	// Prefix is an optional key prefix under which attempt objects are laid out.
	Prefix string `mapstructure:"prefix"`
	// Region is the store region (e.g. "us-east-1"). Required by AWS S3; ignored
	// by some S3-compatible stores.
	Region string `mapstructure:"region"`
	// Endpoint overrides the S3 endpoint for S3-compatible stores: Google Cloud
	// Storage interop ("https://storage.googleapis.com") or MinIO. Empty uses the
	// AWS default endpoint.
	Endpoint string `mapstructure:"endpoint"`
	// ForcePathStyle uses path-style addressing (bucket in the path, not the
	// host). Required by MinIO and some S3-compatible stores.
	ForcePathStyle bool `mapstructure:"force_path_style"`
	// AccessKeyID is a static access key. Empty (recommended) uses the keyless
	// credential chain (ADR 0035).
	AccessKeyID string `mapstructure:"access_key_id"`
	// SecretAccessKey pairs with AccessKeyID. Discouraged; prefer keyless.
	SecretAccessKey string `mapstructure:"secret_access_key"`
}

// ExecutorSection configures how tasks are executed.
type ExecutorSection struct {
	HTTP HTTPExecutorSection `mapstructure:"http"`
	// TaskNamespace is the Kubernetes namespace the server creates task pods and
	// per-run staging PVCs in. It MUST match the namespace the Helm chart grants
	// the executor Role in (chart `taskNamespace` → LEOFLOW_EXECUTOR_TASK_NAMESPACE);
	// a mismatch 403s every dispatch (#480). Defaults to "leoflow".
	TaskNamespace string `mapstructure:"task_namespace"`
	// Type selects the pod-path executor: "kubernetes" (default, pod-per-task) or
	// "subprocess" (dev only, runs the agent on the host without isolation, used
	// by `leoflow dev`).
	Type string `mapstructure:"type"`
	// AgentPath is the leoflow-agent binary the subprocess executor runs (dev only).
	AgentPath string `mapstructure:"agent_path"`
	// SubprocessWorkDir is the working directory the subprocess executor runs the
	// agent in, so it can import the project's dag.py (dev only). Empty keeps the
	// server's working directory.
	SubprocessWorkDir string `mapstructure:"subprocess_workdir"`
	// AgentControlPlaneAddr is the gRPC address task pods dial back to. Empty
	// falls back to server.grpc_addr; in a local k3d/kind cluster set it to a
	// host-reachable address such as host.k3d.internal:9091.
	AgentControlPlaneAddr string `mapstructure:"agent_control_plane_addr"`
	// AgentTLSCAConfigMap names a ConfigMap (key ca.crt) mounted into task pods so
	// the agent verifies the control plane's gRPC TLS cert (issue #58). Empty =
	// agents use the insecure channel (dev).
	AgentTLSCAConfigMap string `mapstructure:"agent_tls_ca_configmap"`
	// TaskSecretName names a Kubernetes Secret mounted (read-only) into every task
	// pod at TaskSecretMountPath. It lets a task read a credential that lives in
	// the cluster's secret store (e.g. a GCP service-account key) referenced by a
	// connection's key_path — so Leoflow never stores the key itself (ADR 0035).
	// Empty = no secret mounted.
	TaskSecretName string `mapstructure:"task_secret_name"`
	// TaskSecretMountPath is where TaskSecretName is mounted in the task pod.
	TaskSecretMountPath string `mapstructure:"task_secret_mount_path"`
	// Defaults holds per-cluster task defaults applied at dispatch to fill gaps the
	// DAG artifact left empty (ADR 0023, layer L0). They never override a value
	// baked into dag.json, keeping the artifact portable across clusters.
	Defaults PlatformDefaultsSection `mapstructure:"defaults"`
}

// PlatformDefaultsSection configures the lowest-precedence (L0) task defaults,
// applied at dispatch to fill gaps the DAG left empty (ADR 0023).
type PlatformDefaultsSection struct {
	// StagingSize/StagingStorageClass default the per-run staging volume when the
	// DAG enabled staging without pinning them (e.g. the cluster's RWX class).
	StagingSize         string `mapstructure:"staging_size"`
	StagingStorageClass string `mapstructure:"staging_storage_class"`
	// StagingAccessMode is the PVC access mode for the staging volume. Defaults to
	// ReadWriteMany (multi-node prod); single-node dev (k3d local-path, no RWX)
	// sets ReadWriteOnce, which is sufficient for a run's sequential same-node pods.
	StagingAccessMode string `mapstructure:"staging_access_mode"`
	// ResourcesCPU/ResourcesMemory default a task's request when neither the task
	// override nor the DAG set any (Kubernetes quantities, e.g. "250m"/"256Mi").
	ResourcesCPU    string `mapstructure:"resources_cpu"`
	ResourcesMemory string `mapstructure:"resources_memory"`
	// RunTasksAsNonRoot refuses to start a task container whose image resolves
	// to UID 0, completing Pod Security Admission's `restricted` set. On by
	// default now that the images this repo ships carry a numeric non-root UID:
	// runtime/Dockerfile runs as `USER 65532:65532` and every examples/*/image
	// inherits it, and the executor pairs it with a pod-level fsGroup so the
	// per-run staging PVC stays writable. Turn it off for a cluster whose task
	// images legitimately run as root.
	//
	// Deliberately a cluster setting rather than a DAG field: whether untrusted
	// task code may run as root belongs to whoever operates the cluster, not to
	// whoever authors the DAG.
	RunTasksAsNonRoot bool `mapstructure:"run_tasks_as_non_root"`
	// ReadOnlyTaskRootFilesystem mounts every task container's root filesystem
	// read-only. Off by default because `restricted` does not require it and it
	// breaks ordinary Python tasks (pip cache, /tmp, matplotlib config); turn it
	// on for a fleet of tasks known not to write outside their volumes.
	ReadOnlyTaskRootFilesystem bool `mapstructure:"read_only_task_root_filesystem"`
}

// UISection configures the embedded Airflow UI.
type UISection struct {
	// InstanceName is shown in the UI navbar (Airflow's instance_name). Empty
	// falls back to "Leoflow"; `leoflow lite` sets it to mark the environment.
	InstanceName string `mapstructure:"instance_name"`
	// AutoRefreshIntervalSeconds is the SPA's polling cadence for DAG /
	// DagRun / task-instance state refresh (Airflow's auto_refresh_interval).
	// Zero (the default) falls back to api.DefaultUIAutoRefreshIntervalSeconds
	// (30s, production-safe). `leoflow lite` sets it to 1s for a snappy inner
	// loop so the SPA reflects state changes almost immediately during dev.
	AutoRefreshIntervalSeconds int `mapstructure:"auto_refresh_interval_seconds"`
	// Edition marks the running edition; "lite" shows the silver LITE badge and
	// "pro" shows the gold PRO badge in the UI shell (independent of the auth
	// mode). Empty/any other value shows no badge — Demo intentionally renders
	// without an edition pill.
	Edition string `mapstructure:"edition"`
	// Workspace is the DAG project directory the Lite web editor edits (ADR 0025).
	// Empty disables the editor (Production, or Lite without one).
	Workspace string `mapstructure:"workspace"`
	// MonacoDir is where the pinned Monaco bundle was fetched by `leoflow setup`;
	// the editor page is served Monaco from it. Empty shows a setup hint.
	MonacoDir string `mapstructure:"monaco_dir"`
}

// HTTPExecutorSection configures HTTP-related executor knobs.
type HTTPExecutorSection struct {
	// UserAgent is the default User-Agent header for HTTP requests a task image
	// may make on the platform's behalf.
	UserAgent string `mapstructure:"user_agent"`
}

// ServerSection configures the HTTP, metrics, and agent gRPC listeners.
type ServerSection struct {
	// Role selects which components this process runs (ADR 0049): "all" (default;
	// the monolith Lite always runs), "api" (HTTP + UI, restricted identity), or
	// "scheduler" (reconciler + dispatch + agent gRPC, privileged). Splitting is a
	// Pro-only topology; "all" is behavior-identical to the pre-0049 monolith.
	Role        string      `mapstructure:"role"`
	HTTPAddr    string      `mapstructure:"http_addr"`
	MetricsAddr string      `mapstructure:"metrics_addr"`
	GRPCAddr    string      `mapstructure:"grpc_addr"`
	CORS        CORSSection `mapstructure:"cors"`
	// TrustedProxies lists the proxy IPs/CIDRs whose X-Forwarded-For is honored
	// when resolving the client IP. Empty (the default) trusts no proxy, so a
	// spoofed XFF cannot forge the client IP (audit H1); set it to the ingress
	// CIDR when the API runs behind a reverse proxy so rate-limiting and audit
	// see the real client.
	TrustedProxies []string `mapstructure:"trusted_proxies"`
	// GRPCTLSCert/GRPCTLSKey enable TLS on the agent gRPC listener (issue #58).
	// When both are set the channel is encrypted; empty means plaintext (dev).
	GRPCTLSCert string `mapstructure:"grpc_tls_cert"`
	GRPCTLSKey  string `mapstructure:"grpc_tls_key"`
}

// Server roles (ADR 0049).
const (
	// RoleAll runs every component in one process (the default; Lite's only mode).
	RoleAll = "all"
	// RoleAPI runs the HTTP API + UI only (restricted network identity).
	RoleAPI = "api"
	// RoleScheduler runs the reconciler + dispatch + agent gRPC (privileged).
	RoleScheduler = "scheduler"
)

// EffectiveRole returns the configured role, defaulting empty to RoleAll so an
// unset role (Lite, and every pre-0049 deployment) keeps the monolith behavior.
func (s ServerSection) EffectiveRole() string {
	if s.Role == "" {
		return RoleAll
	}
	return s.Role
}

// ServesAPI reports whether this process runs the HTTP API + UI.
func (s ServerSection) ServesAPI() bool {
	r := s.EffectiveRole()
	return r == RoleAll || r == RoleAPI
}

// ServesScheduler reports whether this process runs the scheduler, dispatch, and
// the agent gRPC endpoint.
func (s ServerSection) ServesScheduler() bool {
	r := s.EffectiveRole()
	return r == RoleAll || r == RoleScheduler
}

// CORSSection configures cross-origin access.
type CORSSection struct {
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

// DatabaseSection configures the Postgres connection pool.
type DatabaseSection struct {
	URL          string `mapstructure:"url"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
}

// RedisSection configures the Redis connection.
type RedisSection struct {
	URL string `mapstructure:"url"`
	// CAFile is the absolute path to a PEM CA bundle the client trusts when
	// negotiating TLS to a `rediss://` URL (#312). Required to reach managed
	// Redis (Memorystore SERVER_AUTHENTICATION, ElastiCache in-transit
	// encryption, Azure Cache for Redis) whose server cert is signed by a
	// provider / per-instance CA that is not in the container's system
	// roots. Empty falls back to the SDK default — system roots only.
	// The Helm chart sets this via LEOFLOW_REDIS_CA_FILE when
	// `redis.caConfigMap` is configured, pointing at the mounted ConfigMap.
	CAFile string `mapstructure:"ca_file"`
}

// AuthSection configures authentication.
type AuthSection struct {
	Provider string     `mapstructure:"provider"`
	JWT      JWTSection `mapstructure:"jwt"`
	// DevNoAuth disables authentication entirely, treating every request as an
	// admin. It exists ONLY for `leoflow dev` (local, unsandboxed). It is false by
	// default and the server logs a prominent warning when it is on. NEVER set
	// this in production (LEOFLOW_AUTH_DEV_NO_AUTH).
	DevNoAuth bool `mapstructure:"dev_no_auth"`
	// LoginRateLimitPerMinute caps failed /auth/token attempts per client IP per
	// minute (anti-brute-force). Only failures count, so a successful login never
	// consumes budget. Lite raises this well above the production default because
	// it is a local single-user tool where lockouts are pure friction.
	LoginRateLimitPerMinute int `mapstructure:"login_rate_limit_per_minute"`
}

// JWTSection configures JWT issuance and validation.
type JWTSection struct {
	Secret          string `mapstructure:"secret"`
	TokenTTLSeconds int    `mapstructure:"token_ttl_seconds"`
}

// SchedulerSection configures the scheduler loop.
type SchedulerSection struct {
	LoopIntervalMS int             `mapstructure:"loop_interval_ms"`
	Enabled        bool            `mapstructure:"enabled"`
	Dispatch       DispatchSection `mapstructure:"dispatch"`
}

// DispatchSection sizes the BufferedDispatcher (#127). BufferSize=0 keeps the
// scheduler tick synchronous with the inner dispatcher — the right shape for
// Lite (subprocess fork is microseconds). BufferSize>0 enables the worker
// pool — the right shape for Pro (Kubernetes API calls add real latency).
// The defaults are set per-edition by configsetup so the user does not have
// to think about this; an operator can still tune the knobs.
type DispatchSection struct {
	// BufferSize is the depth of the queued-dispatches channel. 0 disables the
	// pool (synchronous passthrough). A full channel returns ErrAtCapacity to
	// the scheduler, which leaves the TI scheduled for the next tick.
	BufferSize int `mapstructure:"buffer_size"`
	// Workers is the number of goroutines draining the queue. Ignored when
	// BufferSize <= 0; otherwise floored to 1.
	Workers int `mapstructure:"workers"`
}

// ObservabilitySection configures logging, metrics, and tracing.
type ObservabilitySection struct {
	OTel      OTelSection `mapstructure:"otel"`
	LogLevel  string      `mapstructure:"log_level"`
	LogFormat string      `mapstructure:"log_format"`
}

// OTelSection configures OpenTelemetry export.
type OTelSection struct {
	Enabled  bool   `mapstructure:"enabled"`
	Endpoint string `mapstructure:"endpoint"`
}

// serverDefaults lists every leaf key with its default so that AutomaticEnv and
// Unmarshal resolve nested keys correctly.
var serverDefaults = map[string]any{
	// Empty defaults to RoleAll (EffectiveRole). This entry must exist so viper's
	// AutomaticEnv binds LEOFLOW_SERVER_ROLE — see the ui.auto_refresh note below.
	"server.role":                 "",
	"server.http_addr":            "0.0.0.0:8080",
	"server.metrics_addr":         "0.0.0.0:9090",
	"server.grpc_addr":            "0.0.0.0:9091",
	"server.grpc_tls_cert":        "",
	"server.grpc_tls_key":         "",
	"server.cors.allowed_origins": []string{"http://localhost:8080"},
	"database.url":                "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable",
	"database.max_open_conns":     25,
	"database.max_idle_conns":     5,
	// Empty by default: no Redis configured selects the embedded edition (Lite —
	// XCom on Postgres, in-process log tailer, ADR 0026). Production sets this
	// explicitly via the Helm chart (external Redis).
	"redis.url":                        "",
	"auth.provider":                    "jwt",
	"auth.jwt.secret":                  "",
	"auth.jwt.token_ttl_seconds":       3600,
	"auth.login_rate_limit_per_minute": 5,
	"scheduler.loop_interval_ms":       1000,
	"scheduler.enabled":                true,
	// Default: synchronous dispatch (BufferSize=0). Safe and zero-overhead for
	// Lite. Pro deployments should set buffer_size>=1 + workers>=1 in their
	// values.yaml so K8s API latency does not stretch the tick (#127, ADR 0031).
	"scheduler.dispatch.buffer_size":                   0,
	"scheduler.dispatch.workers":                       0,
	"executor.http.user_agent":                         "leoflow/0.1",
	"executor.task_namespace":                          "leoflow",
	"executor.type":                                    "kubernetes",
	"executor.agent_path":                              "leoflow-agent",
	"executor.subprocess_workdir":                      "",
	"executor.agent_control_plane_addr":                "",
	"executor.agent_tls_ca_configmap":                  "",
	"executor.task_secret_name":                        "",
	"executor.task_secret_mount_path":                  "/etc/leoflow/secrets",
	"executor.defaults.staging_access_mode":            "ReadWriteMany",
	"executor.defaults.run_tasks_as_non_root":          true,
	"executor.defaults.read_only_task_root_filesystem": false,
	"logs.dir":                      "/var/log/leoflow",
	"logs.backend":                  "disk",
	"logs.object.bucket":            "",
	"logs.object.prefix":            "",
	"logs.object.region":            "",
	"logs.object.endpoint":          "",
	"logs.object.force_path_style":  false,
	"logs.object.access_key_id":     "",
	"logs.object.secret_access_key": "",
	"observability.otel.enabled":    true,
	"observability.otel.endpoint":   "localhost:4317",
	"observability.log_level":       "info",
	"observability.log_format":      "json",
	"ui.instance_name":              "Leoflow",
	"ui.edition":                    "",
	"ui.workspace":                  "",
	"ui.monaco_dir":                 "",
	// Must appear here even though the zero value is meaningful (the handler
	// falls back to api.DefaultUIAutoRefreshIntervalSeconds when ≤ 0): viper's
	// AutomaticEnv only binds env vars for keys it has seen via SetDefault or
	// SetConfigFile. Without this line LEOFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS
	// was silently dropped, so `leoflow lite` (which exports the env var to
	// poll every 1s) was actually running at the 30s production default.
	"ui.auto_refresh_interval_seconds": 0,
	"auth.dev_no_auth":                 false,
	"secret_key":                       "",
}

// LoadServer assembles the server configuration from defaults, the given file,
// LEOFLOW_* environment variables, and flags, in increasing precedence.
func LoadServer(configFile string, flags *pflag.FlagSet) (*ServerConfig, error) {
	v := viper.New()
	for key, val := range serverDefaults {
		v.SetDefault(key, val)
	}
	v.SetEnvPrefix("LEOFLOW")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config file %q: %w", configFile, err)
		}
	}
	if flags != nil {
		if err := v.BindPFlags(flags); err != nil {
			return nil, fmt.Errorf("binding flags: %w", err)
		}
	}

	var c ServerConfig
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unmarshaling server config: %w", err)
	}
	return &c, nil
}

// Auth providers (auth.provider allowlist). "jwt" is the only implemented
// authenticator; "oidc" is a recognized-but-unimplemented value that is
// rejected at boot rather than silently falling back to JWT.
const (
	// AuthProviderJWT is the only auth.provider main.go can build an
	// Authenticator for today.
	AuthProviderJWT = "jwt"
	// AuthProviderOIDC is a declared future provider with no implementation;
	// selecting it is a boot failure, not a silent JWT fallback.
	AuthProviderOIDC = "oidc"
)

// Validate reports configuration errors that must abort startup.
func (c *ServerConfig) Validate() error {
	if err := c.validateRole(); err != nil {
		return err
	}
	if err := c.validateProvider(); err != nil {
		return err
	}
	if err := c.validateLogs(); err != nil {
		return err
	}
	if c.Auth.Provider == AuthProviderJWT && c.Auth.JWT.Secret == "" {
		return errors.New("auth.jwt.secret is required (set LEOFLOW_AUTH_JWT_SECRET)")
	}
	// auth.dev_no_auth disables authentication entirely; permit it only when the
	// HTTP API binds to loopback, so a misconfigured (or accidental) dev bypass can
	// never expose an unauthenticated API off-host. Fail closed otherwise.
	if c.Auth.DevNoAuth && !isLoopbackListenAddr(c.Server.HTTPAddr) {
		return fmt.Errorf("auth.dev_no_auth disables authentication and is only permitted on a loopback http_addr (got %q); never enable it in production", c.Server.HTTPAddr)
	}
	return nil
}

// isLoopbackListenAddr reports whether a listen address binds only to loopback,
// so a no-auth dev server is unreachable off-host.
func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateLogs rejects an unknown logs.backend and requires a bucket when the
// object backend is selected, so a misconfigured object sink fails closed at
// boot instead of losing every task log to a nonexistent bucket. Empty and
// "disk" are always valid — the on-disk default is unaffected.
func (c *ServerConfig) validateLogs() error {
	switch c.Logs.Backend {
	case "", "disk":
		return nil
	case "object":
		if c.Logs.Object.Bucket == "" {
			return errors.New(`logs.object.bucket is required when logs.backend is "object" (set LEOFLOW_LOGS_OBJECT_BUCKET)`)
		}
		return nil
	default:
		return fmt.Errorf(`unknown logs.backend %q (want "disk" or "object")`, c.Logs.Backend)
	}
}

// validateProvider rejects an unknown or unimplemented auth.provider, failing
// closed at boot instead of letting main.go build a JWTAuthenticator regardless
// of what was configured. Empty is valid: serverDefaults sets auth.provider to
// "jwt", so an unset provider in an existing config keeps defaulting to JWT and
// is unaffected. "oidc" is recognized but has no implementation, so it is
// rejected with an actionable hint rather than a silent JWT fallback.
func (c *ServerConfig) validateProvider() error {
	switch c.Auth.Provider {
	case "", AuthProviderJWT:
		return nil
	case AuthProviderOIDC:
		return errors.New("auth.provider: oidc is declared but not yet implemented; set provider: jwt")
	default:
		return fmt.Errorf("invalid auth.provider %q: must be %q (or empty = %q)",
			c.Auth.Provider, AuthProviderJWT, AuthProviderJWT)
	}
}

// validateRole rejects an unknown server.role (ADR 0049). Empty is valid (defaults
// to "all"). A typo like "worker" is a loud boot failure, not a silent monolith.
func (c *ServerConfig) validateRole() error {
	switch c.Server.Role {
	case "", RoleAll, RoleAPI, RoleScheduler:
		return nil
	default:
		return fmt.Errorf("invalid server.role %q: must be one of %q, %q, %q (or empty = %q)",
			c.Server.Role, RoleAll, RoleAPI, RoleScheduler, RoleAll)
	}
}
