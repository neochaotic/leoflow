package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
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
	// under Dir; "s3" ships each attempt to an S3-compatible bucket (AWS S3,
	// MinIO, Ceph RGW); "gcs" ships to Google Cloud Storage via its native SDK.
	// Object storage is opt-in — Lite and every deployment that does not set this
	// keep the exact on-disk path unchanged.
	Backend string `mapstructure:"backend"`
	// Sink configures the object-store backend; read only when Backend is "s3" or
	// "gcs".
	Sink ObjectLogSection `mapstructure:"sink"`
}

// ObjectLogSection configures the object-store log backend for both the "s3" and
// "gcs" providers. Auth is keyless-first (ADR 0035): leave the credential fields
// empty to use the ambient chain — IRSA / instance profile for S3, GKE Workload
// Identity (ADC) for GCS. Static keys and credential files are a discouraged
// escape hatch for dev and clusters without an identity broker.
//
// Bucket and Prefix apply to both providers. Region, Endpoint, ForcePathStyle,
// AccessKeyID and SecretAccessKey are S3-only. CredentialsFile is GCS-only. A
// field set for the other provider is simply ignored.
type ObjectLogSection struct {
	// Bucket is the target bucket. Required when Backend is "s3" or "gcs".
	Bucket string `mapstructure:"bucket"`
	// Prefix is an optional key prefix under which attempt objects are laid out.
	Prefix string `mapstructure:"prefix"`
	// Region is the S3 store region (e.g. "us-east-1"). Required by AWS S3;
	// ignored by some S3-compatible stores. S3-only.
	Region string `mapstructure:"region"`
	// Endpoint overrides the S3 endpoint for S3-compatible stores (MinIO, Ceph
	// RGW). Empty uses the AWS default endpoint. S3-only — it is NOT the way to
	// reach GCS, which has its own keyless "gcs" backend.
	Endpoint string `mapstructure:"endpoint"`
	// ForcePathStyle uses path-style addressing (bucket in the path, not the
	// host). Required by MinIO and some S3-compatible stores. S3-only.
	ForcePathStyle bool `mapstructure:"force_path_style"`
	// AccessKeyID is a static S3 access key. Empty (recommended) uses the keyless
	// credential chain (ADR 0035). S3-only.
	AccessKeyID string `mapstructure:"access_key_id"`
	// SecretAccessKey pairs with AccessKeyID. Discouraged; prefer keyless. S3-only.
	SecretAccessKey string `mapstructure:"secret_access_key"`
	// CredentialsFile is a path to a GCS service-account JSON key. Empty
	// (recommended) uses Application Default Credentials — GKE Workload Identity
	// keyless. GCS-only.
	CredentialsFile string `mapstructure:"credentials_file"`
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
	// OIDC configures the OIDC/SSO login flow. It is read only when Provider is
	// "oidc" (Pro-gated); the JWT authenticator remains the request-path verifier
	// in both modes.
	OIDC OIDCSection `mapstructure:"oidc"`
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
	// SecretScoping is the operator scope-by-declaration policy (ADR 0055 D9):
	// "permissive" | "enforce" | "off". permissive (the default) delivers the
	// whole tenant vault when a DAG declares nothing and warns — but still
	// delivers the whole vault — when a DAG declares a narrower set; enforce
	// delivers only the declared subset (empty declaration → nothing); off
	// disables scoping. It is operator-scoped, NEVER author-settable. Empty = the
	// permissive default.
	SecretScoping string `mapstructure:"secret_scoping"`
	// SecretLivenessMode gates secret delivery on task-instance liveness (ADR 0055
	// E2): "observe" | "enforce". observe (the default) logs + audits a
	// would-have-denied when the caller's TI is not live but still delivers;
	// enforce denies with PermissionDenied. Empty = the observe default.
	SecretLivenessMode string `mapstructure:"secret_liveness_mode"`
}

// JWTSection configures JWT issuance and validation.
type JWTSection struct {
	Secret          string `mapstructure:"secret"`
	TokenTTLSeconds int    `mapstructure:"token_ttl_seconds"`
}

