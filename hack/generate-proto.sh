#!/usr/bin/env bash

set -euxo pipefail

# shellcheck disable=SC2128
PROJECT_ROOT=$(cd "$(dirname "${BASH_SOURCE}")"/..; pwd)

rm -rf "${PROJECT_ROOT}/pkg/api"
mkdir -p "${PROJECT_ROOT}/pkg/api"
ln -s "${PROJECT_ROOT}/api/v1alpha1" "${PROJECT_ROOT}/pkg/api/v1alpha1"

PACKAGES=(
  github.com/akuity/kargo/pkg/api/v1alpha1
)
APIMACHINERY_PKGS=(
  +k8s.io/apimachinery/pkg/util/intstr
  +k8s.io/apimachinery/pkg/api/resource
  +k8s.io/apimachinery/pkg/runtime/schema
  +k8s.io/apimachinery/pkg/runtime
  k8s.io/apimachinery/pkg/apis/meta/v1
  k8s.io/api/core/v1
  k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1
)

go-to-protobuf \
  --go-header-file="${PROJECT_ROOT}/hack/boilerplate.go.txt" \
  --packages="$(IFS=, ; echo "${PACKAGES[*]}")" \
  --apimachinery-packages="$(IFS=, ; echo "${APIMACHINERY_PKGS[*]}")" \
  --only-idl \
  --trim-path-prefix="${PROJECT_ROOT}/github.com/akuity/kargo/" \
  --output-base="${PROJECT_ROOT}"

# Generate apimachinery types for UI
paths=()
for pkg in "${APIMACHINERY_PKGS[@]}"; do
  pkg="${pkg#+}"
  paths+=("--path=./vendor/${pkg}")
done

buf generate --template=ui.buf.gen.yaml "${paths[@]}"

rm -rf "${PROJECT_ROOT}/pkg/api/v1alpha1"
