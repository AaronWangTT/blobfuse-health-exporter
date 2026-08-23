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
if len(top_level_panels) != 21 or not all(
    isinstance(value, int) for value in panel_ids
):
    raise SystemExit("dashboard must contain 21 top-level panels with numeric IDs")
if len(panel_ids) != len(set(panel_ids)):
    raise SystemExit("dashboard panel IDs must be unique")
if dashboard.get("version", 0) < 10:
    raise SystemExit("dashboard version does not include metric-centric queries")
if dashboard.get("title") != "Blobfuse CI Telemetry":
    raise SystemExit("dashboard title must not make CI run the primary dimension")

for panel in panels:
    position = panel.get("gridPos", {})
    if not all(isinstance(position.get(key), int) for key in ("x", "y", "w", "h")):
        raise SystemExit(f"panel {panel.get('title')!r} must have an integer grid position")
    if (
        position["x"] < 0
        or position["y"] < 0
        or position["w"] <= 0
        or position["h"] <= 0
        or position["x"] + position["w"] > 24
    ):
        raise SystemExit(f"panel {panel.get('title')!r} has an invalid grid position")

for index, first in enumerate(top_level_panels):
    first_position = first["gridPos"]
    for second in top_level_panels[index + 1 :]:
        second_position = second["gridPos"]
        horizontal_overlap = max(first_position["x"], second_position["x"]) < min(
            first_position["x"] + first_position["w"],
            second_position["x"] + second_position["w"],
        )
        vertical_overlap = max(first_position["y"], second_position["y"]) < min(
            first_position["y"] + first_position["h"],
            second_position["y"] + second_position["h"],
        )
        if horizontal_overlap and vertical_overlap:
            raise SystemExit(
                f"panels {first.get('title')!r} and {second.get('title')!r} overlap"
            )

variables = {
    variable.get("name"): variable
    for variable in dashboard.get("templating", {}).get("list", [])
}
run_id = variables.get("run_id", {})
if not run_id.get("includeAll") or run_id.get("allValue") != ".+":
    raise SystemExit("run_id must default to an all-runs regex")
if run_id.get("current", {}).get("value") != "$__all":
    raise SystemExit("run_id must select all runs by default")
if run_id.get("label") != "Run filter (optional)":
    raise SystemExit("run_id must be presented as an optional drill-down filter")
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
if len(targets) != 18:
    raise SystemExit(f"dashboard PromQL target count = {len(targets)}, want 18")

panels_by_title = {panel.get("title"): panel for panel in panels}
expected_metric_panels = {
    "Cache disk usage": (
        "azure_blobfuse_cache_usage_bytes", "bytes", "max(max_over_time(", (),
    ),
    "Cache utilization": (
        "azure_blobfuse_cache_utilization_ratio", "percentunit", "max(max_over_time(", (),
    ),
    "Open file handles": (
        "azure_blobfuse_file_open",
        "short",
        "max by (azure_blobfuse_component_name) (max_over_time(",
        ("{{azure_blobfuse_component_name}}",),
    ),
    "Cache file downloads": (
        "azure_blobfuse_cache_file_downloads_total", "short", "sum(max_over_time(", (),
    ),
    "Cache hits": (
        "azure_blobfuse_cache_hits_total", "short", "sum(max_over_time(", (),
    ),
    "Source records processed": (
        "blobfuse_health_exporter_source_records_total",
        "short",
        "sum by (outcome) (max_over_time(",
        ("{{outcome}}",),
    ),
    "Source rotations": (
        "blobfuse_health_exporter_source_rotations_total", "short", "sum(max_over_time(", (),
    ),
    "Source discontinuities": (
        "blobfuse_health_exporter_source_discontinuities_total",
        "short",
        "sum by (reason) (max_over_time(",
        ("{{reason}}",),
    ),
    "Source counter resets": (
        "blobfuse_health_exporter_source_counter_resets_total",
        "short",
        "sum by (source_metric) (max_over_time(",
        ("{{source_metric}}",),
    ),
    "Metric export errors": (
        "blobfuse_health_exporter_export_errors_total",
        "short",
        "sum by (error_type) (max_over_time(",
        ("{{error_type}}",),
    ),
}
for title, (metric_name, unit, aggregation, legend_labels) in expected_metric_panels.items():
    panel = panels_by_title.get(title, {})
    panel_targets = [target for target in panel.get("targets", []) if target.get("expr")]
    if panel.get("type") != "timeseries" or len(panel_targets) != 1:
        raise SystemExit(f"{title} must be a single-query time series panel")
    expression = panel_targets[0]["expr"]
    if expression.count(f'__name__="{metric_name}"') != 2:
        raise SystemExit(f"{title} must select only {metric_name}")
    if expression.count(aggregation) != 2:
        raise SystemExit(f"{title} must aggregate away CI run identity")
    if panel.get("fieldConfig", {}).get("defaults", {}).get("unit") != unit:
        raise SystemExit(f"{title} must use Grafana unit {unit}")
    legend = panel_targets[0].get("legendFormat", "")
    if any(label not in legend for label in legend_labels):
        raise SystemExit(f"{title} has an incomplete legend")
    if any(label in legend for label in ("{{cicd_pipeline_run_id}}", "{{github_run_attempt}}")):
        raise SystemExit(f"{title} must not expose CI identity in its legend")

