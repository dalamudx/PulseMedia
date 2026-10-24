#!/usr/bin/env bash
set -euo pipefail
exec env PULSE_M9_RATE_BROWSER=1 PULSE_M9_HOME_INBOX_BROWSER=1 PULSE_M9_RATE_REVIEW=1 bash "$(dirname -- "$0")/verify_m9_global_rates_ui.sh"
