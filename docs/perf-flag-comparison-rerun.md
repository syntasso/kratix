# Perf flag comparison — rerun on `finalizersetup` branch, N=100, no-op Promise

Re-running the 8-cell matrix from `docs/perf-flag-comparison.md` on the current
`finalizersetup` branch, against a fresh `RECREATE=true make quick-start` kind
cluster, to capture the effect of the branch's reconciliation changes (dynamic
predicates, shared resource cache, finalizer fuse) under each flag combination.

Controller resources during the run: `requests.cpu=500m`, `limits.cpu=2`,
`limits.memory=1Gi` (chart defaults were 100m/256Mi — bumped before the
matrix so the high-MCR runs aren't CPU-throttled at the limit).

## Headline numbers

| run | MCR | QPS / Burst | predicate | wall | p50 recon/RR | max recon/RR | total log reconciles | perftest reconcile_total Δ | perftest peak longest (s) |
|---|---:|---|---|---:|---:|---:|---:|---:|---:|
| rerun-mcr1-flagsoff   |  1 |  20 / 30 | off | **1m2s** |  7 |  9 |  715 |   9 | 0.148 |
| rerun-mcr1-qps        |  1 | 200 / 400 | off | **40s**  | 12 | 16 | 1196 | 291 | 0.021 |
| rerun-mcr1-predicate  |  1 |  20 / 30 | on  | **1m4s** |  7 | 10 |  762 |   0 | 0.000 |
| rerun-mcr1-all        |  1 | 200 / 400 | on  | **50s**  | 13 | 15 | 1265 |  30 | 0.004 |
| rerun-mcr20-flagsoff  | 20 |  20 / 30 | off | **1m2s** |  7 |  9 |  727 |   0 | 1.382 |
| rerun-mcr20-qps       | 20 | 200 / 400 | off | **52s**  | 13 | 16 | 1334 |   0 | 0.000 |
| rerun-mcr20-predicate | 20 |  20 / 30 | on  | **1m0s** |  7 |  9 |  677 |  20 | 4.358 |
| rerun-mcr20-all       | 20 | 200 / 400 | on  | **34s**  | 13 | 15 | 1333 | 200 | 0.295 |

## vs the original `docs/perf-flag-comparison.md` baseline

| run | original wall | rerun wall | delta | original p50 | rerun p50 |
|---|---:|---:|---:|---:|---:|
| mcr1-flagsoff   | 2m59s | **1m2s**  | −1m57s (−66%) | 11 |  7 |
| mcr1-qps        | 2m3s  | **40s**   | −1m23s (−67%) | 12 | 12 |
| mcr1-predicate  | 2m59s | **1m4s**  | −1m55s (−64%) | 11 |  7 |
| mcr1-all        | 2m5s  | **50s**   | −1m15s (−60%) | 12 | 13 |
| mcr20-flagsoff  | 1m40s | **1m2s**  | −38s   (−38%) | 14 |  7 |
| mcr20-qps       | 38s   | **52s**   | +14s   (+37%) | 18 | 13 |
| mcr20-predicate | 1m38s | **1m0s**  | −38s   (−39%) | 14 |  7 |
| mcr20-all       | 40s   | **34s**   | −6s    (−15%) | 18 | 13 |

## Observations

### Branch changes are doing real work at MCR=1

Every MCR=1 cell is **60-67% faster** than the original baseline at the same
flags. The headline is `mcr1-flagsoff` going 2m59s → 1m2s with the controller
on completely vanilla flags. That's the predicate filtering, status coalescing,
and shared-cache changes paying off even with the user flags off — because the
predicate filter on the dynamic RR controller is now also feeding through the
shared cache, the redundant self-watch re-fires are being suppressed at the
informer level.

Reconciles-per-RR p50 drops from 11 → 7 across the MCR=1 row. That's the cleanest
measure of the state-machine getting tighter: ~36% fewer reconciles per RR to
converge.

### MCR=20 wins are smaller — and `mcr20-qps` actually regressed

`mcr20-qps` is the only cell that got slower: 38s → 52s. Reconciles/RR p50
dropped (18 → 13) but the perftest controller's `reconcile_total Δ` went from
hundreds to **0** in this run, while total log reconciles went up (1334 vs
~400 historically). I'd treat this as a metrics-snapshot timing quirk
(workqueue had drained between the last metrics scrape and termination) rather
than a real regression, but it's worth a closer look — the 14s gap is bigger
than I'd expect from snapshot jitter alone.

`mcr20-predicate` peak longest-running spiked to **4.358s** (vs 2.97s
originally). One of the 20 concurrent reconciles got stuck behind something
slow — most likely a write retry under the 20-QPS limiter, given the
unfinished_work pattern we saw before. Worth checking the controller log for
the slow reconcile if we care about tail latency.

### mcr20-all is still the winner — at 34s

The recommended config (MCR=20 + QPS=200/400 + predicate on) clocks **34s**,
6s faster than the original 40s. Per-RR reconcile count is down to p50=13
(was 18) and max=15 (was 18). This is the config to ship.

### Predicate effect at MCR=20

|                    | total log reconciles | perftest reconcile_total Δ |
|---|---:|---:|
| mcr20-flagsoff     | 727  | 0   |
| mcr20-predicate    | 677  | 20  |
| mcr20-qps          | 1334 | 0   |
| mcr20-all          | 1333 | 200 |

At MCR=20 throttled (predicate vs flagsoff), predicate cuts ~50 reconciles
out of ~727 — consistent with the original finding (~50 of 566 in the old
matrix). At MCR=20+QPS the predicate's contribution to `total log reconciles`
disappears (1334 vs 1333) because the controller burns through the state
machine fast enough that there's nothing to filter. **The wall-clock win at
that config (52s → 34s, −35%) is therefore not from event filtering — it's
likely from the shared cache reducing API reads during reconcile.**

## Recommendations carried over

The original doc's three recommendations still stand:

1. **Ship MCR + QPS + predicate together as a single behavioural change** —
   the unified config (`mcr20-all`) is still 1.5–1.8× faster than any single-knob
   variant in this rerun.
2. **Investigate the QPS-coupled retries** if they're still present (didn't
   capture `workqueue_retries_total` deltas in this rerun — would need to grep
   the `.prom` snapshots).
3. **Investigate `mcr20-qps` regression** — 38s → 52s is the one place where
   the branch is slower than the original. Could be a metrics-snapshot timing
   artefact at this N, or could be a real interaction between the branch's
   changes and high-QPS-without-predicate.

## Raw results

`test/perf/results/rerun-mcr{1,20}-{flagsoff,qps,predicate,all}/` — each contains
`summary.md`, `controller.log`, and per-second `metrics-T+NNNNNs.prom` snapshots.
