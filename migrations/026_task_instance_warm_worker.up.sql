-- Durable warm-attempt binding (ADR 0058 N1d-a1). When a warm worker acks an
-- assignment as started, the control plane records WHICH warm pod is serving the
-- attempt in warm_worker_id — the pod's own name, delivered to the worker via the
-- downward API and sent up in WorkerRegister.pod_name. The binding is durable so a
-- later failover reaper (N1d-a2) and the stale-queued reaper can tell, even after a
-- leader failover, which running attempts a now-dead warm pod held, by matching
-- warm_worker_id against the live warm-pod set ListWarmPods returns.
--
-- Nullable and written ONLY on a warm ack: a dedicated-pod attempt (the default,
-- and every attempt while execution.warm_pools_enabled is off) never sets it, so
-- the column stays NULL and today's behavior is byte-for-byte unchanged. It is
-- distinct from pod_name (the dedicated task pod that ran an attempt): this names
-- the long-lived warm WORKER pod, which serves many attempts across its life.
ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS warm_worker_id TEXT;
