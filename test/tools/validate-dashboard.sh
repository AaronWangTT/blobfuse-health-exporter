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

def walk_panels(panels):
    for panel in panels:
        yield panel
        yield from walk_panels(panel.get("panels", []))


top_level_panels = dashboard.get("panels", [])
panels = list(walk_panels(top_level_panels))
panel_ids = [panel.get("id") for panel in panels]
if len(top_level_panels) != 11 or not all(
    isinstance(value, int) for value in panel_ids
):
    raise SystemExit("dashboard must contain 11 top-level panels with numeric IDs")
if len(panel_ids) != len(set(panel_ids)):
    raise SystemExit("dashboard panel IDs must be unique")
if dashboard.get("version", 0) < 4:
    raise SystemExit("dashboard version does not include completed-run queries")

variables = {
    variable.get("name"): variable
    for variable in dashboard.get("templating", {}).get("list", [])
}
run_id = variables.get("run_id", {})
if not run_id.get("includeAll") or run_id.get("allValue") != ".+":
    raise SystemExit("run_id must default to an all-runs regex")
if run_id.get("current", {}).get("value") != "$__all":
    raise SystemExit("run_id must select all runs by default")
metric_query = variables.get("metric", {}).get("query", {}).get("query", "")
metric = variables.get("metric", {})
if not metric.get("includeAll") or metric.get("allValue") != ".+":
    raise SystemExit("metric must default to an all-metrics regex")
if metric.get("current", {}).get("value") != "$__all":
    raise SystemExit("metric must select all metrics by default")
if 'cicd_pipeline_run_id=~"$run_id"' not in metric_query:
    raise SystemExit("metric discovery must use the run_id regex")

targets = [
    (panel, target)
    for panel in panels
    for target in panel.get("targets", [])
    if target.get("expr")
]
if len(targets) != 10:
    raise SystemExit(f"dashboard PromQL target count = {len(targets)}, want 10")

stat_count = 0
chart_count = 0
for index, (panel, target) in enumerate(targets):
    expression = target["expr"]
    if 'cicd_pipeline_run_id=~"$run_id"' not in expression:
        raise SystemExit(f"target {index} does not filter with the run_id regex")
    range_selectors = re.findall(r"\[[^]]+\]", expression)
    if panel.get("type") == "stat":
        stat_count += 1
        if not target.get("instant") or target.get("range") is not False:
            raise SystemExit(f"summary target {index} must be instant-only")
        if range_selectors != ["[${__range_s}s]"]:
            raise SystemExit(
                f"summary target {index} has invalid range selectors: "
                f"{range_selectors!r}"
            )
    elif panel.get("type") == "timeseries":
        chart_count += 1
        if target.get("range") is not True or target.get("instant") is True:
            raise SystemExit(f"chart target {index} must be a range query")
        if "max_over_time" not in expression or range_selectors != [
            "[${__interval_ms}ms]"
        ]:
            raise SystemExit(f"chart target {index} can lose short completed runs")
    else:
        raise SystemExit(
            f"target {index} uses unsupported panel type {panel.get('type')!r}"
        )

    safe_expression = re.sub(
        r"\$\{metric:regex\}",
        "azure_blobfuse_.*",
        expression,
    )
    safe_expression = safe_expression.replace("$run_id", "123")
    safe_expression = safe_expression.replace("${__range_s}", "2592000")
    safe_expression = safe_expression.replace("${__interval_ms}", "3600000")
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

if stat_count != 4 or chart_count != 6:
    raise SystemExit(
        f"dashboard target roles = {stat_count} stat/{chart_count} chart, want 4/6"
    )

print(f"Validated {len(targets)} completed-run dashboard queries")
PYTHON