// OIDCSection configures the OIDC/SSO login flow (Authorization Code + PKCE).
// It is read only when auth.provider is "oidc", which is Pro-gated and fails
// boot closed unless Issuer, ClientID, and RedirectURL are all set.
//
// Verification is keyless: the ID token is validated against the issuer's
// public JWKS discovered from Issuer, so no secret is stored for the verify
// path. ClientSecret is used solely for the authorization-code exchange and is
// injected via LEOFLOW_AUTH_OIDC_CLIENT_SECRET (env, never persisted, never
// logged) — the same posture as the JWT secret.
type OIDCSection struct {
	// Issuer is the org's single-tenant issuer URL (https). It is pinned: any ID
	// token whose iss claim differs is rejected (fail-closed tenant pin).
	Issuer string `mapstructure:"issuer"`
	// ClientID is the registered application (client) id; it is the expected
	// audience of every ID token.
	ClientID string `mapstructure:"client_id"`
	// ClientSecret is used only for the code exchange. Set via
	// LEOFLOW_AUTH_OIDC_CLIENT_SECRET; never persist it in a config file.
	ClientSecret string `mapstructure:"client_secret"`
	// RedirectURL is this server's callback URL registered with the IdP
	// (…/api/v2/auth/oidc/callback).
	RedirectURL string `mapstructure:"redirect_url"`
	// Scopes are the OAuth scopes requested; defaults to openid, email, profile.
	// Add the IdP's groups scope here when group→role mapping is used.
	Scopes []string `mapstructure:"scopes"`
	// GroupsClaim is the ID-token claim carrying the user's IdP groups (default
	// "groups"). Its values drive RoleMappings.
	GroupsClaim string `mapstructure:"groups_claim"`
	// RoleMappings maps an IdP group value to an existing Leoflow role name.
	// Default-DENY: a group with no mapping grants no role. Configure via the
	// config file / Helm values (maps do not bind from a single env var).
	RoleMappings map[string]string `mapstructure:"role_mappings"`
	// DefaultRole softens the default-deny WITHOUT weakening the secure default:
	// when an authenticated user resolves to zero mapped roles and DefaultRole is
	// set, they are granted this single role (operators are advised to use a
	// read-only role such as "viewer"). Empty (the default) keeps strict
	// default-deny — an unmapped user gets no role. It must name an existing DB
	// role for the resolved tenant; an unknown role fails the login closed.
	DefaultRole string `mapstructure:"default_role"`
	// TenantClaim selects which IdP claim identifies the tenant: "tid" (Entra) or
	// "hd" (Google Workspace).
	TenantClaim string `mapstructure:"tenant_claim"`
	// TenantClaims maps a TenantClaim value to a Leoflow tenant name. A value not
	// present here is rejected (403) — the login never falls back to "default".
	TenantClaims map[string]string `mapstructure:"tenant_claims"`
	// AllowedEmailDomains is an install-time, login-level allowlist layered on TOP
	// of the tid/hd tenant pin — it is NOT the pin itself (that stays issuer +
	// tid/hd + email_verified per D6). The check runs only AFTER the pin and
	// email_verified==true have passed, so the email domain is trustworthy at that
	// point. Empty (the default) imposes no domain restriction — the tid/hd pin is
	// the sole boundary. Non-empty admits a login (pre-provisioned OR JIT) only
	// when the verified email's domain is in the list; every other login is
	// rejected 403. It gates EVERY OIDC login, not just auto-provisioning.
	AllowedEmailDomains []string `mapstructure:"allowed_email_domains"`
	// BreakGlassEmails is the allowlist of local password logins permitted while
	// provider is "oidc"; every other password login is rejected (SSO-only).
	BreakGlassEmails []string `mapstructure:"break_glass_emails"`
	// JITProvisioning creates a user row on first OIDC login when no matching one
	// exists. OFF by default (a pre-provisioned user is required); when ON, the new
	// row is granted the roles from RoleMappings.
	JITProvisioning bool `mapstructure:"jit_provisioning"`
	// ClockSkewSeconds is the tolerance applied to the ID token's exp/iat/nbf
	// checks to absorb small clock differences between the IdP and this server.
	// Defaults to 60.
	ClockSkewSeconds int `mapstructure:"clock_skew_seconds"`
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
	// Secret scope-by-declaration and token-liveness policies (ADR 0055). Both
	// ship SAFE by default: permissive delivers the whole tenant vault (today's
	// behavior) and observe logs a would-have-denied without denying. The go-live
	// flips (enforce) are separate operator decisions after an observe period.
	"auth.secret_scoping":       "permissive",
	"auth.secret_liveness_mode": "observe",
	// OIDC leaves. Every leaf is registered so viper's AutomaticEnv binds the
	// scalar LEOFLOW_AUTH_OIDC_* env vars (notably the client secret). The map and
	// slice leaves are config-file / Helm-values driven — viper does not split a
	// single env var into a map or list — but they must appear here so Unmarshal
	// resolves them from the file.
	"auth.oidc.issuer":                "",
	"auth.oidc.client_id":             "",
	"auth.oidc.client_secret":         "",
	"auth.oidc.redirect_url":          "",
	"auth.oidc.scopes":                []string{"openid", "email", "profile"},
	"auth.oidc.groups_claim":          "groups",
	"auth.oidc.role_mappings":         map[string]string{},
	"auth.oidc.default_role":          "",
	"auth.oidc.tenant_claim":          "",
	"auth.oidc.tenant_claims":         map[string]string{},
	"auth.oidc.allowed_email_domains": []string{},
	"auth.oidc.break_glass_emails":    []string{},
	"auth.oidc.jit_provisioning":      false,
	"auth.oidc.clock_skew_seconds":    60,
	"scheduler.loop_interval_ms":      1000,
	"scheduler.enabled":               true,
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
	"logs.dir":                    "/var/log/leoflow",
	"logs.backend":                "disk",
	"logs.sink.bucket":            "",
	"logs.sink.prefix":            "",
	"logs.sink.region":            "",
	"logs.sink.endpoint":          "",
	"logs.sink.force_path_style":  false,
	"logs.sink.access_key_id":     "",
	"logs.sink.secret_access_key": "",
	"logs.sink.credentials_file":  "",
	"observability.otel.enabled":  true,
	"observability.otel.endpoint": "localhost:4317",
	"observability.log_level":     "info",
	"observability.log_format":    "json",
	"ui.instance_name":            "Leoflow",
	"ui.edition":                  "",
	"ui.workspace":                "",
	"ui.monaco_dir":               "",
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

// Auth providers (auth.provider allowlist). "jwt" is the default credential
// authenticator; "oidc" adds the SSO login flow on top of it (the JWT
// authenticator stays the request-path verifier in both modes).
const (
	// AuthProviderJWT is the default: username/password issues an HS256 token.
	AuthProviderJWT = "jwt"
	// AuthProviderOIDC enables the OIDC/SSO login flow. It is Pro-gated and
	// requires the auth.oidc.* configuration; boot fails closed otherwise.
	AuthProviderOIDC = "oidc"
)

// Secret policy allowlists (ADR 0055). auth.secret_scoping and
// auth.secret_liveness_mode are validated against these; an unknown value fails
// boot closed. Empty is valid — serverDefaults sets the safe default for each.
const (
	// SecretScopingPermissive delivers the whole tenant vault (today's behavior),
	// scoping only where a DAG declared; the default.
	SecretScopingPermissive = "permissive"
	// SecretScopingEnforce delivers only the declared subset.
	SecretScopingEnforce = "enforce"
	// SecretScopingOff disables scope-by-declaration entirely.
	SecretScopingOff = "off"
	// SecretLivenessObserve logs a would-have-denied without denying; the default.
	SecretLivenessObserve = "observe"
	// SecretLivenessEnforce denies secret delivery when the caller's TI is not live.
	SecretLivenessEnforce = "enforce"
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
	if err := c.validateSecretPolicies(); err != nil {
		return err
	}
	// Both providers mint the app's own HS256 _token (oidc mints it after the IdP
	// verify), so the JWT secret is required for either.
	if (c.Auth.Provider == AuthProviderJWT || c.Auth.Provider == AuthProviderOIDC) && c.Auth.JWT.Secret == "" {
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

// validateLogs rejects an unknown logs.backend and requires a bucket when an
// object backend is selected, so a misconfigured object sink fails closed at
// boot instead of losing every task log to a nonexistent bucket. Empty and
// "disk" are always valid — the on-disk default is unaffected.
func (c *ServerConfig) validateLogs() error {
	switch c.Logs.Backend {
	case "", "disk":
		return nil
	case "s3", "gcs":
		if c.Logs.Sink.Bucket == "" {
			return fmt.Errorf(`logs.sink.bucket is required when logs.backend is %q (set LEOFLOW_LOGS_SINK_BUCKET)`, c.Logs.Backend)
		}
		return nil
	default:
		return fmt.Errorf(`unknown logs.backend %q (want "disk", "s3" or "gcs")`, c.Logs.Backend)
	}
}

// validateSecretPolicies rejects an unknown auth.secret_scoping or
// auth.secret_liveness_mode, failing closed at boot rather than letting main.go
// wire an unrecognized policy (ADR 0055). Empty is valid: serverDefaults sets
// the safe default (permissive / observe) for each.
func (c *ServerConfig) validateSecretPolicies() error {
	switch c.Auth.SecretScoping {
	case "", SecretScopingPermissive, SecretScopingEnforce, SecretScopingOff:
	default:
		return fmt.Errorf("invalid auth.secret_scoping %q: must be %q, %q or %q",
			c.Auth.SecretScoping, SecretScopingPermissive, SecretScopingEnforce, SecretScopingOff)
	}
	switch c.Auth.SecretLivenessMode {
	case "", SecretLivenessObserve, SecretLivenessEnforce:
	default:
		return fmt.Errorf("invalid auth.secret_liveness_mode %q: must be %q or %q",
			c.Auth.SecretLivenessMode, SecretLivenessObserve, SecretLivenessEnforce)
	}
	return nil
}

// validateProvider rejects an unknown auth.provider, failing closed at boot
// instead of letting main.go build an authenticator regardless of what was
// configured. Empty is valid: serverDefaults sets auth.provider to "jwt", so an
// unset provider in an existing config keeps defaulting to JWT and is
// unaffected. "oidc" is valid only when its Pro-gated prerequisites are met
// (validateOIDC); anything else is a loud boot failure.
func (c *ServerConfig) validateProvider() error {
	switch c.Auth.Provider {
	case "", AuthProviderJWT:
		return nil
	case AuthProviderOIDC:
		return c.validateOIDC()
	default:
		return fmt.Errorf("invalid auth.provider %q: must be %q or %q (or empty = %q)",
			c.Auth.Provider, AuthProviderJWT, AuthProviderOIDC, AuthProviderJWT)
	}
}

// validateOIDC enforces the fail-closed prerequisites for auth.provider: oidc
// (D7): the Pro edition, and the three fields the login flow cannot run without
// (issuer, client_id, redirect_url). The issuer must be https so discovery and
// JWKS are fetched over TLS (keyless verify, ADR 0035). A misconfigured OIDC
// deployment fails boot with an actionable message rather than starting a login
// flow that cannot complete.
func (c *ServerConfig) validateOIDC() error {
	if c.UI.Edition != "pro" {
		return errors.New("auth.provider: oidc requires the Pro edition (set ui.edition: pro)")
	}
	var missing []string
	if c.Auth.OIDC.Issuer == "" {
		missing = append(missing, "auth.oidc.issuer")
	}
	if c.Auth.OIDC.ClientID == "" {
		missing = append(missing, "auth.oidc.client_id")
	}
	if c.Auth.OIDC.RedirectURL == "" {
		missing = append(missing, "auth.oidc.redirect_url")
	}
	if len(missing) > 0 {
		return fmt.Errorf("auth.provider: oidc requires %s to be set", strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(c.Auth.OIDC.Issuer, "https://") {
		return fmt.Errorf("auth.oidc.issuer must be an https:// URL (got %q)", c.Auth.OIDC.Issuer)
	}
	return validateRedirectURL(c.Auth.OIDC.RedirectURL)
}

// validateRedirectURL requires the OIDC callback URL to use https so the
// authorization code is never returned over plaintext, with an http exception
// for loopback hosts (localhost, 127.0.0.1, [::1]) to keep local dev workable.
func validateRedirectURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("auth.oidc.redirect_url must be a valid URL (got %q): %w", raw, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("auth.oidc.redirect_url must be an https:// URL (got %q); http:// is only allowed for loopback hosts (localhost, 127.0.0.1, [::1])", raw)
	default:
		return fmt.Errorf("auth.oidc.redirect_url must be an https:// URL (got %q)", raw)
	}
}

// isLoopbackHost reports whether host is a loopback name or address. url.Hostname
// strips the brackets from an IPv6 literal, so [::1] arrives as "::1".
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
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
