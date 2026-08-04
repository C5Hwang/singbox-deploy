#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT

fake_bin="${test_dir}/bin"
mkdir -p "${fake_bin}"

cat > "${fake_bin}/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -s) printf '%s\n' Linux ;;
  -m) printf '%s\n' x86_64 ;;
  *) exit 1 ;;
esac
EOF

cat > "${fake_bin}/id" <<'EOF'
#!/usr/bin/env bash
[ "${1:-}" = "-u" ] || exit 1
printf '%s\n' 0
EOF

cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
url="$2"
dest="$4"
printf '%s\n' "${url}" >> "${INSTALL_TEST_LOG}"
case "${url}" in
  */SHA256SUMS)
    digest="$(cat "${INSTALL_TEST_DIGEST_FILE}")"
    if [ "${FAKE_BAD_CHECKSUM:-}" = "1" ]; then
      digest='ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
    fi
    printf '%s  %s\n' "${digest}" singbox-deploy-linux-amd64 > "${dest}"
    ;;
  *)
    {
      printf '%s\n' '#!/usr/bin/env bash'
      printf '%s\n' 'printf '\''%s\n'\'' "${FAKE_CANDIDATE_VERSION}"'
    } > "${dest}"
    sha256sum "${dest}" | awk '{print $1}' > "${INSTALL_TEST_DIGEST_FILE}"
    ;;
esac
EOF

cat > "${fake_bin}/install" <<'EOF'
#!/usr/bin/env bash
[ "$1" = "-m" ] && [ "$2" = "0755" ] && [ -x "$3" ] && \
  [ "$4" = "/usr/bin/singbox-deploy" ] || exit 1
printf '%s %s\n' "$2" "$4" >> "${INSTALL_TEST_INSTALL_LOG}"
exit 0
EOF

chmod 0755 "${fake_bin}"/*

run_installer() {
  local installer="$1"
  local log="$2"
  local candidate_version="$3"
  local install_log="${log}.install"
  shift 3
  : > "${log}"
  : > "${install_log}"
  env PATH="${fake_bin}:${PATH}" \
    INSTALL_TEST_LOG="${log}" \
    INSTALL_TEST_INSTALL_LOG="${install_log}" \
    INSTALL_TEST_DIGEST_FILE="${log}.digest" \
    FAKE_CANDIDATE_VERSION="${candidate_version}" \
    "$@" bash "${installer}" >/dev/null
  grep -Fxq '0755 /usr/bin/singbox-deploy' "${install_log}"
}

latest_log="${test_dir}/latest.log"
run_installer "${script_dir}/install.sh" "${latest_log}" v9.9.9
diff -u <(printf '%s\n' \
  'https://github.com/C5Hwang/singbox-deploy/releases/latest/download/singbox-deploy-linux-amd64' \
  'https://github.com/C5Hwang/singbox-deploy/releases/latest/download/SHA256SUMS') "${latest_log}"

pinned_installer="${test_dir}/install-v1.2.3.sh"
"${script_dir}/package-installer.sh" v1.2.3 "${pinned_installer}"
grep -Fxq 'DEFAULT_RELEASE="v1.2.3"' "${pinned_installer}"

pinned_log="${test_dir}/pinned.log"
run_installer "${pinned_installer}" "${pinned_log}" v1.2.3
diff -u <(printf '%s\n' \
  'https://github.com/C5Hwang/singbox-deploy/releases/download/v1.2.3/singbox-deploy-linux-amd64' \
  'https://github.com/C5Hwang/singbox-deploy/releases/download/v1.2.3/SHA256SUMS') "${pinned_log}"

override_log="${test_dir}/override.log"
run_installer "${pinned_installer}" "${override_log}" v2.3.4 SINGBOX_DEPLOY_VERSION=v2.3.4
diff -u <(printf '%s\n' \
  'https://github.com/C5Hwang/singbox-deploy/releases/download/v2.3.4/singbox-deploy-linux-amd64' \
  'https://github.com/C5Hwang/singbox-deploy/releases/download/v2.3.4/SHA256SUMS') "${override_log}"

mismatch_log="${test_dir}/mismatch.log"
: > "${mismatch_log}"
: > "${mismatch_log}.install"
if env PATH="${fake_bin}:${PATH}" \
  INSTALL_TEST_LOG="${mismatch_log}" \
  INSTALL_TEST_INSTALL_LOG="${mismatch_log}.install" \
  INSTALL_TEST_DIGEST_FILE="${mismatch_log}.digest" \
  FAKE_CANDIDATE_VERSION=v1.2.4 \
  bash "${pinned_installer}" >/dev/null 2>&1; then
  echo "wrong-version release asset unexpectedly installed" >&2
  exit 1
fi

checksum_log="${test_dir}/checksum.log"
: > "${checksum_log}"
: > "${checksum_log}.install"
if env PATH="${fake_bin}:${PATH}" \
  INSTALL_TEST_LOG="${checksum_log}" \
  INSTALL_TEST_INSTALL_LOG="${checksum_log}.install" \
  INSTALL_TEST_DIGEST_FILE="${checksum_log}.digest" \
  FAKE_CANDIDATE_VERSION=v1.2.3 \
  FAKE_BAD_CHECKSUM=1 \
  bash "${pinned_installer}" >/dev/null 2>&1; then
  echo "checksum mismatch unexpectedly installed" >&2
  exit 1
fi
if [ -s "${checksum_log}.install" ]; then
  echo "checksum mismatch reached install" >&2
  exit 1
fi
if [ -s "${mismatch_log}.install" ]; then
  echo "wrong-version release asset reached install" >&2
  exit 1
fi

for invalid_version in '' latest '../../invalid'; do
  invalid_log="${test_dir}/invalid-${invalid_version//\//_}.log"
  : > "${invalid_log}"
  if env PATH="${fake_bin}:${PATH}" INSTALL_TEST_LOG="${invalid_log}" \
    SINGBOX_DEPLOY_VERSION="${invalid_version}" bash "${script_dir}/install.sh" >/dev/null 2>&1; then
    echo "invalid release version unexpectedly succeeded: ${invalid_version}" >&2
    exit 1
  fi
  if [ -s "${invalid_log}" ]; then
    echo "invalid release version reached the network fetch: ${invalid_version}" >&2
    exit 1
  fi
done

if "${script_dir}/package-installer.sh" 'v1.2.3/invalid' "${test_dir}/invalid-installer" >/dev/null 2>&1; then
  echo "invalid release tag unexpectedly packaged" >&2
  exit 1
fi

if "${script_dir}/package-installer.sh" v1.2.3 "${script_dir}/install.sh" >/dev/null 2>&1; then
  echo "packager unexpectedly overwrote its source installer" >&2
  exit 1
fi

echo "installer tests passed"
