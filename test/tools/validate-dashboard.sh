#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
promtool_bin=${PROMTOOL_BIN:-promtool}
dashboard_path="$repo_root/deploy/azure-vm/grafana/dashboards/blobfuse-ci-runs.json"
compose_path="$repo_root/deploy/azure-vm/compose-existing-vm.yaml"
installer_path="$repo_root/deploy/azure-vm/install-existing-vm.sh"

command -v "$promtool_bin" >/dev/null 2>&1 || {
    printf 'promtool not found: %s\n' "$promtool_bin" >&2
    exit 1
}

grep -F --quiet \
    'GF_DASHBOARDS_DEFAULT_HOME_DASHBOARD_PATH: /var/lib/grafana/dashboards/blobfuse-ci-runs.json' \
    "$compose_path"
grep -F --quiet 'handle /blobfuse-grafana/* {' "$installer_path"
if grep -F --quiet 'handle_path /blobfuse-grafana/* {' "$installer_path"; then
    printf '%s\n' 'Grafana proxy must preserve the configured subpath' >&2
    exit 1
fi

python3 - "$dashboard_path" "$promtool_bin" <<'PYTHON'
import json
import re
import subprocess
import sys

dashboard_path, promtool = sys.argv[1:]
with open(dashboard_path, encoding="utf-8") as stream:
    dashboard = json.load(stream)

panels = dashboard.get("panels", [])
panel_ids = [panel.get("id") for panel in panels]
if len(panels) != 11 or not all(isinstance(value, int) for value in panel_ids):
    raise SystemExit("dashboard must contain 11 panels with numeric IDs")
if len(panel_ids) != len(set(panel_ids)):
    raise SystemExit("dashboard panel IDs must be unique")
if dashboard.get("version", 0) < 2:
    raise SystemExit("dashboard version does not include completed-run queries")

variables = {
    variable.get("name"): variable
    for variable in dashboard.get("templating", {}).get("list", [])
}
run_id = variables.get("run_id", {})
if not run_id.get("includeAll") or run_id.get("allValue") != ".*":
    raise SystemExit("run_id must default to an all-runs regex")
if run_id.get("current", {}).get("value") != "$__all":
    raise SystemExit("run_id must select all runs by default")
metric_query = variables.get("metric", {}).get("query", {}).get("query", "")
metric = variables.get("metric", {})
if not metric.get("includeAll") or metric.get("allValue") != ".*":
    raise SystemExit("metric must default to an all-metrics regex")
if metric.get("current", {}).get("value") != "$__all":
    raise SystemExit("metric must select all metrics by default")
if 'cicd_pipeline_run_id=~"$run_id"' not in metric_query:
    raise SystemExit("metric discovery must use the run_id regex")

targets = [
    target
    for panel in panels
    for target in panel.get("targets", [])
    if target.get("expr")
]
if len(targets) != 10:
    raise SystemExit(f"dashboard PromQL target count = {len(targets)}, want 10")

for index, target in enumerate(targets):
    expression = target["expr"]
    if 'cicd_pipeline_run_id=~"$run_id"' not in expression:
        raise SystemExit(f"target {index} does not filter with the run_id regex")
    if index < 4:
        if not target.get("instant") or target.get("range") is not False:
            raise SystemExit(f"summary target {index} must be instant-only")
        if "$__range" not in expression:
            raise SystemExit(f"summary target {index} must search the dashboard range")
    else:
        if target.get("range") is not True or target.get("instant") is True:
            raise SystemExit(f"chart target {index} must be a range query")
        if "max_over_time" not in expression or "$__rate_interval" not in expression:
            raise SystemExit(f"chart target {index} can lose short completed runs")

    safe_expression = re.sub(
        r"\$\{metric:regex\}",
        "azure_blobfuse_.*",
        expression,
    )
    safe_expression = safe_expression.replace("$run_id", "123")
    safe_expression = safe_expression.replace("$__range", "30d")
    safe_expression = safe_expression.replace("$__rate_interval", "2h")
    result = subprocess.run(
        [promtool, "--experimental", "promql", "format", safe_expression],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise SystemExit(
            f"target {index} is invalid PromQL: {result.stderr.strip()}"
        )

print(f"Validated {len(targets)} completed-run dashboard queries")
PYTHON