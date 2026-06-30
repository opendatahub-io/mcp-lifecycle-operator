include Makefile

.PHONY: build-ocp
build-ocp: clean fmt
	CGO_ENABLED=1 $(GO_BUILD_ENV) go build $(COMMON_BUILD_ARGS) -tags=strictfipsruntime -mod=vendor -a -o manager ./cmd
