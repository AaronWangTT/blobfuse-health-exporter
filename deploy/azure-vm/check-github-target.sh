#!/usr/bin/env bash

set -euo pipefail

[[ $# -eq 2 ]] || {
    printf 'usage: %s GH_BIN REPOSITORY\n' "$0" >&2
    exit 2
}

gh_bin=$1
repository=$2
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
    printf '%s\n' 'GitHub telemetry secret already exists; refusing to deploy a competing endpoint' >&2
    exit 1
fi
printf '%s\n' 'GitHub telemetry target is available'