# Security Policy

## Reporting a Vulnerability

The Leoflow team takes security issues seriously. We appreciate your efforts to disclose vulnerabilities responsibly.

**Please do NOT open public GitHub issues for security vulnerabilities.**

### How to Report

**Use GitHub's private vulnerability reporting:**
[**Report a vulnerability**](https://github.com/neochaotic/leoflow/security/advisories/new)

The report is visible only to the maintainer until an advisory is published. No
account beyond GitHub is needed, and it threads the discussion, the fix and the
eventual advisory in one place.

There is deliberately no email address here. This file used to publish
`security@leoflow.io` alongside the word "placeholder" — the domain does not
exist and never received anything, so a reporter following the policy reached
nobody. A channel that does not work is worse than no channel: it looks like
diligence and absorbs the report. The same went for the Keybase link.

If GitHub is not an option for you, open a public issue saying only that you
have a security report and asking for a contact — no details — and you will get
a private channel.

### What to Include

Please provide as much of the following as possible:

- Type of vulnerability (e.g., privilege escalation, RCE, authentication bypass)
- Affected component (control plane, agent, executor, CLI)
- Affected version(s)
- Step-by-step reproduction
- Proof-of-concept code or commands
- Impact assessment (what an attacker could achieve)
- Suggested remediation, if any

### Our Commitments

| Action | Timeline |
|---|---|
| Acknowledge receipt of your report | Within 2 business days |
| Initial assessment and severity rating | Within 5 business days |
| Regular status updates | Every 7 days until resolution |
| Public disclosure coordination | After fix is released, typically 30-90 days |

### Severity Ratings

We use CVSS 3.1 to score vulnerabilities:

| Severity | CVSS Score | Response |
|---|---|---|
| Critical | 9.0-10.0 | Emergency patch, within 7 days |
| High | 7.0-8.9 | Patch in next minor release, within 14 days |
| Medium | 4.0-6.9 | Patch in next planned release |
| Low | 0.1-3.9 | Patch when convenient, documented in release notes |

### Supported Versions

We provide security fixes for:

- The current stable release (latest minor version of the latest major)
- The previous minor version, for 90 days after the current release

Older versions receive critical-severity fixes only, on a best-effort basis. Upgrading to a supported version is the recommended remediation.

## Recognition

We maintain a [Security Hall of Fame](docs/security-hall-of-fame.md) acknowledging researchers who have responsibly disclosed vulnerabilities. With your permission, we credit you publicly when we publish the fix.

## Out of Scope

The following are NOT considered security vulnerabilities:

- Findings from automated scanners without proof of exploitability
- Reports requiring physical access to the host machine
- Reports requiring the attacker to already have administrative privileges
- Vulnerabilities in third-party dependencies that we cannot fix (we will route these upstream)
- Issues in development/example DAGs included for documentation purposes

## Public Disclosure

We follow coordinated disclosure. We will publish a GitHub Security Advisory and a CVE (when applicable) only after a fix is available, unless evidence of active exploitation requires earlier disclosure to protect users.
