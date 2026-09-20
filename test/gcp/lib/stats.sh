#!/usr/bin/env bash
#
# The arithmetic that turns a raw series into a verdict, kept apart from
# anything that talks to a cluster so it can be tested without one.
#
# THE POINT OF THIS FILE is that "saturated" is defined here, in code, before
# any run happens, rather than decided afterwards by looking at a graph. A
# threshold chosen after seeing the data is not a threshold, and a number nobody
# can recheck is worse than no number.
#
# The definition, in full:
#
#   For one phase (dispatch, schedule, pull, start) measured at rising
#   concurrency levels L1 < L2 < ... < Ln:
#
#   baseline      = p95 of that phase at L1, the lowest level.
#   saturated(Lk) = p95(Lk) >= SAT_MULTIPLE * baseline
#                   AND p95(Lk-1) >= SAT_SUSTAIN * baseline
#
#   The second clause is what separates saturation from noise. One level that
#   spikes and comes back is a shared-cloud artifact: a neighbour's job, a
#   control-plane upgrade, a slow disk. Saturation is a wall, so it shows up at
#   Lk AND at the level before it. A single bad level is reported as a spike and
#   is NOT a verdict.
#
#   A level with fewer than MIN_SAMPLES observations is INCONCLUSIVE and can
#   never be saturated, whatever its p95 says. A p95 over four samples is the
#   maximum wearing a percentile's name.
#
#   The saturating component is the phase whose FIRST saturated level is the
#   lowest. Two phases that saturate at the same level are reported as a tie,
#   not broken by argument order, because "the API server and image pulls went
#   at the same time" is a real and interesting answer and picking one of them
#   would be inventing a result.
#
#   No phase saturating by Ln is `no-saturation-observed`. That is NOT "it
#   scales". It is "the ceiling is above Ln", which is a different and much
#   weaker claim, and the report says so in those words.
#
# Sourced, not executed, except for --self-test.

# p_of computes a percentile by the nearest-rank method over a newline-separated
# series of numbers on stdin. Nearest-rank and not interpolation: with the
# sample counts these experiments produce, interpolation invents precision that
# the measurement does not have, and nearest-rank always returns a value that
# was actually observed, which is the property that lets a reader go find it in
# the raw series.
#
# Empty input returns nothing and a non-zero status, rather than 0. A p95 of
# "no data" is not zero; reporting it as zero is how an empty verdict becomes a
# fast-looking one.
p_of() { # <percentile 0..100>  (series on stdin)
  local pct="$1"
  sort -g | awk -v pct="$pct" '
    { v[n++] = $1 }
    END {
      if (n == 0) { exit 1 }
      r = int(pct / 100 * n + 0.999999)   # ceil, guarded against fp drift
      if (r < 1) r = 1
      if (r > n) r = n
      printf "%.4f", v[r-1]
    }'
}

# count_of and max_of exist so a caller never has to re-read a series twice with
# two different tools and risk them disagreeing about what is in it.
count_of() { awk 'NF{n++} END{printf "%d", n+0}'; }
max_of()   { sort -g | awk '{v=$1} END{ if (NR==0) exit 1; printf "%.4f", v }'; }

# ---------------------------------------------------------------- the verdict

SAT_MULTIPLE="${SAT_MULTIPLE:-3.0}"   # p95 at Lk vs p95 at L1 for "saturated"
SAT_SUSTAIN="${SAT_SUSTAIN:-2.0}"     # p95 at Lk-1 vs p95 at L1 for "sustained"
MIN_SAMPLES="${MIN_SAMPLES:-20}"      # below this a level is inconclusive, never saturated

# level_verdict classifies ONE level of ONE phase. It is the whole decision, and
# it is a pure function of five numbers so the self-test can drive every branch
# without a cluster.
#
# Answers exactly one of: inconclusive | ok | spike | saturated.
#
#   inconclusive  too few samples to say anything. Never upgraded by a big p95.
#   ok            below the multiple.
#   spike         over the multiple, but the level before it was not elevated.
#                 One level out of line on a shared cloud is noise until the
#                 next level agrees with it.
#   saturated     over the multiple, and the level before it was already
#                 elevated. A wall, not a bump.
level_verdict() { # <p95_here> <p95_prev> <baseline> <samples_here> <is_first_level 0|1>
  local here="$1" prev="$2" base="$3" n="$4" first="$5"
  if awk -v n="$n" -v m="$MIN_SAMPLES" 'BEGIN{exit !(n+0 < m+0)}'; then
    echo inconclusive; return 0
  fi
  # The first level IS the baseline, so it cannot be a multiple of itself.
  if [ "$first" = "1" ]; then echo ok; return 0; fi
  if awk -v b="$base" 'BEGIN{exit !(b+0 <= 0)}'; then
    # A zero or negative baseline makes every ratio meaningless. Refuse to
    # divide by it rather than emit an infinity that reads as a finding.
    echo inconclusive; return 0
  fi
  if awk -v h="$here" -v b="$base" -v m="$SAT_MULTIPLE" 'BEGIN{exit !(h+0 >= m+0 * b+0)}'; then
    if awk -v p="$prev" -v b="$base" -v s="$SAT_SUSTAIN" 'BEGIN{exit !(p+0 >= s+0 * b+0)}'; then
      echo saturated; return 0
    fi
    echo spike; return 0
  fi
  echo ok
}

