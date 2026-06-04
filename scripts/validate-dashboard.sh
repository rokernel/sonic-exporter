#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/validate-dashboard.sh <dashboard-json-path>

Validate a Grafana dashboard JSON file for lightweight CI checks.

Checks performed:
- JSON parses with jq
- title is "SONiC Exporter"
- required template variables: datasource, job, instance
- datasource UIDs use variable references
- non-empty panels array
- no obvious private IPv4 or internal-domain target values
USAGE
  exit 2
}

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

if [[ $# -ne 1 ]]; then
  usage
fi

dash_file=$1

if ! command -v jq >/dev/null 2>&1; then
  fail "jq is required to validate dashboard JSON. Install jq and retry."
fi

if [[ ! -f "$dash_file" ]]; then
  fail "dashboard file not found: $dash_file"
fi

if ! jq empty "$dash_file" >/dev/null 2>&1; then
  fail "invalid JSON in dashboard file: $dash_file"
fi

title=$(jq -r '.title // empty' "$dash_file")
if [[ "$title" != "SONiC Exporter" ]]; then
  fail "unexpected dashboard title: '$title' (expected: 'SONiC Exporter')"
fi

for required_var in datasource job instance; do
  if ! jq -e --arg var "$required_var" '.templating.list // [] | any(.name == $var)' "$dash_file" >/dev/null; then
    fail "missing required template variable: $required_var"
  fi
done

invalid_uids=$(jq -r '.. | objects | .datasource? | objects | .uid? // empty' "$dash_file")
if [[ -n "$invalid_uids" ]]; then
  while IFS= read -r uid; do
    [[ -z "$uid" ]] && continue
    if [[ "$uid" != '$datasource' && "$uid" != '${datasource}' ]]; then
      fail "non-portable datasource UID found: '$uid'"
    fi
  done <<< "$invalid_uids"
fi

if ! jq -e '.panels | (type == "array" and length > 0)' "$dash_file" >/dev/null; then
  fail "dashboard panels must be a non-empty array"
fi

panel_strings=$(jq -r '.. | strings' "$dash_file")
private_ipv4_pattern='(^|[^0-9])(10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[0-1])\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3})([^0-9]|$)'
private_domain_pattern='(^|[^A-Za-z0-9_-])(localhost\b|[A-Za-z0-9_-]+\.(local|internal|corp|lan|example)\b)([^A-Za-z0-9_-]|$)'

private_hits="$(printf '%s\n' "$panel_strings" | grep -E "$private_ipv4_pattern" || true)"
private_hits+="$(printf '%s\n' "$panel_strings" | grep -E "$private_domain_pattern" || true)"

if [[ -n "$private_hits" ]]; then
  fail "private target values detected:$'\n'$private_hits"
fi

echo "Dashboard validation passed: $dash_file"
