#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${1:-.cache/grocksdb/include}"
if [[ "${OUT_DIR}" != /* ]]; then
	OUT_DIR="$(pwd)/${OUT_DIR}"
fi

GROCKSDB_DIR="$(go list -m -f '{{.Dir}}' github.com/linxGnu/grocksdb)"
SOURCE_HEADER="${GROCKSDB_DIR}/c.h"
TARGET_DIR="${OUT_DIR%/}/rocksdb"
TARGET_HEADER="${TARGET_DIR}/c.h"

if [ ! -f "${SOURCE_HEADER}" ]; then
	echo "grocksdb header not found: ${SOURCE_HEADER}" >&2
	exit 1
fi

mkdir -p "${TARGET_DIR}"
rm -f "${TARGET_HEADER}"
cp "${SOURCE_HEADER}" "${TARGET_HEADER}"
chmod u+w "${TARGET_HEADER}"

if ! grep -q "rocksdb_options_set_skip_checking_sst_file_sizes_on_db_open" "${TARGET_HEADER}"; then
	echo "grocksdb header is incompatible: ${TARGET_HEADER}" >&2
	exit 1
fi

echo "${OUT_DIR%/}"
