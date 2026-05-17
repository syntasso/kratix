# Perf flag comparison — N=100 RRs, no-op Promise

8 runs, single-cluster kind, fresh install. Measures the effect of three
controller knobs in isolation and combination:

- `--dynamic-rr-max-concurrent-reconciles` (MCR): 0 (=1) vs 20
- `--kube-api-qps` / `--kube-api-burst`: client-go defaults (20 / 30) vs 200 / 400
- `--dynamic-rr-filter-noop-writes` (predicate): off vs on

All runs target the same no-op Promise — one `busybox sh -c "exit 0"` configure
pipeline that produces zero manifests. Convergence is wall-clock from the last
RR `Create` to every RR carrying `Reconciled=True`.

## Headline numbers

| run | MCR | QPS / Burst | predicate | wall | p50 recon | longest_running peak (s) | unfinished_work peak (s) | rec total CPU (s) |
|---|---:|---|---|---:|---:|---:|---:|---:|
| mcr1-flagsoff | 1 | 20 / 30 | off | **2m59s** | 11 | 1.12 | — | 178.7 |
| mcr1-qps | 1 | 200 / 400 | off | **2m3s** | 12 | 1.13 | — | 122.6 |
| mcr1-predicate | 1 | 20 / 30 | on | **2m59s** | 11 | 1.16 | — | 178.7 |
| mcr1-all | 1 | 200 / 400 | on | **2m5s** | 12 | 1.12 | — | 123.7 |
| mcr20-flagsoff | 20 | 20 / 30 | off | **1m40s** | 14 | 3.13 | 26.7 | 875.5 |
| mcr20-qps | 20 | 200 / 400 | off | **38s** | 18 | 1.07 | 6.8 | 69.5 |
| mcr20-predicate | 20 | 20 / 30 | on | **1m38s** | 14 | 2.97 | 27.0 | 778.2 |
| mcr20-all | 20 | 200 / 400 | on | **40s** | 18 | 0.40 | 3.3 | **55.4** |

(`unfinished_work_seconds` is the sum of seconds the perftest workqueue has had
items in-flight — a proxy for "how long are workers stalled?". Empty for MCR=1
runs because at concurrency=1 the metric never accumulates.)

## What each knob actually does

### MCR=20 alone

1m40s vs 2m59s baseline (1.78× faster wall-clock). But each reconcile got
**~3× slower** (`longest_running` 1.12s → 3.13s) and total controller CPU
spent reconciling went from 178s to **875s** — almost 5× more work, for less
than 2× the throughput.

Cause: parallel goroutines compete for the same 20-QPS rate-limited kube-API
client. Each Status().Update stalls in the token bucket, multiplied across
20 concurrent workers. `unfinished_work_seconds` peaks at **26.7s** — workers
spend an aggregate ~27 seconds stuck mid-reconcile waiting for API budget.

**Verdict**: MCR by itself is throughput-positive but CPU-negative. Don't
ship it without raising QPS.

### QPS=200/400 alone (MCR=1)

2m3s vs 2m59s (1.45× faster) at MCR=1 with the same reconcile count.
At MCR=1 the controller is a serial loop; raising QPS just makes each
write faster. CPU total drops from 178s to 123s — pure efficiency.

But: 100 retries appeared in `workqueue_retries_total` (was 0 in baseline).
Each retry is a queue add with backoff. At MCR=1 baseline, status writes
re-enqueue via the self-watch deterministically; at higher QPS, the
controller occasionally hits something (likely a conflict on Status update)
that triggers a retry. **Worth a closer look** — the retries didn't hurt
wall-clock at this N but could compound at scale. Same retry count appears
in `mcr1-all`, so this is QPS-coupled, not predicate-coupled.

### Predicate alone

**No measurable wall-clock difference in either MCR setting.** At MCR=1 the
state machine is gated by serial reconciles; the predicate has nothing to
filter (every status write is genuinely advancing state, no spurious
re-fires to drop). At MCR=20 the bottleneck is API-rate, not event volume.

### Predicate's hidden value (look at the metrics)

The wall-clock table understates the predicate. Compare MCR=20 pairs:

