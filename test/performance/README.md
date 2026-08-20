# Resource-Budget Scenarios

These Linux-only scenarios measure the exporter process directly from procfs.
They build temporary binaries, run a loopback OpenTelemetry Collector, create a
secure report source, enforce the version 0 limits, and remove all processes and
temporary files on exit.

Run the five-minute acceptance scenarios from the repository root:

```bash
bash test/performance/idle-budget.sh
bash test/performance/load-budget.sh
```

GitHub Actions runs both scenarios in parallel from the weekly and manually
dispatched `Performance budgets` workflow. They are intentionally excluded from
pull-request CI.

The idle scenario requires median CPU below 1% of one core and peak RSS below
64 MiB. The load scenario writes at least 1 MiB/s, rotates every ten records,
and requires average CPU below 10% of one core and peak RSS below 128 MiB.

Environment overrides such as `MEASUREMENT_DURATION`, `SAMPLE_INTERVAL`,
`EXPORT_INTERVAL`, `RESCAN_INTERVAL`, and the `MAX_*` limits exist for short
harness calibration. A run using overridden duration or thresholds is not an
acceptance result.