# first_saturated_level walks a phase's per-level verdicts (space separated, in
# level order) and returns the 1-based index of the first `saturated`, or 0.
first_saturated_level() { # <verdict words in level order>
  local i=0 v
  for v in "$@"; do
    i=$((i + 1))
    [ "$v" = "saturated" ] && { echo "$i"; return 0; }
  done
  echo 0
}

# saturating_component takes "<phase>:<first_saturated_index>" pairs and answers
# which phase hit the wall first. Index 0 means that phase never saturated.
#
# A tie is reported as a tie. The remedies for the four phases are completely
# different (a scheduler tick, an API server, an image, a node pool), so
# arbitrarily naming one of two simultaneous phases would send an operator to
# fix the wrong thing with full confidence.
saturating_component() { # <phase:index> ...
  local best=0 winners="" p phase idx
  for p in "$@"; do
    phase="${p%%:*}"; idx="${p##*:}"
    [ "$idx" = "0" ] && continue
    if [ "$best" = "0" ] || [ "$idx" -lt "$best" ]; then
      best="$idx"; winners="$phase"
    elif [ "$idx" = "$best" ]; then
      winners="$winners+$phase"
    fi
  done
  [ "$best" = "0" ] && { echo "no-saturation-observed"; return 0; }
  echo "$winners@L$best"
}

# ------------------------------------------------------------ verdict guards

# The local soak's review found a verdict file that had silently become `{}`.
# An empty verdict is not a passing verdict; it is a missing one, and it looked
# identical to success. These two refuse that shape.

# series_is_usable rejects a series that cannot support the claim made from it.
series_is_usable() { # <count> <label>
  local n="$1" label="$2"
  if [ "${n:-0}" -lt 1 ]; then
    echo "EMPTY: $label produced no observations at all, so nothing can be concluded from it" >&2
    return 1
  fi
  if [ "$n" -lt "$MIN_SAMPLES" ]; then
    echo "THIN: $label has $n observations, below the $MIN_SAMPLES floor; percentiles over it are reported as inconclusive" >&2
    return 2
  fi
  return 0
}

