#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "Usage: $0 <vMAJOR.MINOR.PATCH> <output>" >&2
  exit 2
fi

release="$1"
output="$2"
if [[ ! "${release}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "Invalid release tag: ${release}" >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source_installer="${script_dir}/install.sh"
output_dir="$(cd -- "$(dirname -- "${output}")" && pwd)"
output_path="${output_dir}/$(basename -- "${output}")"
if [ "${output_path}" = "${source_installer}" ]; then
  echo "Refusing to overwrite source installer: ${source_installer}" >&2
  exit 1
fi

staged_output="$(mktemp "${output_dir}/.install.sh.XXXXXX")"
trap 'rm -f "${staged_output}"' EXIT

awk -v release="${release}" '
  $0 == "DEFAULT_RELEASE=\"latest\"" {
    print "DEFAULT_RELEASE=\"" release "\""
    replacements++
    next
  }
  { print }
  END {
    if (replacements != 1) {
      print "expected exactly one DEFAULT_RELEASE assignment" > "/dev/stderr"
      exit 1
    }
  }
' "${source_installer}" > "${staged_output}"
chmod 0755 "${staged_output}"
mv -- "${staged_output}" "${output_path}"
trap - EXIT
