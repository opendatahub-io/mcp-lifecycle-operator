# Override the IMAGE_TAG_BASE from the upstream repo
IMAGE_TAG_BASE ?= quay.io/opendatahub/odh-mcp-lifecycle-operator
IMAGE_TAG ?= odh-stable

include Makefile

# E2E test container image.
IMAGE_TAG_BASE_E2E ?= $(IMAGE_TAG_BASE)/e2e
IMG_E2E ?= $(IMAGE_TAG_BASE_E2E):$(IMAGE_TAG)

.PHONY: image-e2e
image-e2e: ## Build e2e test container image locally.
	$(CONTAINER_TOOL) build -f test/e2e/Dockerfile -t $(IMG_E2E) .

.PHONY: build-ocp
build-ocp: clean fmt
	CGO_ENABLED=1 $(GO_BUILD_ENV) go build $(COMMON_BUILD_ARGS) -tags=strictfipsruntime -mod=vendor -a -o manager ./cmd
