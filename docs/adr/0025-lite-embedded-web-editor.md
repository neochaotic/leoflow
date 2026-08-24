# ADR 0025: Embedded Monaco Web Editor for Leoflow Lite

**Status:** Accepted
**Date:** 2026-05-25
**Deciders:** Project founder
**Scope:** Lite edition only (see [editions](../editions.md)). Production teams use
their own IDE + the GitOps flow.

## Context

Lite is a single-machine, local edition. The last Lite-exclusive feature before
the Production push is a **web IDE**: edit a DAG project in the workspace from the
browser, opened from the Leoflow UI, theme-matched, so the whole loop (edit →
hot-reload → run) stays in one place.

The obvious candidates are full VS-Code-in-the-browser servers — **code-server**,
**openvscode-server**, or a built **Theia** app. Two problems rule them out for
Lite:

1. **Weight.** They are Node apps (~150 MB+), shipped as a separate process via
   Docker or a large download. Lite was just made deliberately light (no Airflow,
   vendored deps, one-command install); bolting on a 150 MB IDE undoes that.
2. **They break over plain HTTP off-localhost.** VS Code Web relies on **Service
   Workers** (extension host, file routing), the async **Clipboard API**, and
   `Secure` cookies. Browsers gate all of these to a **secure context**. Crucially,
   `http://localhost` / `127.0.0.1` *is* a secure context, so code-server works
   over HTTP on localhost — but **a non-localhost HTTP origin is not**. Lite is
   meant for an **internal network / VPN** (ADR-aligned: see editions), where users
   reach it at `http://<LAN-IP>:8088`. There, Service Workers and the Clipboard API
   are blocked and `Secure` cookies are dropped → the IDE breaks. Making it work
   would require HTTPS/TLS (certificates, browser warnings), which contradicts
   Lite's "simple" requirement.

## Decision

**Lite serves a built-in, minimal web editor — the Monaco editor plus a file
tree — from the control plane itself, same-origin, over HTTP.** It is opened in a
new browser tab from an injected "IDE" button in the served UI shell.

- **Monaco only** (the editor component behind VS Code), not a VS Code server.
  Monaco is plain client-side JS: it needs **no Service Worker**, and basic
  editing + copy/paste use native key/`paste` events (and `execCommand`), which
  **work in a non-secure HTTP context**. So it passes the secure-context rule on
  both `localhost` and a LAN-IP/VPN over plain HTTP.
- **Same-origin, new tab.** Served by the Leoflow control plane at a path on the
  same origin (no CORS), opened in a new tab (not an iframe), so there are no
  cross-origin cookie/`SameSite`/iframe issues.
- **Workspace-sandboxed files API.** A small API (list / read / write / create /
  delete) rooted at the configured workspace, with strict path-traversal guards —
  it must never read or write outside the workspace.
- **Lite only.** Enabled when the edition is Lite; never in Production.
- **Theme-matched.** Opens with a light/dark theme (`vs` / `vs-dark`) matching the
  Leoflow UI.
- **Orthogonal to the executor.** It edits workspace source on the host, so it
  behaves identically under the `subprocess` and `k8s` (k3d) executors.

## Rationale

- **Passes the HTTP / secure-context rule** where code-server would not — works on
  localhost *and* over plain HTTP on a LAN/VPN, no TLS needed.
- **Light & simple** — no Node, no Docker, no 150 MB download; a few MB of editor
  assets served by the existing binary. Matches Lite's ethos and the stated
  "the IDE can be simple" requirement.
- **Same-origin** — no CORS, cookie, or iframe headaches.
- **Right-sized** — browse/open/edit/save/create/delete is what DAG authoring
  needs; the hot-reload loop does the rest.

## Consequences

- **Not a full IDE**: no extensions, integrated terminal, debugger, or global
  search — by design. Users who want those run their own editor against the
  workspace.
- **Security**: the files API is a local disk surface — it MUST be strictly
  confined to the workspace root (reject absolute paths and `..` traversal), and is
  Lite-only / loopback-or-trusted-network only, consistent with Lite's "run on an
  internal network/VPN" posture.
- **Assets / binary weight**: Monaco's bundle (~5 MB) is **not embedded in the
  binary**. It is fetched once by `leoflow setup` / `leoflow lite provision` into
  the managed home (`~/.leoflow/assets/monaco/<pinned-version>/`), SHA-256-verified
  (the same download-and-verify path used for the relocatable Python), and served
  same-origin from there by the control plane. This keeps the binary light, works
  offline after the one-time fetch, and stays pinned + checksummed per the
  supply-chain stance (ADR 0014). Only the small editor page (a few KB of
  HTML/JS) is embedded. If Monaco is not yet provisioned, the page shows a
  `leoflow setup` hint instead of a broken editor.
- **Production**: out of scope — enterprise teams use their own IDE and the GitOps
  deploy flow.

## Alternatives Rejected

- **code-server / openvscode-server**: heavy (Node, ~150 MB, Docker/download) and
  broken over non-localhost HTTP without TLS (Service Workers/Clipboard/Secure
  cookies). Viable only behind HTTPS, which Lite deliberately avoids.
- **Eclipse Theia (built app)**: same browser constraints, plus a heavy Node build
  to assemble the app — the most work for no benefit over Monaco here.
- **Requiring HTTPS for Lite**: certificates and browser-trust friction contradict
  the one-command, simple, internal-network design.