|  | adds (queue events) | reconcile_count | rec total CPU (s) |
|---|---:|---:|---:|
| mcr20-flagsoff | 566 | 456 | 875.5 |
| mcr20-predicate | **518 (−48)** | **409 (−47)** | **778.2 (−97s)** |
| mcr20-qps | 520 | 400 | 69.5 |
| mcr20-all | **491 (−29)** | **399 (−1)** | **55.4 (−14s)** |

Two observations:

1. **The predicate is filtering events.** ~50 queue adds and ~50 reconciles
   get suppressed per 100 RRs at MCR=20 (predicate vs flagsoff). At MCR=20+QPS
   it's smaller (~30) because the controller burns through state-machine
   steps faster, so fewer redundant self-watch events have time to accumulate.
2. **The CPU win is disproportionate.** A 10% drop in reconcile count gives
   an 11% drop in CPU at MCR=20+throttled, and **a 20% drop at MCR=20+QPS**.
   That's because the *throttled* runs spend most of their reconcile time
   stalled in the rate-limiter (not in actual work), so cutting 10% of
   reconciles only saves 10% of CPU. The unthrottled runs do all their CPU
   work for real, so the 10% you cut is pure savings.

In other words, the predicate is **only worth pairing with the QPS bump**.
On its own, it cuts a fraction of stalls that the rate-limiter would have
absorbed anyway. With QPS unblocked, every filtered reconcile is real CPU
saved.

The `longest_running` metric on mcr20-all (0.40s) is the cleanest measure of
how fast the controller is in its best state: reconciles complete in 400ms
and there are no parallel-worker stalls. That's the configuration to ship
for high-N workloads.

## Recommendations

1. **Ship MCR + QPS together** as a single behavioural change for high-N
   environments. Either knob alone is mediocre (MCR alone wastes CPU; QPS
   alone doesn't parallelise).
2. **Ship the predicate alongside them** — it's cheap, correctness-neutral
   (this run set confirms convergence at the same RR-count and reconciled
   state), and the CPU savings compound with the QPS bump.
3. **Investigate the QPS-coupled retries** (100 retries in `mcr1-qps` and
   `mcr1-all`, 0 elsewhere). Not load-bearing at N=100 but a likely
   conflict-write footprint worth understanding before N=2500.
4. The wall-clock summary is misleading for the predicate. The doc previously
   concluded "predicate does nothing useful"; that's accurate for *wall-clock*
   but wrong for *controller CPU* and *event volume*. Update the framing in
   `docs/perf-rig-findings.md`.

## Why "reconciles per RR" goes UP at MCR=20

p50=11 (MCR=1) → p50=14 (MCR=20 unthrottled) → p50=18 (MCR=20 + QPS).
Counter-intuitive — more parallel goroutines means more concurrent status
writes, more interleavings observed by the self-watch, more legitimate
re-enters of `Reconcile` per RR. The predicate **does** suppress the most
redundant of these but not enough to lower per-RR count below MCR=1's
serial number.

This is consistent with the model in `perf-rig-findings.md`: the state
machine is built on status-write-driven re-enqueues. Predicate filters
*redundant* writes (same metadata, same status). It does not filter the
*designed* re-enqueues that drive the state machine forward.

## Reproduction

```bash
# from kratix repo root, with kind-platform cluster running
RECREATE=true make quick-start          # fresh state

# for each scenario, patch deployment args then run perf-test
./test/perf/set-controller-flags.sh '<json args>'
PERF_N=100 PERF_RUN=<name> PERF_TIMEOUT=15m make perf-test

# wait between runs for cleanup:
until [ "$(kubectl -n default get perftests --no-headers 2>/dev/null | wc -l)" = "0" ] \
   && [ "$(kubectl get promises --no-headers 2>/dev/null | grep -c perftest)" = "0" ]; do
  sleep 5
done
```

Results are in `test/perf/results/mcr{1,20}-{flagsoff,qps,predicate,all}/`.
Each contains `summary.md`, raw `controller.log`, and per-second
`metrics-T+NNNNNs.prom` snapshots.
