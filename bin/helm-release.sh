#!/bin/bash
# exit immediately when a command fails
set -e
# only exit with zero if all commands of the pipeline exit successfully
set -o pipefail

BASEDIR="$(realpath "$(dirname "$0")/..")"
CHART_NAME=nfs-subdir-external-provisioner

if [[ -z "$1" ]]; then
  VERSION=$(cat "$BASEDIR/VERSION")
else
  VERSION=$1
fi

if [[ -z "$2" ]]; then
  ARTIFACTS_BUILD_DIR="dist/charts"
else
  ARTIFACTS_BUILD_DIR=$2
fi

echo "Building HELM chart for ${CHART_NAME} ${VERSION} version"

rm -rf "${ARTIFACTS_BUILD_DIR:?}/${CHART_NAME}"
mkdir -p "${ARTIFACTS_BUILD_DIR}"

cp -R charts/${CHART_NAME} "${ARTIFACTS_BUILD_DIR}/"
sed -i "/tag: /s/0\.0\.0/${VERSION}/" "${ARTIFACTS_BUILD_DIR}/${CHART_NAME}/values.yaml"
helm package "${ARTIFACTS_BUILD_DIR}/${CHART_NAME}" \
    --app-version="${VERSION}" \
    --version="${VERSION}" \
    --dependency-update \
    --destination="${ARTIFACTS_BUILD_DIR}"

rm -rf "${ARTIFACTS_BUILD_DIR}/${CHART_NAME}"
