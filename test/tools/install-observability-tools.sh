#!/usr/bin/env bash

set -euo pipefail

otelcol_version=0.159.0
otelcol_sha256=d56f84c3e7a67c3b8e4f4e25734ec5456be1271fab233a70486ebf1cf181a1e8
prometheus_version=3.14.0
prometheus_sha256=f665c6da19eb7ba399c915d30c7d9793c9b417bf8a749b504bc470678631478d

if (($# < 1 || $# > 2)); then
    printf 'usage: %s INSTALL_DIR [collector|all]\n' "$0" >&2
    exit 2
fi

install_dir=$1
selection=${2:-all}
if [[ "$selection" != "collector" && "$selection" != "all" ]]; then
    printf 'selection must be collector or all\n' >&2
    exit 2
fi

mkdir -p -- "$install_dir"
install_dir=$(cd -- "$install_dir" && pwd)
temp_dir=$(mktemp -d)
trap 'rm -rf -- "$temp_dir"' EXIT

download() {
    local url=$1
    local destination=$2

    curl \
        --fail \
        --location \
        --retry 3 \
        --retry-all-errors \
        --silent \
        --show-error \
        --output "$destination" \
        "$url"
}

otelcol_archive="otelcol_${otelcol_version}_linux_amd64.tar.gz"
download \
    "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${otelcol_version}/${otelcol_archive}" \
    "$temp_dir/$otelcol_archive"
printf '%s  %s\n' "$otelcol_sha256" "$temp_dir/$otelcol_archive" |
    sha256sum --check --strict
mkdir "$temp_dir/otelcol"
tar -xzf "$temp_dir/$otelcol_archive" -C "$temp_dir/otelcol"
install -m 0755 "$temp_dir/otelcol/otelcol" "$install_dir/otelcol"

if [[ "$selection" == "all" ]]; then
    prometheus_archive="prometheus-${prometheus_version}.linux-amd64.tar.gz"
    download \
        "https://github.com/prometheus/prometheus/releases/download/v${prometheus_version}/${prometheus_archive}" \
        "$temp_dir/$prometheus_archive"
    printf '%s  %s\n' "$prometheus_sha256" "$temp_dir/$prometheus_archive" |
        sha256sum --check --strict
    mkdir "$temp_dir/prometheus"
    tar -xzf "$temp_dir/$prometheus_archive" \
        --strip-components=1 \
        -C "$temp_dir/prometheus"
    install -m 0755 "$temp_dir/prometheus/prometheus" "$install_dir/prometheus"
    install -m 0755 "$temp_dir/prometheus/promtool" "$install_dir/promtool"
fi

"$install_dir/otelcol" --version
if [[ "$selection" == "all" ]]; then
    "$install_dir/prometheus" --version
    "$install_dir/promtool" --version
fi