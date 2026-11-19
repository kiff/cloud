#!/usr/bin/env bash
# Refresh the RDS-managed master password in every service that connects
# as the master user (kiff-api) after Secrets Manager rotation. The gateway
# has its own role and password (deploy.sh), so it is not listed.
set -euo pipefail

: "${DB_SECRET_ARN:?DB_SECRET_ARN is required}"
: "${DB_HOST:?DB_HOST is required}"

AWS_REGION="${AWS_REGION:-eu-west-1}"
DB_NAME="${DB_NAME:-kiff}"
# Amazon's RDS CA bundle, installed by deploy.sh: verify-full checks the
# database's certificate and host name against it.
DB_CA="${DB_CA:-/etc/kiff/rds-ca.pem}"
[[ -r "$DB_CA" ]] || { printf '[db-credential-refresh] no %s\n' "$DB_CA"; exit 1; }
# "env-file:service" pairs. A pair whose env file does not exist is
# skipped, so the script works on a box that has no gateway yet.
DB_CLIENTS="${DB_CLIENTS:-/etc/kiff/api.env:kiff-api}"

log() { printf '[db-credential-refresh] %s\n' "$*"; }

secret_json="$(aws --region "$AWS_REGION" secretsmanager get-secret-value \
  --secret-id "$DB_SECRET_ARN" --version-stage AWSCURRENT \
  --query SecretString --output text)"

# Read through stdin so the password never appears in argv. A URI keeps the
# generated password safe across both systemd EnvironmentFile parsing and pgx.
encoded_password="$(printf '%s' "$secret_json" | jq -er \
  '.password | select(type == "string" and length > 0) | @uri')"
next_url="postgresql://kiff_admin:${encoded_password}@${DB_HOST}:5432/${DB_NAME}?sslmode=verify-full&sslrootcert=${DB_CA}"
next_line="KIFF_DATABASE_URL=${next_url}"

refresh() {
  local env_file="$1" service="$2" mode
  [[ -f "$env_file" ]] || { log "no $env_file; skipping $service"; return 0; }
  local current_line
  current_line="$(grep -m1 '^KIFF_DATABASE_URL=' "$env_file" || true)"
  if [[ -z "$current_line" ]]; then
    log "$env_file has no KIFF_DATABASE_URL"
    return 1
  fi
  [[ "$current_line" == "$next_line" ]] && return 0

  mode="$(stat -c '%a' "$env_file")"
  local tmp
  tmp="$(mktemp "${env_file}.XXXXXX")"
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == KIFF_DATABASE_URL=* ]]; then
      printf '%s\n' "$next_line"
    else
      printf '%s\n' "$line"
    fi
  done < "$env_file" > "$tmp"
  chmod "$mode" "$tmp"
  if [[ "$(id -u)" -eq 0 ]]; then
    chown root:root "$tmp"
  fi
  mv "$tmp" "$env_file"

  log "RDS credential changed; restarting $service"
  if [[ "$service" == kiff-api ]]; then
    # Swap the API like a deploy so requests keep flowing (scripts/swap-slot.sh).
    /opt/kiff/bin/swap-slot.sh api 8080 8090 kiff-api.service
    service="kiff-api@$(cat /etc/kiff/slot-api)"
  else
    systemctl restart "$service"
  fi
  systemctl is-active --quiet "$service"
}

status=0
for pair in $DB_CLIENTS; do
  refresh "${pair%%:*}" "${pair##*:}" || status=1
done
[[ $status -eq 0 ]] && log "credential refresh complete"
exit $status
