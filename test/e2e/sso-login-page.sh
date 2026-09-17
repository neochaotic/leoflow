#!/usr/bin/env bash
#
# Stands up what #1160's browser assertion needs and runs it twice: once against a
# control plane with SSO configured, once without.
#
# The awkward part is that leoflow refuses a non-https issuer at boot, and OIDC
# discovery happens at boot, so a fake IdP has to serve
# /.well-known/openid-configuration over TLS before the server will start at all.
# This generates a throwaway CA for it and points the server at that CA through
# SSL_CERT_FILE, which the Linux build of Go's crypto/x509 reads. Nothing here
# touches the system trust store.
#
# Requirements: openssl, python3, node with playwright-core, a built
# bin/leoflow-server, and a reachable dev Postgres.
#
# Usage: test/e2e/sso-login-page.sh
set -euo pipefail

# PLATFORM NOTE. This needs Go to trust a throwaway CA through SSL_CERT_FILE,
# because leoflow refuses a non-https issuer and discovery happens at boot. That
# works on Linux, which is where CI runs it. It does NOT work on macOS: the darwin
# build of crypto/x509 defers to the platform verifier and has no SSL_CERT_FILE
# path at all, so the variable is read by nothing.
#
# Measure that with a handshake, not with the cert pool. x509.SystemCertPool on
# darwin returns a pool whose Subjects() is empty whether or not SSL_CERT_FILE is
# set, so "empty pool" is the same observation in both cases and distinguishes
# nothing. An https GET against a server holding a cert signed by the named CA
# does distinguish, and on macOS it fails with "certificate signed by unknown
# authority" with the variable set (measured, go1.26, darwin/arm64).
#
# On a Mac, run it inside a Linux container or let CI run it.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
IDP_PORT="${IDP_PORT:-18443}"
SSO_PORT="${SSO_PORT:-18080}"
PLAIN_PORT="${PLAIN_PORT:-18081}"
DB="${LEOFLOW_E2E_DATABASE_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable}"
PIDS=()

