#!/usr/bin/env bash
# Fold the pending changie fragments into CHANGELOG.md's `## [Unreleased]`.
#
# This is the other half of #1200. Contributors stop editing CHANGELOG.md and
# write one file per change under .changes/unreleased/ instead, so two PRs never
# touch the same bytes and the merge conflict stops existing by construction.
# Something has to put those files back together, and this is it: cut-release.sh
# calls it on the release branch, before the gates and the commit.
#
# It merges rather than appends. A release in flight may still carry entries a
# human typed into [Unreleased] by hand, and the fragments may render the same
# `### Fixed` heading those entries already live under. Appending would leave
# two `### Fixed` headings in one section, which is precisely the corruption
# that reached main on 2026-09-20 (five headings for three types, every
# conflict resolved by keeping both sides) and the reason this whole change
# exists. Kinds are unioned, in the order .changie.yaml declares them.
#
# Usage:
#   scripts/changelog-fold.sh <version>        # render with changie, then merge
#   scripts/changelog-fold.sh --render <file>  # merge an already-rendered block
#   scripts/changelog-fold.sh --self-test
#
# <version> is only a label for changie's renderer; nothing about it is written
# to the CHANGELOG, because [Unreleased] is where the entries land. Dating the
# section is cut-release.sh's job and happens after this, on GA only.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHANGELOG="$ROOT/CHANGELOG.md"
FRAGMENT_DIR="$ROOT/.changes/unreleased"
# The kind order, and the exact heading spelling, both come from .changie.yaml.
# Keep them in step: a label here that changie does not emit is inert, and one
# changie emits that is missing here still survives, but lands after the known
# kinds instead of in its declared position.
KINDS="Added,Changed,Deprecated,Removed,Fixed,Security"

