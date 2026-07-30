# Pro TLS (agent gRPC) with cert-manager

The Pro control plane delivers secrets (Connections, Variables) to task pods over
gRPC. In Pro that channel **must be TLS** — the server refuses to boot without it
(#281), and the chart refuses to install when `agentTLS.enabled` but the cert
Secret or CA ConfigMap is missing (#280). The clean way to provision the cert is
[cert-manager](https://cert-manager.io).

An operator-ready values file is at
[`helm/leoflow/examples/values-pro-tls.yaml`](https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/examples/values-pro-tls.yaml)
— `-f` it after the steps below. (The full chart value reference is the
[Helm chart](helm-chart.md) page.)

## 1. Install cert-manager

```console
$ helm repo add jetstack https://charts.jetstack.io --force-update
$ helm install cert-manager jetstack/cert-manager \
    --namespace cert-manager --create-namespace \
    --version v1.16.2 --set crds.enabled=true
```

## 2. An Issuer

**Self-signed** (in-cluster / internal — the common case, since the agent channel
never leaves the cluster):

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: leoflow-selfsigned
  namespace: leoflow
spec:
  selfSigned: {}
```

For a CA that agents across namespaces trust, use a **self-signed CA** issuer (a
root Certificate + a `kind: Issuer` with `ca.secretName`); for externally-reachable
endpoints use an **ACME/Let's Encrypt** `ClusterIssuer` instead.

## 3. A Certificate

The Certificate's `secretName` must match `agentTLS.serverCertSecret`:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: leoflow-agent-tls
  namespace: leoflow
spec:
  secretName: leoflow-agent-tls          # == agentTLS.serverCertSecret
  duration: 2160h                        # 90d
  renewBefore: 360h                      # 15d
  issuerRef:
    name: leoflow-selfsigned
    kind: Issuer
  dnsNames:
    - leoflow.leoflow.svc                 # the control-plane Service DNS
    - leoflow.leoflow.svc.cluster.local
```

## 4. The CA trust bundle for task pods

Task pods verify the server cert against `agentTLS.caConfigMap`. Publish the
signing CA to a ConfigMap — cert-manager's
[trust-manager](https://cert-manager.io/docs/trust/trust-manager/) `Bundle` is the
standard way, or copy the issuer's `ca.crt` into a ConfigMap keyed `ca.crt`. Set
`agentTLS.caConfigMap` to that ConfigMap's name.

## 5. Install

```console
$ helm install leoflow leoflow/leoflow -n leoflow \
    -f values-pro-tls.yaml
```

## Troubleshooting

- **`the Pro edition requires TLS on the agent gRPC channel, but it is off`** — the
  server refused to boot because `LEOFLOW_SERVER_GRPC_TLS_CERT`/`_KEY` are unset:
  the cert Secret didn't mount, or the server was started outside this chart.
  Provide the cert as above (#281).
- **`agentTLS.enabled=false is not a supported configuration`** — helm refused the
  install. This chart only deploys the Pro edition, and that edition cannot boot
  without a cert, so turning TLS off buys a `CrashLoopBackOff`, not a plaintext
  deployment. Provision the cert as above; for a plaintext local loop use the Lite
  dev server (`leoflow dev lite`), not this chart (#459).
- **`agentTLS.caConfigMap is required when agentTLS.enabled`** — helm refused the
  install because the CA ConfigMap is missing. Without it task pods fail the cert
  chain (`x509: certificate signed by unknown authority`) and hang (#280). Do step 4.
- **Tasks stuck queued, agent logs `certificate signed by unknown authority`** —
  `caConfigMap` points at a ConfigMap that doesn't hold the CA that signed
  `serverCertSecret`. Re-check the trust bundle.