expected_overview_panels = {
    "Filesystem operations": (
        "sum by (azure_blobfuse_operation_name) (max_over_time(",
        "{{azure_blobfuse_operation_name}}",
    ),
    "Storage I/O": (
        "sum by (azure_blobfuse_io_direction) (max_over_time(",
        "{{azure_blobfuse_io_direction}}",
    ),
    "Blobfuse virtual memory": (
        "max by (service_name) (max_over_time(",
        "{{service_name}}",
    ),
}
for title, (aggregation, legend_label) in expected_overview_panels.items():
    panel = panels_by_title.get(title, {})
    panel_targets = [target for target in panel.get("targets", []) if target.get("expr")]
    if panel.get("type") != "timeseries" or len(panel_targets) != 1:
        raise SystemExit(f"{title} must be a single-query time series panel")
    if panel_targets[0]["expr"].count(aggregation) != 2:
        raise SystemExit(f"{title} must aggregate away CI run identity")
    legend = panel_targets[0].get("legendFormat", "")
    if legend_label not in legend or any(
        label in legend
        for label in ("{{cicd_pipeline_run_id}}", "{{github_run_attempt}}")
    ):
        raise SystemExit(f"{title} must use only metric-semantic legend labels")

raw_explorer = panels_by_title.get("$metric", {})
raw_targets = [target for target in raw_explorer.get("targets", []) if target.get("expr")]
if len(raw_targets) != 1 or "{{cicd_pipeline_run_id}}" not in raw_targets[0].get(
    "legendFormat", ""
):
    raise SystemExit("raw metric explorer must retain CI run identity for drill-down")

for row_title in ("Blobfuse cache", "Exporter health", "All metric families"):
    if panels_by_title.get(row_title, {}).get("type") != "row":
        raise SystemExit(f"{row_title} must be a dashboard row")

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
        if range_selectors not in (["[${__range_s}s]"], ["[${__range_s}s:]"]):
            raise SystemExit(
                f"summary target {index} has invalid range selectors: "
                f"{range_selectors!r}"
            )
    elif panel.get("type") == "timeseries":
        chart_count += 1
        if target.get("range") is not True or target.get("instant") is True:
            raise SystemExit(f"chart target {index} must be a range query")
        if panel.get("maxDataPoints") != 11000:
            raise SystemExit(
                f"chart target {index} must honor Prometheus range resolution"
            )
        if "offset" in expression:
            raise SystemExit(f"chart target {index} must not read past range ends")
        expected_range = (
            "[2 * ${__interval_ms}ms:]"
            if panel.get("title") == "$metric"
            else "[2 * ${__interval_ms}ms]"
        )
        if "max_over_time" not in expression or range_selectors != [
            expected_range,
            expected_range,
        ]:
            raise SystemExit(f"chart target {index} can lose short completed runs")
        if (
            expression.count(" or ") != 1
            or expression.count("@ ${__to:date:seconds}") != 1
            or expression.count("and on ()") != 1
            or expression.count(
                "vector(time()) > vector(${__to:date:seconds} - ${__interval_ms} / 1000)"
            )
            != 1
        ):
            raise SystemExit(f"chart target {index} must cover the exact range end")
    else:
        raise SystemExit(
            f"target {index} uses unsupported panel type {panel.get('type')!r}"
        )

    broad_panel = panel.get("title") in {
        "Selected metric series",
        "Blobfuse metric series",
        "$metric",
    }
    metric_name_copy = (
        'label_replace(', '"metric_name", "$1", "__name__", "(.*)"'
    )
    expected_copies = 2 if panel.get("type") == "timeseries" else 1
    if broad_panel and (
        expression.count(metric_name_copy[0]) != expected_copies
        or expression.count(metric_name_copy[1]) != expected_copies
    ):
        raise SystemExit(f"target {index} must preserve metric names")
    if broad_panel and panel.get("type") == "timeseries" and (
        "{{metric_name}}" not in target.get("legendFormat", "")
    ):
        raise SystemExit(f"chart target {index} must display preserved metric names")

    safe_expression = re.sub(
        r"\$\{metric:regex\}",
        "azure_blobfuse_.*",
        expression,
    )
    safe_expression = safe_expression.replace("$run_id", "123")
    safe_expression = safe_expression.replace("${__range_s}", "2592000")
    safe_expression = safe_expression.replace("${__interval_ms}", "3600000")
    safe_expression = safe_expression.replace("${__to:date:seconds}", "1700000000")
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

if stat_count != 4 or chart_count != 14:
    raise SystemExit(
        f"dashboard target roles = {stat_count} stat/{chart_count} chart, want 4/14"
    )

print(f"Validated {len(targets)} metric-centric dashboard queries")
PYTHON