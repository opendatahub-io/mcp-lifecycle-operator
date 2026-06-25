# Override the IMAGE_TAG_BASE from the upstream repo
IMAGE_TAG_BASE ?= quay.io/opendatahub/odh-mcp-lifecycle-operator
IMAGE_TAG ?= odh-stable

include Makefile

.PHONY: build-ocp
build-ocp: clean fmt
	CGO_ENABLED=1 $(GO_BUILD_ENV) go build $(COMMON_BUILD_ARGS) -tags=strictfipsruntime -mod=vendor -a -o manager ./cmd
