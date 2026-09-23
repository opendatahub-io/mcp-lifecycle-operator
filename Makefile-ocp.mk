# Override the IMAGE_TAG_BASE from the upstream repo
IMAGE_TAG_BASE ?= quay.io/opendatahub/odh-mcp-lifecycle-operator
IMAGE_TAG ?= odh-stable

# Use the ODH overlay to include the Prometheus ServiceMonitor.
KUSTOMIZE_DEFAULT_DIR = config/overlays/odh

# Run downstream builds and tests with the Go compiler provided by the container.
export GOTOOLCHAIN := local
GO_TOOLCHAIN := local
LOCALBIN ?= $(CURDIR)/bin/openshift

include Makefile

# E2E test container image.
IMAGE_TAG_BASE_E2E ?= $(IMAGE_TAG_BASE)/e2e
IMG_E2E ?= $(IMAGE_TAG_BASE_E2E):$(IMAGE_TAG)

.PHONY: image-e2e
image-e2e: ## Build e2e test container image locally.
	$(CONTAINER_TOOL) build -f test/e2e/Dockerfile -t $(IMG_E2E) .

.PHONY: build-ocp
build-ocp: clean
	CGO_ENABLED=1 $(GO_BUILD_ENV) go build $(COMMON_BUILD_ARGS) -tags=strictfipsruntime -mod=readonly -a -o manager ./cmd

.PHONY: test-local
test-local: setup-envtest kuadrant-test-crds ## Run unit tests with the container's Go compiler.
	packages="$$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)" && \
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
		go test $$packages -coverprofile $(COVER_PROFILE)
ifneq ($(strip $(COVER_PROFILE_E2ECOVERAGE)),)
	go test -tags=e2ecoverage -run 'Coverage|Flusher' ./cmd/... -coverprofile $(COVER_PROFILE_E2ECOVERAGE)
endif
