#!/usr/bin/env bash

set -euo pipefail

[[ $# -eq 4 ]] || {
    printf 'usage: %s GH_BIN REPOSITORY OTLP_ENDPOINT DASHBOARD_URL\n' "$0" >&2
    exit 2
}

gh_bin=$1
repository=$2
otlp_endpoint=$3
dashboard_url=$4
authorization=$(cat)

[[ -n "$authorization" && "$authorization" != *$'\n'* && "$authorization" != *$'\r'* ]] || {
    printf '%s\n' 'GitHub OTLP authorization is empty or contains line breaks' >&2
    exit 1
}

endpoint_name=BLOBFUSE_OTLP_METRICS_ENDPOINT
dashboard_name=BLOBFUSE_GRAFANA_DASHBOARD_URL
previous_endpoint=
previous_dashboard=
actual_endpoint=
actual_dashboard=
endpoint_existed=false
dashboard_existed=false
variables_changed=false

if previous_endpoint=$("$gh_bin" variable get "$endpoint_name" --repo "$repository" 2>/dev/null); then
    previous_endpoint=${previous_endpoint//$'\r'/}
    endpoint_existed=true
fi
if previous_dashboard=$("$gh_bin" variable get "$dashboard_name" --repo "$repository" 2>/dev/null); then
    previous_dashboard=${previous_dashboard//$'\r'/}
    dashboard_existed=true
fi
secret_state=$("$gh_bin" secret list \
    --repo "$repository" \
    --json name \
    --jq 'any(.[]; .name == "BLOBFUSE_OTLP_AUTHORIZATION")')
secret_state=${secret_state//$'\r'/}
[[ "$secret_state" == true || "$secret_state" == false ]] || {
    printf '%s\n' 'GitHub secret inventory returned an invalid state' >&2
    exit 1
}
if [[ "$secret_state" == true ]]; then
    printf '%s\n' 'GitHub telemetry secret already exists; refusing an implicit credential rotation' >&2
    exit 1
fi

restore_variable() {
    local name=$1
    local existed=$2
    local previous_value=$3
    local actual_value
    local inventory

    if [[ "$existed" == true ]]; then
        "$gh_bin" variable set "$name" \
            --body "$previous_value" \
            --repo "$repository" >/dev/null || return 1
        actual_value=$("$gh_bin" variable get "$name" --repo "$repository") || return 1
        actual_value=${actual_value//$'\r'/}
        [[ "$actual_value" == "$previous_value" ]]
    else
        "$gh_bin" variable delete "$name" --repo "$repository" >/dev/null 2>&1 || return 1
        inventory=$("$gh_bin" variable list \
            --repo "$repository" \
            --json name \
            --jq "any(.[]; .name == \"$name\")") || return 1
        inventory=${inventory//$'\r'/}
        [[ "$inventory" == false ]]
    fi
}

cleanup() {
    local status=$?
    local rollback_failed=false
    set +e

    if ((status != 0)) && [[ "$variables_changed" == true ]]; then
        restore_variable "$endpoint_name" "$endpoint_existed" "$previous_endpoint" ||
            rollback_failed=true
        restore_variable "$dashboard_name" "$dashboard_existed" "$previous_dashboard" ||
            rollback_failed=true
        if [[ "$rollback_failed" == true ]]; then
            printf '%s\n' \
                'GitHub variable rollback could not be verified; manual recovery is required' >&2
            status=2
        else
            printf '%s\n' 'Prior GitHub telemetry variables were restored' >&2
        fi
    fi
    unset authorization previous_endpoint previous_dashboard actual_endpoint actual_dashboard
    exit "$status"
}
trap cleanup EXIT

variables_changed=true
"$gh_bin" variable set "$endpoint_name" \
    --body "$otlp_endpoint" \
    --repo "$repository" >/dev/null
"$gh_bin" variable set "$dashboard_name" \
    --body "$dashboard_url" \
    --repo "$repository" >/dev/null

actual_endpoint=$("$gh_bin" variable get "$endpoint_name" --repo "$repository")
actual_endpoint=${actual_endpoint//$'\r'/}
[[ "$actual_endpoint" == "$otlp_endpoint" ]] || {
    printf '%s\n' 'GitHub OTLP endpoint verification failed' >&2
    exit 1
}
actual_dashboard=$("$gh_bin" variable get "$dashboard_name" --repo "$repository")
actual_dashboard=${actual_dashboard//$'\r'/}
[[ "$actual_dashboard" == "$dashboard_url" ]] || {
    printf '%s\n' 'GitHub dashboard URL verification failed' >&2
    exit 1
}

if ! printf '%s' "$authorization" |
    "$gh_bin" secret set BLOBFUSE_OTLP_AUTHORIZATION --repo "$repository" >/dev/null; then
    secret_state=$("$gh_bin" secret list \
        --repo "$repository" \
        --json name \
        --jq 'any(.[]; .name == "BLOBFUSE_OTLP_AUTHORIZATION")' 2>/dev/null || true)
    secret_state=${secret_state//$'\r'/}
    if [[ "$secret_state" == true ]]; then
        variables_changed=false
        printf '%s\n' 'GitHub secret creation was confirmed after an ambiguous CLI failure' >&2
        exit 0
    fi
    printf '%s\n' 'GitHub telemetry secret update failed; rolling back endpoint variables' >&2
    exit 1
fi
variables_changed=false
printf '%s\n' 'GitHub telemetry settings configured'