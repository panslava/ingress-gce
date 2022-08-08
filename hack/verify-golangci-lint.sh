#!/bin/sh

# Copyright YEAR The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This script checks coding style for go language using golangci-lint
# Usage: `hack/verify-golangci-lint.sh`.
set -o errexit
set -o nounset
set -o pipefail

# Ensure that we find the binaries we build before anything else.
export GO111MODULE=on

# Install golangci-lint
go version
echo 'installing golangci-lint '
cd "hack/tools"
  go get github.com/golangci/golangci-lint/cmd/golangci-lint
cd ../../

echo 'running golangci-lint '
if [[ "$#" -gt 0 ]]; then
    golangci-lint run "$@"
else
    golangci-lint run
fi