#!/bin/sh
# Compatible with the upstream Helm chart's explicit `...sh minio server` call.
set -eu
if [ "$#" -eq 0 ]; then set -- minio --help; fi
if [ "$1" != minio ]; then set -- minio "$@"; fi
exec "$@"
