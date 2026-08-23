#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

fail() {
    printf 'Existing Azure VM deployment failed: %s\n' "$1" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

[[ ${CONFIRM_EXISTING_VM_CHANGES:-} == yes ]] ||
    fail "set CONFIRM_EXISTING_VM_CHANGES=yes to acknowledge changes to the existing VM and its Caddy service"

for command_name in az base64 curl openssl python3 scp ssh ssh-keygen tar; do
    require_command "$command_name"
done
if [[ -n ${GH_BIN:-} ]]; then
    require_command "$GH_BIN"
    gh_bin=$GH_BIN
elif command -v gh >/dev/null 2>&1; then
    gh_bin=gh
elif command -v gh.exe >/dev/null 2>&1; then
    gh_bin=gh.exe
else
    fail "required command not found: gh or gh.exe"
fi

az account show --output none 2>/dev/null ||
    fail "Azure CLI is not authenticated; run 'az login' directly in your terminal"
"$gh_bin" auth status --hostname github.com >/dev/null 2>&1 ||
    fail "GitHub CLI is not authenticated; run 'gh auth login' directly in your terminal"

resource_group=${AZURE_RESOURCE_GROUP:-GW}
vm_name=${AZURE_VM_NAME:-gwsea}
gateway_dir=${GATEWAY_DIR:-/home/yuwang/az3166-gateway}
github_repository=${GITHUB_REPOSITORY:-AaronWangTT/blobfuse-health-exporter}
prometheus_retention=${PROMETHEUS_RETENTION:-30d}
otlp_username=${OTLP_USERNAME:-github-actions}
credentials_dir=${XDG_CONFIG_HOME:-$HOME/.config}/blobfuse-health-exporter
credentials_file="$credentials_dir/${vm_name}-observability"

[[ "$resource_group" =~ ^[-A-Za-z0-9._()]+$ ]] || fail "AZURE_RESOURCE_GROUP contains unsupported characters"
[[ "$vm_name" =~ ^[-A-Za-z0-9]+$ ]] || fail "AZURE_VM_NAME contains unsupported characters"
[[ "$gateway_dir" == /* && "$gateway_dir" != *$'\n'* ]] || fail "GATEWAY_DIR must be an absolute path without line breaks"
[[ "$otlp_username" =~ ^[A-Za-z0-9_-]+$ ]] || fail "OTLP_USERNAME contains unsupported characters"
[[ "$prometheus_retention" =~ ^[1-9][0-9]*[dhm]$ ]] || fail "PROMETHEUS_RETENTION must be a positive duration in d, h, or m"
[[ -f "$script_dir/check-github-target.sh" && -f "$script_dir/configure-github.sh" && \
    -f "$script_dir/install-existing-vm.sh" ]] ||
    fail "existing-VM deployment helpers are missing"
[[ ! -e "$credentials_file" ]] ||
    fail "credentials file already exists; initial deployment will not replace it"
install -d -m 0700 "$credentials_dir"
[[ -w "$credentials_dir" ]] || fail "credentials directory is not writable"
bash "$script_dir/check-github-target.sh" "$gh_bin" "$github_repository"

vm_details_json=$(az vm show \
    --resource-group "$resource_group" \
    --name "$vm_name" \
    --show-details \
    --query '{osType:storageProfile.osDisk.osType,powerState:powerState,adminUser:osProfile.adminUsername,fqdn:fqdns}' \
    --output json)
mapfile -t vm_details < <(python3 -c '
import json
import sys

details = json.load(sys.stdin)
for key in ("osType", "powerState", "adminUser", "fqdn"):
    value = details.get(key)
    print(value if isinstance(value, str) else "")
' <<<"$vm_details_json")
[[ ${#vm_details[@]} -eq 4 ]] || fail "Azure CLI returned incomplete VM details"
os_type=${vm_details[0]}
power_state=${vm_details[1]}
admin_user=${vm_details[2]}
fqdn=${vm_details[3]}
[[ "$os_type" == Linux ]] || fail "target VM must run Linux"
[[ "$power_state" == 'VM running' ]] || fail "target VM must be running"
[[ "$admin_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || fail "VM admin user is invalid"
[[ "$fqdn" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || fail "target VM has no usable public DNS name"

temporary_dir=$(mktemp -d)
key_marker="blobfuse-observability-$(openssl rand -hex 8)"
remote_key_added=false
remote_files_uploaded=false
remote_archive=
remote_secrets=
remote_installer=
credentials_file_written=false
remote_install_succeeded=false

remove_remote_files() {
    [[ "$remote_files_uploaded" == true ]] || return 0

    printf -v remote_cleanup 'rm -f %q %q %q' \
        "$remote_archive" "$remote_secrets" "$remote_installer"
    # Every remote argument above is shell-escaped with printf %q.
    # shellcheck disable=SC2029
    if ssh "${ssh_options[@]}" "$admin_user@$fqdn" "$remote_cleanup" >/dev/null 2>&1; then
        remote_files_uploaded=false
    fi
}

run_vm_script() {
    local encoded_script

    encoded_script=$(printf '%s' "$1" | base64 --wrap=0)
    az vm run-command invoke \
        --resource-group "$resource_group" \
        --name "$vm_name" \
        --command-id RunShellScript \
        --scripts "printf '%s' '$encoded_script' | base64 -d | bash" \
        --output none
}

remove_remote_key() {
    [[ "$remote_key_added" == true ]] || return 0
    local cleanup_script
    # Dollar expressions in this format string are evaluated on the VM.
    # shellcheck disable=SC2016
    printf -v cleanup_script \
        'set -e; home_dir=$(getent passwd %q | cut -d: -f6); file="$home_dir/.ssh/authorized_keys"; if [[ -f "$file" ]]; then grep -Fv -- %q "$file" >"$file.tmp" || true; chown --reference="$file" "$file.tmp"; chmod --reference="$file" "$file.tmp"; mv "$file.tmp" "$file"; fi' \
        "$admin_user" "$key_marker"
    run_vm_script "$cleanup_script" >/dev/null 2>&1 || true
    remote_key_added=false
}

cleanup() {
    local status=$?
    set +e
    remove_remote_files
    if ((status != 0)) && [[ "$credentials_file_written" == true ]] &&
        [[ "$remote_install_succeeded" != true ]]; then
        rm -f -- "$credentials_file"
    fi
    if ((status != 0)) && [[ "$remote_install_succeeded" == true ]]; then
        printf '%s\n' \
            'Remote installation completed, but a post-install check failed; the stack and local credentials were retained.' >&2
        printf 'Inspect /opt/blobfuse-observability and recover with credentials at %s; do not rerun initial deployment.\n' \
            "$credentials_file" >&2
    fi
    remove_remote_key
    rm -rf -- "$temporary_dir"
    unset authorization otlp_password grafana_admin_password grafana_secret_key
    exit "$status"
}
trap cleanup EXIT
umask 0077

ssh_key="$temporary_dir/deployment-key"
ssh-keygen -q -t ed25519 -N '' -C "$key_marker" -f "$ssh_key"
public_key_base64=$(base64 --wrap=0 <"$ssh_key.pub")
# Dollar expressions in this format string are evaluated on the VM.
# shellcheck disable=SC2016
printf -v authorize_script \
    'set -euo pipefail; user=%q; home_dir=$(getent passwd "$user" | cut -d: -f6); group=$(id -gn "$user"); install -d -m 0700 -o "$user" -g "$group" "$home_dir/.ssh"; touch "$home_dir/.ssh/authorized_keys"; chown "$user:$group" "$home_dir/.ssh/authorized_keys"; chmod 0600 "$home_dir/.ssh/authorized_keys"; key=$(printf %%s %q | base64 -d); grep -Fq -- %q "$home_dir/.ssh/authorized_keys" || printf "%%s\\n" "$key" >>"$home_dir/.ssh/authorized_keys"' \
    "$admin_user" "$public_key_base64" "$key_marker"
run_vm_script "$authorize_script"
remote_key_added=true
# Dollar expressions in this format string are evaluated on the VM.
# shellcheck disable=SC2016
printf -v verify_key_script \
    'set -euo pipefail; home_dir=$(getent passwd %q | cut -d: -f6); grep -Fq -- %q "$home_dir/.ssh/authorized_keys"' \
    "$admin_user" "$key_marker"
run_vm_script "$verify_key_script"

ssh_options=(
    -i "$ssh_key"
    -o BatchMode=yes
    -o ConnectTimeout=10
    -o IdentitiesOnly=yes
    -o StrictHostKeyChecking=accept-new
)
ssh "${ssh_options[@]}" "$admin_user@$fqdn" true ||
    fail "temporary SSH authorization did not become usable"

stack_archive="$temporary_dir/blobfuse-observability.tar.gz"
secrets_file="$temporary_dir/deployment-secrets"
otlp_password=$(openssl rand -hex 32)
grafana_admin_password=$(openssl rand -hex 24)
grafana_secret_key=$(openssl rand -hex 32)
otlp_url="https://$fqdn/blobfuse-otlp/v1/metrics"
dashboard_url="https://$fqdn/blobfuse-grafana/d/blobfuse-ci-runs/blobfuse-ci-run-metrics"
authorization="Basic $(printf '%s:%s' "$otlp_username" "$otlp_password" | base64 --wrap=0)"

credentials_file_written=true
{
    printf 'OTLP_METRICS_ENDPOINT=%q\n' "$otlp_url"
    printf 'OTLP_AUTHORIZATION=%q\n' "$authorization"
    printf 'GRAFANA_URL=%q\n' "$dashboard_url"
    printf 'GRAFANA_ADMIN_USER=admin\n'
    printf 'GRAFANA_ADMIN_PASSWORD=%q\n' "$grafana_admin_password"
} >"$credentials_file"
chmod 0600 "$credentials_file"

tar -C "$script_dir" -czf "$stack_archive" \
    compose-existing-vm.yaml \
    otel-collector.yaml \
    prometheus-otlp.yaml \
    grafana
{
    printf 'TELEMETRY_HOST=%q\n' "$fqdn"
    printf 'OTLP_USERNAME=%q\n' "$otlp_username"
    printf 'OTLP_PASSWORD=%q\n' "$otlp_password"
    printf 'GRAFANA_ADMIN_USER=%q\n' admin
    printf 'GRAFANA_ADMIN_PASSWORD=%q\n' "$grafana_admin_password"
    printf 'GRAFANA_SECRET_KEY=%q\n' "$grafana_secret_key"
    printf 'PROMETHEUS_RETENTION=%q\n' "$prometheus_retention"
} >"$secrets_file"
chmod 0600 "$secrets_file"

remote_archive="/home/$admin_user/blobfuse-observability.tar.gz"
remote_secrets="/home/$admin_user/blobfuse-observability-secrets"
remote_installer="/home/$admin_user/install-blobfuse-observability.sh"
remote_files_uploaded=true
scp "${ssh_options[@]}" "$stack_archive" "$admin_user@$fqdn:$remote_archive"
scp "${ssh_options[@]}" "$secrets_file" "$admin_user@$fqdn:$remote_secrets"
scp "${ssh_options[@]}" "$script_dir/install-existing-vm.sh" "$admin_user@$fqdn:$remote_installer"
printf -v remote_command \
    'chmod 0700 %q %q && sudo %q %q %q %q' \
    "$remote_installer" \
    "$remote_secrets" \
    "$remote_installer" \
    "$remote_archive" \
    "$remote_secrets" \
    "$gateway_dir"
# Every remote argument above is shell-escaped with printf %q.
# shellcheck disable=SC2029
ssh "${ssh_options[@]}" "$admin_user@$fqdn" "$remote_command"
remote_install_succeeded=true
remove_remote_files

printf '%s\n' 'Waiting for Grafana through the existing Caddy endpoint...'
grafana_ready=false
for _ in {1..60}; do
    if [[ $(curl --silent --output /dev/null --write-out '%{http_code}' \
        "https://$fqdn/blobfuse-grafana/api/health" || true) == 200 ]]; then
        grafana_ready=true
        break
    fi
    sleep 5
done
[[ "$grafana_ready" == true ]] || fail "Grafana did not become ready within five minutes"

unauthenticated_status=$(curl --silent --output /dev/null --write-out '%{http_code}' \
    --request POST \
    --header 'Content-Type: application/x-protobuf' \
    --data-binary '' \
    "$otlp_url")
[[ "$unauthenticated_status" == 401 ]] ||
    fail "OTLP endpoint returned $unauthenticated_status without authentication, expected 401"
authenticated_status=$(curl --silent --output /dev/null --write-out '%{http_code}' \
    --user "$otlp_username:$otlp_password" \
    --request POST \
    --header 'Content-Type: application/x-protobuf' \
    --data-binary '' \
    "$otlp_url")
[[ "$authenticated_status" == 200 ]] ||
    fail "OTLP endpoint returned $authenticated_status with authentication, expected 200"

printf '%s' "$authorization" |
    bash "$script_dir/configure-github.sh" \
        "$gh_bin" \
        "$github_repository" \
        "$otlp_url" \
        "$dashboard_url"

remove_remote_key
unset authorization otlp_password grafana_admin_password grafana_secret_key
printf 'OTLP endpoint configured for trusted GitHub Actions runs.\n'
printf 'Grafana dashboard: %s\n' "$dashboard_url"
printf 'Operator credentials saved with mode 0600 at %s\n' "$credentials_file"