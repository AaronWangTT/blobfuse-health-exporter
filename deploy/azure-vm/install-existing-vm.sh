#!/usr/bin/env bash

set -euo pipefail

[[ ${EUID:-$(id -u)} -eq 0 ]] || {
    printf '%s\n' 'install-existing-vm.sh must run as root' >&2
    exit 1
}
[[ $# -eq 3 ]] || {
    printf 'usage: %s STACK_ARCHIVE SECRETS_FILE GATEWAY_DIR\n' "$0" >&2
    exit 2
}

stack_archive=$1
secrets_file=$2
gateway_dir=$3
install_dir=/opt/blobfuse-observability
gateway_caddyfile="$gateway_dir/Caddyfile"
gateway_override="$gateway_dir/compose.override.yaml"
staging_dir=$(mktemp -d)
backup_suffix="blobfuse-backup-$(date -u +%Y%m%dT%H%M%SZ)"
caddy_backup="$gateway_caddyfile.$backup_suffix"
installed_stack=false
install_dir_changed=false
gateway_changed=false
network_connected=false
success=false

cleanup() {
    local status=$?
    set +e
    rm -rf -- "$staging_dir"
    rm -f -- "$secrets_file"
    unset OTLP_PASSWORD GRAFANA_ADMIN_PASSWORD GRAFANA_SECRET_KEY

    if [[ "$success" != true ]]; then
        if [[ "$gateway_changed" == true ]]; then
            cp --preserve=mode,ownership,timestamps -- "$caddy_backup" "$gateway_caddyfile"
            rm -f -- "$gateway_override"
            (cd "$gateway_dir" && docker compose up --detach caddy) >/dev/null 2>&1 || true
            caddy_container=$(cd "$gateway_dir" && docker compose ps --quiet caddy)
        fi
        if [[ "$network_connected" == true && -n ${caddy_container:-} ]]; then
            docker network disconnect blobfuse-observability-edge "$caddy_container" >/dev/null 2>&1 || true
        fi
        if [[ "$installed_stack" == true && -d "$install_dir" ]]; then
            (cd "$install_dir" && docker compose down --volumes) >/dev/null 2>&1 || true
        fi
        docker network rm blobfuse-observability-edge >/dev/null 2>&1 || true
        if [[ "$install_dir_changed" == true ]]; then
            rm -rf -- "$install_dir"
        fi
    fi

    exit "$status"
}
trap cleanup EXIT

umask 0077
# The provisioning script creates this root-consumed file from generated values.
# shellcheck disable=SC1090
source "$secrets_file"

required_values=(
    TELEMETRY_HOST
    OTLP_USERNAME
    OTLP_PASSWORD
    GRAFANA_ADMIN_USER
    GRAFANA_ADMIN_PASSWORD
    GRAFANA_SECRET_KEY
    PROMETHEUS_RETENTION
)
for variable_name in "${required_values[@]}"; do
    [[ -n "${!variable_name:-}" ]] || {
        printf 'required deployment value is empty: %s\n' "$variable_name" >&2
        exit 1
    }
done

[[ "$TELEMETRY_HOST" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || {
    printf '%s\n' 'TELEMETRY_HOST is not a valid lowercase DNS name' >&2
    exit 1
}
[[ "$OTLP_USERNAME" =~ ^[A-Za-z0-9_-]+$ ]] || {
    printf '%s\n' 'OTLP_USERNAME contains unsupported characters' >&2
    exit 1
}
[[ "$PROMETHEUS_RETENTION" =~ ^[1-9][0-9]*[dhm]$ ]] || {
    printf '%s\n' 'PROMETHEUS_RETENTION must be a positive duration in d, h, or m' >&2
    exit 1
}
[[ -d "$gateway_dir" && -f "$gateway_caddyfile" && -f "$gateway_dir/compose.yaml" ]] || {
    printf '%s\n' 'existing gateway directory is incomplete' >&2
    exit 1
}
for conflicting_override in compose.override.yml docker-compose.override.yml; do
    [[ ! -e "$gateway_dir/$conflicting_override" ]] || {
        printf 'unsupported existing gateway override: %s\n' "$gateway_dir/$conflicting_override" >&2
        exit 1
    }
done

caddy_container=$(cd "$gateway_dir" && docker compose ps --quiet caddy)
[[ -n "$caddy_container" && $(docker inspect --format '{{.State.Running}}' "$caddy_container") == true ]] || {
    printf '%s\n' 'existing Caddy service is not running' >&2
    exit 1
}

begin_count=$(grep -F -c '# BEGIN blobfuse-health-exporter' "$gateway_caddyfile" || true)
end_count=$(grep -F -c '# END blobfuse-health-exporter' "$gateway_caddyfile" || true)
if ((begin_count != 0 || end_count != 0)); then
    if ((begin_count == 1 && end_count == 1)) &&
        [[ -f "$gateway_override" ]] &&
        grep -F --quiet 'blobfuse-observability-edge' "$gateway_override"; then
        printf '%s\n' 'Blobfuse observability is already installed; refusing to replace the live deployment' >&2
    else
        printf '%s\n' 'gateway contains an incomplete Blobfuse observability configuration' >&2
    fi
    exit 1
fi
if [[ -e "$gateway_override" ]]; then
    printf '%s\n' 'an existing compose.override.yaml must be merged manually before installation' >&2
    exit 1
fi
if [[ -d "$install_dir" && -n $(find "$install_dir" -mindepth 1 -maxdepth 1 -print -quit) ]]; then
    printf '%s\n' 'observability install directory already exists without managed gateway routes' >&2
    exit 1
fi

python3 - "$stack_archive" <<'PYTHON'
import pathlib
import sys
import tarfile

archive_path = sys.argv[1]
seen = set()
with tarfile.open(archive_path, mode="r:gz") as archive:
    for member in archive.getmembers():
        path = pathlib.PurePosixPath(member.name)
        if not member.name or path.is_absolute() or ".." in path.parts:
            raise SystemExit(f"unsafe deployment archive path: {member.name!r}")
        if member.name in seen:
            raise SystemExit(f"duplicate deployment archive path: {member.name!r}")
        seen.add(member.name)
        if not (member.isfile() or member.isdir()):
            raise SystemExit(
                f"unsupported deployment archive member: {member.name!r}"
            )
PYTHON
tar --no-same-owner --no-same-permissions -xzf "$stack_archive" -C "$staging_dir"
for required_file in \
    compose-existing-vm.yaml \
    otel-collector.yaml \
    prometheus-otlp.yaml \
    grafana/provisioning/datasources/datasource.yaml \
    grafana/provisioning/dashboards/blobfuse.yaml \
    grafana/dashboards/blobfuse-ci-runs.json; do
    [[ -f "$staging_dir/$required_file" ]] || {
        printf 'deployment archive is missing %s\n' "$required_file" >&2
        exit 1
    }
done

otlp_password_hash=$(docker exec "$caddy_container" \
    caddy hash-password --plaintext "$OTLP_PASSWORD")
[[ ${otlp_password_hash:0:2} == "\$2" ]] || {
    printf '%s\n' 'Caddy did not return a bcrypt password hash' >&2
    exit 1
}

install_dir_changed=true
install -d -m 0700 "$install_dir"
find "$install_dir" -mindepth 1 -maxdepth 1 ! -name .env -exec rm -rf -- {} +
cp -a "$staging_dir/." "$install_dir/"
mv "$install_dir/compose-existing-vm.yaml" "$install_dir/compose.yaml"
chown -R root:root "$install_dir"
cat >"$install_dir/.env" <<EOF
TELEMETRY_HOST=$TELEMETRY_HOST
GRAFANA_ADMIN_USER=$GRAFANA_ADMIN_USER
GRAFANA_ADMIN_PASSWORD=$GRAFANA_ADMIN_PASSWORD
GRAFANA_SECRET_KEY=$GRAFANA_SECRET_KEY
PROMETHEUS_RETENTION=$PROMETHEUS_RETENTION
EOF
chmod 0600 "$install_dir/.env"

cd "$install_dir"
docker compose config --quiet
installed_stack=true
docker compose pull
docker compose up --detach --remove-orphans

for service_name in collector prometheus grafana; do
    service_container=$(docker compose ps --quiet "$service_name")
    [[ -n "$service_container" && $(docker inspect --format '{{.State.Running}}' "$service_container") == true ]] || {
        printf 'observability service did not start: %s\n' "$service_name" >&2
        exit 1
    }
done

cp --preserve=mode,ownership,timestamps -- "$gateway_caddyfile" "$caddy_backup"
gateway_changed=true

cat >"$gateway_override" <<'YAML'
services:
  caddy:
    networks:
      default:
      blobfuse-observability:

networks:
  blobfuse-observability:
    external: true
    name: blobfuse-observability-edge
YAML
chown --reference="$gateway_caddyfile" "$gateway_override"
chmod 0640 "$gateway_override"

export BLOBFUSE_OTLP_USERNAME="$OTLP_USERNAME"
export BLOBFUSE_OTLP_PASSWORD_HASH="$otlp_password_hash"
python3 - "$gateway_caddyfile" <<'PYTHON'
import os
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as stream:
    content = stream.read()

begin = "\t# BEGIN blobfuse-health-exporter\n"
end = "\t# END blobfuse-health-exporter\n"
if begin in content or end in content:
    raise SystemExit("Caddyfile already contains a Blobfuse route block")

closing = content.rfind("\n}")
if closing < 0 or content[closing + 2 :].strip():
    raise SystemExit("Caddyfile does not end in one site block")

username = os.environ["BLOBFUSE_OTLP_USERNAME"]
password_hash = os.environ["BLOBFUSE_OTLP_PASSWORD_HASH"]
snippet = (
    "\n"
    + begin
    + "\thandle /blobfuse-otlp/v1/metrics {\n"
    + "\t\tbasic_auth {\n"
    + f"\t\t\t{username} {password_hash}\n"
    + "\t\t}\n"
    + "\t\turi strip_prefix /blobfuse-otlp\n"
    + "\t\treverse_proxy blobfuse-collector:4318\n"
    + "\t}\n\n"
    + "\thandle /blobfuse-grafana {\n"
    + "\t\tredir /blobfuse-grafana/ 308\n"
    + "\t}\n\n"
    + "\thandle /blobfuse-grafana/* {\n"
    + "\t\treverse_proxy blobfuse-grafana:3000\n"
    + "\t}\n"
    + end
)
with open(path, "w", encoding="utf-8", newline="\n") as stream:
    stream.write(content[:closing] + snippet + content[closing:])
PYTHON
unset BLOBFUSE_OTLP_USERNAME BLOBFUSE_OTLP_PASSWORD_HASH otlp_password_hash
chown --reference="$caddy_backup" "$gateway_caddyfile"
chmod --reference="$caddy_backup" "$gateway_caddyfile"

(cd "$gateway_dir" && docker compose config --quiet)
docker exec "$caddy_container" caddy validate --config /etc/caddy/Caddyfile

if ! docker inspect --format '{{json .NetworkSettings.Networks}}' "$caddy_container" |
    grep -F --quiet 'blobfuse-observability-edge'; then
    docker network connect blobfuse-observability-edge "$caddy_container"
    network_connected=true
else
    network_connected=true
fi
grafana_ready=false
for _ in {1..60}; do
    if docker exec "$caddy_container" wget --quiet --output-document=/dev/null \
        http://blobfuse-grafana:3000/api/health 2>/dev/null; then
        grafana_ready=true
        break
    fi
    sleep 2
done
[[ "$grafana_ready" == true ]] || {
    printf '%s\n' 'Grafana did not become ready on the shared Docker network' >&2
    exit 1
}

(cd "$gateway_dir" && docker compose up --detach caddy)
caddy_container=$(cd "$gateway_dir" && docker compose ps --quiet caddy)
[[ -n "$caddy_container" && $(docker inspect --format '{{.State.Running}}' "$caddy_container") == true ]] || {
    printf '%s\n' 'Caddy did not restart after adding the observability routes' >&2
    exit 1
}

success=true
printf '%s\n' 'Existing VM observability stack installed'
printf 'Caddyfile backup: %s\n' "$caddy_backup"