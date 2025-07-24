#Copyright 2018 The Kubernetes Authors.
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

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.33.2

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
ENVTEST = go run ${PROJECT_DIR}/vendor/sigs.k8s.io/controller-runtime/tools/setup-envtest

GO111MODULE = on
export GO111MODULE
GOFLAGS ?= -mod=vendor
export GOFLAGS
GOPROXY ?=
export GOPROXY

DBG ?= 0

ifeq ($(DBG),1)
GOGCFLAGS ?= -gcflags=all="-N -l"
endif

GOARCH  ?= $(shell go env GOARCH)
GOOS    ?= $(shell go env GOOS)

VERSION     ?= $(shell git describe --always --abbrev=7)
REPO_PATH   ?= github.com/openshift/machine-api-provider-aws
LD_FLAGS    ?= -X $(REPO_PATH)/pkg/version.Raw=$(VERSION) -extldflags "-static"
MUTABLE_TAG ?= latest
IMAGE        = origin-aws-machine-controllers
BUILD_IMAGE ?= registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.24-openshift-4.20

# race tests need CGO_ENABLED, everything else should have it disabled
CGO_ENABLED = 0
unit : CGO_ENABLED = 1

.PHONY: all
all: generate build images check

NO_DOCKER ?= 1

ifeq ($(shell command -v podman > /dev/null 2>&1 ; echo $$? ), 0)
	ENGINE=podman
else ifeq ($(shell command -v docker > /dev/null 2>&1 ; echo $$? ), 0)
	ENGINE=docker
else
	NO_DOCKER=1
endif

USE_DOCKER ?= 0
ifeq ($(USE_DOCKER), 1)
	ENGINE=docker
endif

ifeq ($(NO_DOCKER), 1)
  DOCKER_CMD = CGO_ENABLED=$(CGO_ENABLED) GOARCH=$(GOARCH) GOOS=$(GOOS)
  IMAGE_BUILD_CMD = imagebuilder
else
  DOCKER_CMD = $(ENGINE) run --rm -e CGO_ENABLED=$(CGO_ENABLED) -e GOARCH=$(GOARCH) -e GOOS=$(GOOS) -v "$(PWD)":/go/src/github.com/openshift/machine-api-provider-aws:Z -w /go/src/github.com/openshift/machine-api-provider-aws $(BUILD_IMAGE)
  IMAGE_BUILD_CMD = $(ENGINE) build
endif

.PHONY: vendor
vendor:
	$(DOCKER_CMD) hack/go-mod.sh
.PHONY: generate
generate: gogen goimports

gogen:
	$(DOCKER_CMD) go generate ./pkg/... ./cmd/...

.PHONY: test
test: unit

bin:
	@mkdir $@

.PHONY: build
build: ## build binaries
	$(DOCKER_CMD) go build $(GOGCFLAGS) -o "bin/machine-controller-manager" \
               -ldflags "$(LD_FLAGS)" "$(REPO_PATH)/cmd/manager"
	$(DOCKER_CMD) go build  $(GOGCFLAGS) -o "bin/termination-handler" \
	             -ldflags "$(LD_FLAGS)" "$(REPO_PATH)/cmd/termination-handler"

.PHONY: images
images: ## Create images
ifeq ($(NO_DOCKER), 1)
	./hack/imagebuilder.sh
endif
	$(IMAGE_BUILD_CMD) -t "$(IMAGE):$(VERSION)" -t "$(IMAGE):$(MUTABLE_TAG)" ./

.PHONY: push
push:
	$(ENGINE) push "$(IMAGE):$(VERSION)"
	$(ENGINE) push "$(IMAGE):$(MUTABLE_TAG)"

.PHONY: check
check: fmt vet lint test # Check your code

.PHONY: unit
unit: # Run unit test
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path --bin-dir $(PROJECT_DIR)/bin --index https://raw.githubusercontent.com/openshift/api/master/envtest-releases.yaml)" ./hack/ci-test.sh

.PHONY: test-e2e
test-e2e: ## Run e2e tests
	 hack/e2e.sh

.PHONY: lint
lint: ## Go lint your code
	$(DOCKER_CMD) hack/go-lint.sh -min_confidence 0.3 $$(go list -f '{{ .ImportPath }}' ./... | grep -v -e 'github.com/openshift/machine-api-provider-aws/test' -e 'github.com/openshift/machine-api-provider-aws/pkg/cloud/aws/client/mock' -e 'github.com/openshift/machine-api-provider-aws/pkg/api/machine/v1')

.PHONY: fmt
fmt: ## Go fmt your code
	$(DOCKER_CMD) hack/go-fmt.sh .

.PHONY: goimports
goimports:
	$(DOCKER_CMD) hack/goimports.sh .
	hack/verify-diff.sh

.PHONY: vet
vet: ## Apply go vet to all go files
	$(DOCKER_CMD) hack/go-vet.sh ./...

.PHONY: help
help:
	@grep -E '^[a-zA-Z/0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