# verdict_is_publishable refuses to write a conclusion built from nothing. It is
# the guard the soak's `{}` needed: a run that measured nothing must exit
# non-zero and say so, never write a file that a later reader mistakes for a
# result.
verdict_is_publishable() { # <phases_measured> <levels_measured> <total_observations>
  local phases="$1" levels="$2" obs="$3"
  if [ "${phases:-0}" -lt 1 ] || [ "${levels:-0}" -lt 1 ] || [ "${obs:-0}" -lt 1 ]; then
    echo "REFUSING to write a verdict: phases=$phases levels=$levels observations=$obs." >&2
    echo "A verdict built from nothing is indistinguishable from a passing one, which is exactly how an empty {} gets read as a result." >&2
    return 1
  fi
  if [ "$levels" -lt 2 ]; then
    echo "REFUSING to write a saturation verdict from $levels level(s): saturation is a statement about a TREND across rising concurrency." >&2
    echo "One level is one sample of one shape. Re-run with at least two levels." >&2
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------- self-test

self_test() {
  local fail=0
  _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  # -------------------------------------------------- percentiles
  _eq "$(printf '1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n' | p_of 50)" "5.0000" "p50 by nearest rank is an observed value"
  _eq "$(printf '1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n' | p_of 95)" "10.0000" "p95 of ten values is the tenth"
  _eq "$(printf '5\n' | p_of 95)" "5.0000" "a single value is its own p95"
  # Out of order input must not change the answer: the caller appends as it
  # measures, and nothing guarantees arrival order. The property is that the
  # same SET gives the same percentile whatever order it arrives in, so the two
  # are compared to each other rather than to a number written out by hand.
  _eq "$(printf '10\n1\n5\n3\n' | p_of 50)" "$(printf '1\n3\n5\n10\n' | p_of 50)" \
      "an unsorted series gives the same p50 as the sorted same set"
  # Nearest rank over an even count takes the LOWER middle (ceil(0.5*4)=2nd of
  # 1,3,5,10), which is an observed value. Stated explicitly so nobody later
  # "fixes" it into an interpolating p50 that returns a number never measured.
  _eq "$(printf '10\n1\n5\n3\n' | p_of 50)" "3.0000" "nearest rank returns an observed value, never an interpolated one"
  # Numeric and not lexicographic: sort without -g puts 10 before 9.
  _eq "$(printf '9\n10\n11\n' | p_of 100)" "11.0000" "the series sorts numerically, not as text"
  printf '' | p_of 95 >/dev/null 2>&1 \
    && { echo "  FAIL an empty series produced a percentile"; fail=1; } \
    || echo "  ok   an empty series refuses to produce a percentile rather than returning 0"
  _eq "$(printf '1\n2\n3\n' | count_of)" "3" "count_of counts"
  _eq "$(printf '' | count_of)" "0" "count_of of nothing is zero"

  # -------------------------------------------------- level_verdict
  # Baseline 1.0, multiple 3.0, sustain 2.0, floor 20 samples.
  _eq "$(level_verdict 1.0 0    1.0 50 1)" "ok" "the first level is the baseline and is never a multiple of itself"
  _eq "$(level_verdict 1.5 1.0  1.0 50 0)" "ok" "a 1.5x rise is not saturation"
  _eq "$(level_verdict 9.0 1.0  1.0 50 0)" "spike" "one level over the bar with a calm level before it is a spike, not a verdict"
  _eq "$(level_verdict 9.0 2.5  1.0 50 0)" "saturated" "over the bar with the level before it already elevated is saturation"
  _eq "$(level_verdict 3.0 2.0  1.0 50 0)" "saturated" "exactly on both bars counts: the threshold was written down in advance"
  # THE guard that keeps a thin level from becoming a headline.
  _eq "$(level_verdict 99.0 50.0 1.0 4 0)" "inconclusive" "four samples cannot be saturated however large the p95"
  _eq "$(level_verdict 99.0 50.0 1.0 19 0)" "inconclusive" "one sample below the floor is still below the floor"
  _eq "$(level_verdict 99.0 50.0 1.0 20 0)" "saturated" "the floor itself is enough"
  # A zero baseline would make every ratio infinite and every level a finding.
  _eq "$(level_verdict 5.0 5.0 0 50 0)" "inconclusive" "a zero baseline refuses to divide rather than reporting everything as saturated"

  # -------------------------------------------------- first_saturated_level
  _eq "$(first_saturated_level ok ok saturated ok)" "3" "the FIRST saturated level is the one that counts"
  _eq "$(first_saturated_level ok ok ok)" "0" "no saturation is zero, not an error"
  _eq "$(first_saturated_level ok spike saturated)" "3" "a spike does not count as the wall"
  _eq "$(first_saturated_level inconclusive saturated)" "2" "an inconclusive level is stepped over, not treated as clean"

  # -------------------------------------------------- saturating_component
  # This is the answer the whole experiment exists to produce.
  _eq "$(saturating_component dispatch:3 schedule:0 pull:5 start:0)" "dispatch@L3" "the lowest saturated level names the component"
  _eq "$(saturating_component dispatch:0 schedule:0 pull:0 start:0)" "no-saturation-observed" "nothing saturating is its own answer, not a pass"
  _eq "$(saturating_component dispatch:4 pull:4)" "dispatch+pull@L4" "a tie is reported as a tie rather than resolved by argument order"
  # Argument order must not decide a tie. Same inputs, reversed.
  _eq "$(saturating_component pull:4 dispatch:4)" "pull+dispatch@L4" "a tie names both phases whichever order they arrive in"
  _eq "$(saturating_component dispatch:5 pull:2)" "pull@L2" "a later dispatch wall does not outrank an earlier pull wall"

  # -------------------------------------------------- the empty-verdict guards
  verdict_is_publishable 0 0 0 2>/dev/null \
    && { echo "  FAIL a verdict built from nothing was publishable"; fail=1; } \
    || echo "  ok   a verdict built from nothing is refused, which is what {} needed"
  verdict_is_publishable 4 1 500 2>/dev/null \
    && { echo "  FAIL a one-level saturation verdict was publishable"; fail=1; } \
    || echo "  ok   a single concurrency level cannot support a saturation verdict"
  verdict_is_publishable 4 3 500 2>/dev/null \
    && echo "  ok   a verdict with phases, levels and observations is publishable" \
    || { echo "  FAIL a well-formed verdict was refused"; fail=1; }
  series_is_usable 0 "dispatch@L1" 2>/dev/null \
    && { echo "  FAIL an empty series was usable"; fail=1; } \
    || echo "  ok   an empty series is refused"
  series_is_usable 5 "dispatch@L1" 2>/dev/null; [ "$?" = "2" ] \
    && echo "  ok   a thin series is flagged thin rather than silently used" \
    || { echo "  FAIL a thin series was not flagged"; fail=1; }

  [ "$fail" = "0" ] && { echo "stats self-test: ok"; return 0; }
  return 1
}

# Only when EXECUTED, never when sourced. A sourced file inherits the caller's
# positional parameters, so without this guard `netpol.sh --self-test` would run
# this library's self-test and exit before the runner's own ever defined itself.
if [ "${BASH_SOURCE[0]}" = "$0" ] && [ "${1:-}" = "--self-test" ]; then
  self_test; exit $?
fi
return 0 2>/dev/null || true
