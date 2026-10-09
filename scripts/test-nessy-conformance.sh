#!/usr/bin/env bash
set -eu

agent_cli_name="nessy"
timeout_seconds=900

write_failure_result() {
  failure_class=$1
  printf '{"schema_version":1,"selected":false,"passed":false,"provider":"nessy","assertions":{},"tool_inventory":{"requested_exact":[],"behavior_observed":[],"preflight_present":false},"forbidden_capabilities":{"outcomes":{},"acp_tool_events":0,"canaries_found":0},"native_read":{"outcome":"not_observed","os_isolation_guaranteed":false},"failure_class":"%s"}\n' "$failure_class"
}

while (($# > 0)); do
  case "$1" in
    --timeout-seconds)
      if (($# < 2)); then
        write_failure_result invalid_timeout
        exit 2
      fi
      timeout_seconds=$2
      shift 2
      ;;
    *)
      write_failure_result unknown_argument
      exit 2
      ;;
  esac
done

case "$timeout_seconds" in
  ''|*[!0-9]*)
    write_failure_result invalid_timeout
    exit 2
    ;;
esac
if ((timeout_seconds < 60 || timeout_seconds > 3600)); then
  write_failure_result invalid_timeout
  exit 2
fi
if [[ $(uname -s) != Darwin || $(uname -m) != arm64 ]]; then
  write_failure_result unsupported_host
  exit 2
fi
if ! command -v -- "$agent_cli_name" >/dev/null 2>&1; then
  write_failure_result executable_not_found
  exit 2
fi
if ! command -v go >/dev/null 2>&1; then
  write_failure_result go_not_found
  exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
run_root=$(mktemp -d "${TMPDIR:-/tmp}/stepan-nessy-conformance.XXXXXXXX")
result_path=$run_root/result.json
test_log=$run_root/go-test.log

cleanup() {
  case "$run_root" in
    "${TMPDIR:-/tmp}"/stepan-nessy-conformance.*) rm -rf -- "$run_root" ;;
    *) printf '%s\n' '{"schema_version":1,"selected":true,"passed":false,"provider":"nessy","tool_inventory":{"requested_exact":[],"behavior_observed":[],"preflight_present":false},"forbidden_capabilities":{"outcomes":{},"acp_tool_events":0,"canaries_found":0},"failure_class":"unsafe_cleanup_target"}' >&2 ;;
  esac
}
trap cleanup EXIT HUP INT TERM

cd -- "$repo_root" || exit 2
export STEPAN_NESSY_REAL_CLI=1
export STEPAN_NESSY_RESULT=$result_path

set +e
go test -tags nessy_real_cli -run '^TestNessyRealCLIConformance$' \
  ./internal/agentruntime/nessyapp -count=1 -timeout "${timeout_seconds}s" >"$test_log" 2>&1
test_exit=$?
set -e

if [[ -f $result_path ]]; then
  cat -- "$result_path"
else
  printf '{"schema_version":1,"selected":true,"passed":false,"provider":"nessy","os":"darwin","arch":"arm64","tool_inventory":{"requested_exact":[],"behavior_observed":[],"preflight_present":false},"forbidden_capabilities":{"outcomes":{},"acp_tool_events":0,"canaries_found":0},"failure_class":"test_harness_failed","test_exit_code":%d}\n' \
    "$test_exit"
fi
exit "$test_exit"