cleanup() {
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

log "Generating a throwaway CA and a localhost cert for the fake IdP"
openssl req -x509 -newkey rsa:2048 -keyout "$WORK/idp.key" -out "$WORK/idp.crt" \
  -days 1 -nodes -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
# The agent gRPC listener needs its own pair; the Pro edition refuses to boot
# without one, and it is unrelated to the IdP.
openssl req -x509 -newkey rsa:2048 -keyout "$WORK/grpc.key" -out "$WORK/grpc.crt" \
  -days 1 -nodes -subj "/CN=localhost" >/dev/null 2>&1

ISSUER="https://localhost:${IDP_PORT}"
cat > "$WORK/discovery.json" <<JSON
{
  "issuer": "${ISSUER}",
  "authorization_endpoint": "${ISSUER}/authorize",
  "token_endpoint": "${ISSUER}/token",
  "jwks_uri": "${ISSUER}/jwks",
  "response_types_supported": ["code"],
  "subject_types_supported": ["public"],
  "id_token_signing_alg_values_supported": ["RS256"]
}
JSON

log "Serving the fake IdP discovery document on ${ISSUER}"
cat > "$WORK/idp.py" <<'PYEOF'
import http.server, ssl, sys

work, port = sys.argv[1], int(sys.argv[2])
doc = open(work + "/discovery.json", "rb").read()


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        # Only discovery is needed. NewProvider fetches it at boot; JWKS is not
        # touched until a token is verified, which this test never reaches.
        # /authorize only has to be somewhere the browser can land.
        if self.path.startswith("/.well-known/openid-configuration"):
            body, ctype = doc, "application/json"
        elif self.path.startswith("/authorize"):
            body, ctype = b"<html><body>fake idp</body></html>", "text/html"
        else:
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(work + "/idp.crt", work + "/idp.key")
srv = http.server.HTTPServer(("127.0.0.1", port), Handler)
srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
srv.serve_forever()
PYEOF
python3 "$WORK/idp.py" "$WORK" "$IDP_PORT" >"$WORK/idp.log" 2>&1 &
PIDS+=($!)

for _ in $(seq 1 20); do
  curl -sf --cacert "$WORK/idp.crt" "${ISSUER}/.well-known/openid-configuration" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf --cacert "$WORK/idp.crt" "${ISSUER}/.well-known/openid-configuration" >/dev/null \
  || { echo "FAIL: the fake IdP never served discovery; see $WORK/idp.log" >&2; exit 1; }

# tenant_claims is required by validateOIDC and is a MAP, so it has no
# environment-variable route at all: viper binds env only for the scalar leaves,
# and this one is excluded because a Google `hd` key is a dotted domain (#826).
# It comes from the file named by LEOFLOW_CONFIG, which is the same shape the
# chart mounts. Writing it here is not test scaffolding trivia; it is the whole
# reason the chart needed a ConfigMap.
cat > "$WORK/config.yaml" <<'CFGEOF'
auth:
  oidc:
    tenant_claims:
      "example.com": "default"
CFGEOF

log "Booting the control plane WITH sso on :${SSO_PORT}"
env LEOFLOW_UI_EDITION=pro \
    LEOFLOW_AUTH_PROVIDER=oidc \
    LEOFLOW_AUTH_JWT_SECRET=sso-e2e \
    LEOFLOW_CONFIG="$WORK/config.yaml" \
    LEOFLOW_AUTH_OIDC_ISSUER="$ISSUER" \
    LEOFLOW_AUTH_OIDC_CLIENT_ID=leoflow \
    LEOFLOW_AUTH_OIDC_REDIRECT_URL="https://localhost:${SSO_PORT}/api/v2/auth/oidc/callback" \
    LEOFLOW_AUTH_OIDC_TENANT_CLAIM=hd \
    LEOFLOW_AUTH_OIDC_BREAK_GLASS_EMAILS=breakglass@example.com \
    LEOFLOW_SERVER_GRPC_TLS_CERT="$WORK/grpc.crt" \
    LEOFLOW_SERVER_GRPC_TLS_KEY="$WORK/grpc.key" \
    LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${SSO_PORT}" \
    LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:19090" \
    LEOFLOW_DATABASE_URL="$DB" \
    SSL_CERT_FILE="$WORK/idp.crt" \
    "$ROOT/bin/leoflow-server" >"$WORK/sso-server.log" 2>&1 &
PIDS+=($!)

log "Booting a control plane WITHOUT sso on :${PLAIN_PORT}"
env LEOFLOW_AUTH_JWT_SECRET=plain-e2e \
    LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${PLAIN_PORT}" \
    LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:19091" \
    LEOFLOW_SERVER_GRPC_ADDR="0.0.0.0:19092" \
    LEOFLOW_DATABASE_URL="$DB" \
    "$ROOT/bin/leoflow-server" >"$WORK/plain-server.log" 2>&1 &
PIDS+=($!)

wait_ready() { # <port> <name>
  for _ in $(seq 1 40); do
    if curl -sf "http://localhost:$1/api/v2/auth/login" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  echo "FAIL: $2 never served its login page; see $WORK" >&2
  tail -20 "$WORK/$2.log" >&2 || true
  return 1
}
wait_ready "$SSO_PORT" sso-server
wait_ready "$PLAIN_PORT" plain-server

log "Asserting the SSO deployment offers the flow"
LEOFLOW_URL="http://localhost:${SSO_PORT}" LEOFLOW_IDP_ORIGIN="$ISSUER" \
  node "$ROOT/test/e2e/sso-login-page.js"

log "Asserting the JWT-only deployment does not advertise a route it never registered"
LEOFLOW_URL="http://localhost:${PLAIN_PORT}" LEOFLOW_EXPECT_SSO=0 node "$ROOT/test/e2e/sso-login-page.js"

log "sso login page e2e passed"
