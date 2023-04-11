#!/bin/bash -e

BASEDIR="$(realpath "$(dirname "$0")/..")"

if [[ -z "$1" ]]; then
  VERSION=$(cat "$BASEDIR/VERSION")
else
  VERSION=$1
fi

echo "Modifying nfs-subdir-external-provisioner version to: $1"
echo "$VERSION" > VERSION
