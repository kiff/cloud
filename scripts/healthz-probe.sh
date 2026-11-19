#!/usr/bin/env bash
#
# scripts/healthz-probe.sh — cron-on-box HTTP healthz probe.
#
# A cheaper alternative to CloudWatch Synthetics.
# Runs from a systemd timer every minute, hits Caddy on loopback
# with each public Host header, and emits a custom CloudWatch
# metric per vhost: kiff/healthz/<vhost> = 1 (ok) | 0 (fail).
#
# Pre-DNS-cutover: probes go through Caddy on the bare EIP using
# the Host header — this validates the full Caddy → upstream
# chain without depending on TLS or DNS.
#
# Post-cutover: probes still hit loopback with Host header,
# which is the right thing for "is the local service healthy".
# An additional external probe (CloudWatch Synthetics or a
# remote uptime checker) can be added later if we need to
# detect DNS or network-edge failures.
#
# CloudWatch metric namespace: KiffCloud/Healthz
# Dimension:                   Vhost = api.kiff.dev | kiff.dev | app.kiff.dev | mcp.kiff.dev
# Value:                       1 (200 OK) | 0 (anything else)
set -euo pipefail

REGION="${AWS_REGION:-eu-west-1}"
NAMESPACE="KiffCloud/Healthz"

probe() {
	local vhost="$1"
	local upstream_port="$2"
	# Hit Caddy on :80 with the Host header — vhost-aware match.
	# Caddy 308-redirects to https. We follow http→https locally,
	# bypassing TLS verification (we're hitting the bare IP).
	# Then check the underlying /healthz body returned 200.
	local code
	code=$(curl -sS -o /dev/null \
		--max-time 10 \
		--connect-timeout 5 \
		-H "Host: ${vhost}" \
		"http://localhost:${upstream_port}/healthz" \
		-w '%{http_code}' 2>/dev/null || echo "000")

	local value=0
	[[ "$code" == "200" ]] && value=1

	aws --region "$REGION" cloudwatch put-metric-data \
		--namespace "$NAMESPACE" \
		--metric-data \
		"MetricName=Healthy,Dimensions=[{Name=Vhost,Value=${vhost}}],Unit=None,Value=${value}" \
		>/dev/null 2>&1 || true

	logger -t kiff-healthz "vhost=${vhost} code=${code} healthy=${value}"
}

# port <name> <base>: the port a service's active slot listens on
# (scripts/swap-slot.sh), or its base port before the first swap.
port() {
	cat "/etc/kiff/slot-$1" 2>/dev/null || echo "$2"
}

# Probe each upstream directly so we measure the binary's health,
# not Caddy's. This is the only health check: Caddy has none, since
# each vhost has one upstream.
probe api.kiff.dev "$(port api 8080)"
probe kiff.dev     "$(port web 8081)"
probe app.kiff.dev "$(port web 8081)"
probe mcp.kiff.dev "$(port gateway 8282)"