# merge_unreleased <changelog> <rendered>: prints the whole CHANGELOG with the
# rendered block folded into [Unreleased]. A pure filter over two files, so the
# self-test can drive every case without changie, git, or a release.
#
# Anything inside [Unreleased] that is not a `### ` heading is carried over
# verbatim under the heading it was written below (or above all of them, for a
# prose note at the top of the section). Dropping a line because it did not look
# like a bullet would be silent data loss in the one file whose entire job is to
# not lose things.
merge_unreleased() { # <changelog> <rendered>
	awk -v kinds="$KINDS" -v rendered="$2" '
		function emit(   i, k, j, lines, n, last) {
			print ""
			for (i = 1; i <= leadn; i++) print lead[i]
			if (leadn > 0) print ""
			for (i = 1; i <= ordern; i++) {
				k = order[i]
				if (!(k in items)) continue
				# A blank line after the heading: the convention in 83 of the
				# 85 headings this file already has, and the two exceptions are
				# in released sections this script never rewrites.
				print "### " k
				print ""
				n = split(items[k], lines, "\n")
				last = 0
				for (j = 1; j <= n; j++) if (lines[j] != "") last = j
				for (j = 1; j <= last; j++) print lines[j]
				print ""
			}
		}
		function add(k, line,   key) {
			if (!(k in known)) { known[k] = 1; order[++ordern] = k }
			# Blank lines inside an entry are load-bearing: entries in this file
			# run to several paragraphs, and swallowing the blanks welds them
			# into one. They are appended raw, never deduplicated, and a run of
			# them at the start or end of a kind is dropped by emit().
			if (line == "") {
				if (k in items) items[k] = items[k] "\n"
				return
			}
			# Exact duplicates collapse. They arise when someone writes both the
			# fragment and the hand entry for one change, and two identical
			# bullets carry no information the first one does not.
			key = k "\x01" line
			if (key in dedup) return
			dedup[key] = 1
			items[k] = items[k] line "\n"
		}
		BEGIN {
			ordern = split(kinds, order, ",")
			for (i = 1; i <= ordern; i++) known[order[i]] = 1
			leadn = 0
		}
		# The passes are told apart by FILENAME, not by the usual NR == FNR.
		# The rendered file is legitimately empty whenever nothing is pending,
		# and with an empty first file NR == FNR stays true for every line of
		# the second one: the CHANGELOG gets read as the rendered block and the
		# script prints nothing at all.
		FILENAME == rendered {
			if ($0 ~ /^### /) { k = substr($0, 5); sub(/[ \t]+$/, "", k); next }
			# A blank line inside a rendered entry is a paragraph break and is
			# kept; entries in this file routinely run to several paragraphs.
			# Before any heading it is layout, and there is no kind to keep it
			# under anyway.
			if ($0 == "") { if (k != "") add(k, ""); next }
			if (k == "") next
			# changie renders `- {{.Body}}`, so only the FIRST line of a body
			# gets the bullet marker and every line after it comes back flush
			# left. In markdown a flush-left line after a blank one ends the
			# list item, which would drop the second paragraph of an entry out
			# of its own bullet. Indent the continuations to the two spaces the
			# rest of this CHANGELOG uses. A line the author already indented is
			# left alone, and one that starts its own bullet is a sibling entry.
			if ($0 !~ /^- / && $0 !~ /^[ \t]/) $0 = "  " $0
			add(k, $0)
			next
		}
		# From here on, the CHANGELOG itself.
		FNR == 1 { state = 0; k = "" }
		state == 0 {
			print
			if ($0 ~ /^## \[Unreleased\]/) state = 1
			next
		}
		state == 1 {
			if ($0 ~ /^## \[/) { emit(); print; state = 2; next }
			if ($0 ~ /^### /) { k = substr($0, 5); sub(/[ \t]+$/, "", k); next }
			if ($0 == "" && k == "") next
			if (k == "") lead[++leadn] = $0; else add(k, $0)
			next
		}
		{ print }
		END { if (state == 1) emit() }
	' "$2" "$1"
}

self_test() {
	local fail=0
	_eq() { # <got> <want> <name>
		if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"
		else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi
	}
	local tmp; tmp="$(mktemp -d)"; trap 'rm -rf "${tmp:-}"' RETURN

	# 1. The case this script exists for: the rendered block repeats a heading
	#    the section already has. One heading out, both bullets under it.
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '### Fixed' '' '- hand (#1)' '' '## [1.0.0] - 2026-01-01' '' '### Added' '' '- old' > "$tmp/cl.md"
	printf '%s\n' '### Fixed' '- fragment (#2)' > "$tmp/r.md"
	local got; got="$(merge_unreleased "$tmp/cl.md" "$tmp/r.md")"
	_eq "$(printf '%s' "$got" | grep -c '^### Fixed')" "1" "one heading per kind after folding"
	_eq "$(printf '%s' "$got" | grep -c '^- hand (#1)$')" "1" "keeps the hand-written entry"
	_eq "$(printf '%s' "$got" | grep -c '^- fragment (#2)$')" "1" "adds the fragment entry"
	_eq "$(printf '%s' "$got" | grep -c '^- old$')" "1" "leaves released sections alone"
	# The released section's own `### Added` must survive untouched, which is the
	# difference between folding a section and rewriting a file.
	_eq "$(printf '%s' "$got" | grep -c '^### Added')" "1" "does not touch headings below [Unreleased]"

	# 2. Canonical order, regardless of the order things were written in.
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '### Security' '' '- s' '' '## [1.0.0] - 2026-01-01' > "$tmp/cl2.md"
	printf '%s\n' '### Fixed' '- f' '### Added' '- a' > "$tmp/r2.md"
	_eq "$(merge_unreleased "$tmp/cl2.md" "$tmp/r2.md" | grep '^### ' | tr '\n' ' ' | sed 's/ $//')" \
		"### Added ### Fixed ### Security" "emits kinds in the declared order"

	# 3. An empty [Unreleased] with nothing pending stays empty, and stays valid.
	#    This is the shape right after a GA cut, and the shape cut-release.sh
	#    hands this script on a release with no fragments at all.
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-01' '- x' > "$tmp/cl3.md"
	: > "$tmp/r3.md"
	_eq "$(merge_unreleased "$tmp/cl3.md" "$tmp/r3.md")" \
		"$(printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-01' '- x')" \
		"empty in, empty out, byte for byte"

	# 4. A kind changie does not declare must not vanish. It lands after the
	#    known kinds rather than being dropped on the floor.
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '### Notes' '' '- n' '' '## [1.0.0] - 2026-01-01' > "$tmp/cl4.md"
	printf '%s\n' '### Added' '- a' > "$tmp/r4.md"
	_eq "$(merge_unreleased "$tmp/cl4.md" "$tmp/r4.md" | grep '^### ' | tr '\n' ' ' | sed 's/ $//')" \
		"### Added ### Notes" "keeps an unknown kind, after the known ones"

	# 5. Continuation lines and prose are not bullets and are not dropped.
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' 'A note about this release.' '' '### Fixed' '' '- a (#1)' '  with a second line' '' '## [1.0.0] - 2026-01-01' > "$tmp/cl5.md"
	: > "$tmp/r5.md"
	got="$(merge_unreleased "$tmp/cl5.md" "$tmp/r5.md")"
	_eq "$(printf '%s' "$got" | grep -c 'A note about this release.')" "1" "keeps section prose"
	_eq "$(printf '%s' "$got" | grep -c 'with a second line')" "1" "keeps a bullet continuation line"

	# 6. Writing both the fragment and the hand entry yields one bullet.
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '### Fixed' '' '- same (#9)' '' '## [1.0.0] - 2026-01-01' > "$tmp/cl6.md"
	printf '%s\n' '### Fixed' '- same (#9)' > "$tmp/r6.md"
	_eq "$(merge_unreleased "$tmp/cl6.md" "$tmp/r6.md" | grep -c '^- same (#9)$')" "1" "collapses an exact duplicate"

	# 7. [Unreleased] as the last section in the file must still get the block.
	#    Nothing terminates it, so only the END rule can emit, and an earlier
	#    draft of this script emitted nothing at all in that shape.
	printf '%s\n' '# Changelog' '' '## [Unreleased]' > "$tmp/cl7.md"
	printf '%s\n' '### Added' '- a' > "$tmp/r7.md"
	_eq "$(merge_unreleased "$tmp/cl7.md" "$tmp/r7.md" | grep -c '^- a$')" "1" "folds into a trailing [Unreleased]"

	# 8. A multi-paragraph entry keeps its paragraphs. Entries in this file run
	#    to several paragraphs each, and an earlier draft swallowed every blank
	#    line inside a section, welding them into one wall of text. The real
	#    CHANGELOG caught it on the first run; these fixtures could not, because
	#    every one of them was a single-line bullet.
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '### Changed' '' '- **A thing.** First paragraph.' '' '  Second paragraph of the same entry.' '' '- A second entry.' '' '## [1.0.0] - 2026-01-01' > "$tmp/cl8.md"
	printf '%s\n' '### Changed' '- from a fragment' > "$tmp/r8.md"
	got="$(merge_unreleased "$tmp/cl8.md" "$tmp/r8.md")"
	_eq "$(printf '%s' "$got" | grep -c 'Second paragraph of the same entry.')" "1" "keeps a paragraph inside an entry"
	_eq "$(printf '%s' "$got" | sed -n '/^### Changed/,/^## \[1/p' | grep -c '^$')" "5" "keeps the blank lines that separate paragraphs"

	# 8b. A fragment body of several paragraphs, which is how entries in this
	#     file are actually written. changie renders `- {{.Body}}`, so only the
	#     first line carries the bullet and the rest come back flush left; a
	#     flush-left line after a blank one ends the list item in markdown, and
	#     the second paragraph falls out of its own entry. Every fixture above
	#     is a one-line bullet, which is why none of them could see this.
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-01' > "$tmp/cl8b.md"
	printf '%s\n' '### Fixed' '- **A thing** (#1). First paragraph that wraps' 'onto a second line.' '' 'And a second paragraph.' > "$tmp/r8b.md"
	got="$(merge_unreleased "$tmp/cl8b.md" "$tmp/r8b.md")"
	_eq "$(printf '%s' "$got" | grep -c '^onto a second line.$')" "0" "indents a wrapped continuation line"
	_eq "$(printf '%s' "$got" | grep -c '^  onto a second line.$')" "1" "keeps it inside the bullet"
	_eq "$(printf '%s' "$got" | grep -c '^  And a second paragraph.$')" "1" "keeps the second paragraph inside the bullet"
	_eq "$(printf '%s' "$got" | sed -n '/^### Fixed/,/^## \[1/p' | grep -c '^$')" "3" "keeps the paragraph break between them"

	# 9. KINDS above is a second copy of the kind list in .changie.yaml, kept
	#    because this script must order the headings without parsing YAML. Two
	#    places holding one decision is the shape of half the bugs in this
	#    repository, so the copy is checked rather than trusted: a kind added to
	#    .changie.yaml and not here would render, but out of position.
	local declared
	declared="$(awk '/^kinds:/{k=1;next} k&&/^  - label:/{printf "%s%s", sep, $3; sep=","} k&&/^[^ -]/{exit}' "$ROOT/.changie.yaml")"
	_eq "$declared" "$KINDS" "KINDS matches the kinds declared in .changie.yaml"

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

die() { printf 'FATAL: %s\n' "$*" >&2; exit 1; }

rendered=""
case "${1:-}" in
	--render)
		rendered="${2:-}"
		[ -f "$rendered" ] || die "--render needs a file"
		;;
	"")
		die "usage: $(basename "$0") <version> | --render <file> | --self-test"
		;;
	*)
		version="$1"
		# No fragments is not an error. Most rc cuts during the transition will
		# have none, and a release whose entries were all typed by hand is a
		# perfectly good release.
		shopt -s nullglob
		fragments=("$FRAGMENT_DIR"/*.yaml "$FRAGMENT_DIR"/*.yml)
		shopt -u nullglob
		if [ "${#fragments[@]}" -eq 0 ]; then
			echo "OK: no pending fragments under .changes/unreleased; CHANGELOG untouched."
			exit 0
		fi
		command -v changie >/dev/null 2>&1 ||
			die "changie is not installed, and ${#fragments[@]} fragment(s) are pending. See https://changie.dev (brew install changie)"
		rendered="$(mktemp)"
		trap 'rm -f "${rendered:-}"' EXIT
		# --dry-run prints the batch and touches nothing: no .changes/<version>.md,
		# no rewrite of CHANGELOG.md. changie renders, this script decides what
		# the CHANGELOG becomes, and the fragments are removed below only after
		# the merged file is in place.
		(cd "$ROOT" && changie batch "$version" --dry-run) > "$rendered" ||
			die "changie batch $version --dry-run failed"
		;;
esac

[ -f "$CHANGELOG" ] || die "$CHANGELOG not found"
grep -q '^## \[Unreleased\]' "$CHANGELOG" || die "CHANGELOG has no [Unreleased] section"

merged="$(mktemp)"
merge_unreleased "$CHANGELOG" "$rendered" > "$merged"
# A merge that produced a shorter file than it started with has lost something.
# Entries only ever move into this section; nothing here removes a line.
[ "$(wc -l < "$merged")" -ge "$(wc -l < "$CHANGELOG")" ] ||
	{ rm -f "$merged"; die "refusing to write a CHANGELOG shorter than the one it replaced"; }
mv "$merged" "$CHANGELOG"

if [ "${#fragments[@]:-0}" -gt 0 ]; then
	rm -f "${fragments[@]}"
	echo "OK: folded ${#fragments[@]} fragment(s) into [Unreleased] and removed them."
else
	echo "OK: folded $(basename "$rendered") into [Unreleased]."
fi
