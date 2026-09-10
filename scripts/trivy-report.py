#!/usr/bin/env python3
"""Turn Trivy JSON image reports into a review-ready Markdown summary.

Used by .github/workflows/image-scan.yaml to build the job summary and the body
of the container-CVE tracking issue.

Two numbers matter and they are not the same number, which is the whole reason
this exists rather than a `trivy --format table`:

  * **Reported** — every finding with a fix available, at any severity. This is
    what the scan publishes to the Security tab.
  * **Would block** — the subset at or above the gate severity floor. This is
    what a blocking gate WOULD fail on, shown for images that are deliberately
    not gated, so "we chose not to gate this" stays a visible, revisitable
    decision instead of a silent one.

A finding with no fix available never appears in either. Trivy is invoked with
--ignore-unfixed upstream of this script, so the input JSON already excludes
them; the counts here are of what someone could actually act on today.

The fingerprint is the sorted set of (image, vulnerability, package) triples that
would block. The workflow compares it against the one recorded in the open issue
so a daily re-run stays silent when nothing changed, and speaks up when the set
moves. Without it a scheduled scan either spams a comment a day or goes unread.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import sys

SEVERITY_ORDER = ["CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"]


def load(path: pathlib.Path) -> list[dict]:
    """Flatten a Trivy JSON report into a list of finding dicts."""
    doc = json.loads(path.read_text())
    out = []
    for result in doc.get("Results") or []:
        for v in result.get("Vulnerabilities") or []:
            out.append(
                {
                    "id": v.get("VulnerabilityID", "?"),
                    "pkg": v.get("PkgName", "?"),
                    "severity": v.get("Severity", "UNKNOWN"),
                    "installed": v.get("InstalledVersion", ""),
                    "fixed": v.get("FixedVersion", ""),
                    "type": result.get("Type", "?"),
                }
            )
    return out


def render(images: dict[str, list[dict]], floor: list[str]) -> tuple[str, str, int]:
    """Return (markdown, fingerprint, blocking count) for an image -> findings map."""
    lines: list[str] = []
    blocking_all: list[tuple[str, str, str]] = []

    lines.append("| Image | Reported (fixable) | Would block |")
    lines.append("|---|---:|---:|")
    for image in sorted(images):
        findings = images[image]
        blocking = [f for f in findings if f["severity"] in floor]
        blocking_all += [(image, f["id"], f["pkg"]) for f in blocking]
        lines.append(f"| `{image}` | {len(findings)} | {len(blocking)} |")
    lines.append("")

    for image in sorted(images):
        blocking = [f for f in images[image] if f["severity"] in floor]
        if not blocking:
            continue
        lines.append(f"### `{image}`")
        lines.append("")
        lines.append("| Severity | ID | Package | Installed | Fixed in |")
        lines.append("|---|---|---|---|---|")
        blocking.sort(key=lambda f: (SEVERITY_ORDER.index(f["severity"])
                                     if f["severity"] in SEVERITY_ORDER else 99, f["id"]))
        for f in blocking:
            lines.append(
                f"| {f['severity']} | {f['id']} | `{f['pkg']}` | "
                f"`{f['installed']}` | `{f['fixed']}` |"
            )
        lines.append("")

    fp = hashlib.sha256(
        "\n".join(f"{i}|{v}|{p}" for i, v, p in sorted(set(blocking_all))).encode()
    ).hexdigest()[:16]
    return "\n".join(lines), fp, len(blocking_all)


def self_test() -> int:
    """Cases that must hold, each isolating one behaviour."""
    import tempfile

    def report(vulns):
        return {"Results": [{"Type": "debian", "Vulnerabilities": vulns}]}

    def v(vid, sev, pkg="p", fixed="1.1"):
        return {"VulnerabilityID": vid, "Severity": sev, "PkgName": pkg,
                "InstalledVersion": "1.0", "FixedVersion": fixed}

    failures = []

    def check(cond, msg):
        if not cond:
            failures.append(msg)

    with tempfile.TemporaryDirectory() as td:
        p = pathlib.Path(td) / "img.json"

        # Severity floor selects the blocking subset, not the whole report.
        p.write_text(json.dumps(report([v("CVE-1", "CRITICAL"), v("CVE-2", "MEDIUM")])))
        md, fp1, n1 = render({"img": load(p)}, ["CRITICAL", "HIGH"])
        check("| 2 | 1 |" in md, f"floor must report 2 and block 1, got:\n{md}")
        check(n1 == 1, f"blocking count must be 1, got {n1}")
        check("CVE-1" in md, "blocking finding must be listed")
        check("CVE-2" not in md.split("###")[-1], "non-blocking finding must not be in the detail table")

        # An empty report is clean, and its fingerprint is stable.
        p.write_text(json.dumps({"Results": []}))
        md_empty, fp_empty, n_empty = render({"img": load(p)}, ["CRITICAL", "HIGH"])
        check("| 0 | 0 |" in md_empty, "an empty report must render zeroes")
        check(n_empty == 0, f"empty blocking count must be 0, got {n_empty}")
        _, fp_empty2, _ = render({"img": []}, ["CRITICAL", "HIGH"])
        check(fp_empty == fp_empty2, "empty fingerprint must be stable")

        # Fingerprint changes when the blocking set changes...
        p.write_text(json.dumps(report([v("CVE-1", "CRITICAL"), v("CVE-3", "HIGH")])))
        _, fp2, _ = render({"img": load(p)}, ["CRITICAL", "HIGH"])
        check(fp1 != fp2, "fingerprint must change when the blocking set changes")

        # ...and NOT when only a non-blocking finding changes. This is the
        # property that keeps a daily scan from commenting every morning.
        p.write_text(json.dumps(report([v("CVE-1", "CRITICAL"), v("CVE-9", "LOW")])))
        _, fp3, _ = render({"img": load(p)}, ["CRITICAL", "HIGH"])
        p.write_text(json.dumps(report([v("CVE-1", "CRITICAL"), v("CVE-8", "LOW")])))
        _, fp4, _ = render({"img": load(p)}, ["CRITICAL", "HIGH"])
        check(fp3 == fp4, "fingerprint must ignore findings below the floor")

        # Ordering of Trivy's output must not move the fingerprint.
        p.write_text(json.dumps(report([v("CVE-3", "HIGH"), v("CVE-1", "CRITICAL")])))
        _, fp5, _ = render({"img": load(p)}, ["CRITICAL", "HIGH"])
        check(fp2 == fp5, "fingerprint must be order-independent")

        # Multiple results sections in one report are all read. A Trivy image
        # report has one section per package type; reading only the first would
        # silently drop every language-package finding.
        p.write_text(json.dumps({"Results": [
            {"Type": "debian", "Vulnerabilities": [v("CVE-1", "CRITICAL")]},
            {"Type": "python-pkg", "Vulnerabilities": [v("CVE-4", "HIGH")]},
        ]}))
        md_multi, _, n_multi = render({"img": load(p)}, ["CRITICAL", "HIGH"])
        check("| 2 | 2 |" in md_multi, f"both result sections must be read, got:\n{md_multi}")
        check(n_multi == 2, f"both sections must count toward blocking, got {n_multi}")

        # A section with a null Vulnerabilities list is normal for a clean type.
        p.write_text(json.dumps({"Results": [{"Type": "debian", "Vulnerabilities": None}]}))
        check(load(p) == [], "a null Vulnerabilities list must read as no findings")

    if failures:
        for f in failures:
            print(f"self-test FAIL: {f}", file=sys.stderr)
        return 1
    print("trivy-report self-test: ok")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("reports", nargs="*", type=pathlib.Path,
                    help="Trivy JSON reports; the file stem names the image.")
    ap.add_argument("--gate-severity", default="CRITICAL,HIGH",
                    help="Severity floor a blocking gate would use.")
    ap.add_argument("--fingerprint-out", type=pathlib.Path,
                    help="Write the blocking-set fingerprint here.")
    ap.add_argument("--blocking-count-out", type=pathlib.Path,
                    help="Write the number of would-block findings here.")
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()

    if args.self_test:
        return self_test()
    if not args.reports:
        ap.error("no reports given")

    floor = [s.strip().upper() for s in args.gate_severity.split(",") if s.strip()]
    images = {}
    for path in args.reports:
        if not path.is_file():
            print(f"missing report: {path} — refusing to report clean", file=sys.stderr)
            return 1
        images[path.stem] = load(path)

    md, fp, blocking = render(images, floor)
    if args.fingerprint_out:
        args.fingerprint_out.write_text(fp)
    if args.blocking_count_out:
        args.blocking_count_out.write_text(str(blocking))
    print(md)
    return 0


if __name__ == "__main__":
    sys.exit(main())
