ARG BUILDER_IMAGE=registry.access.redhat.com/ubi9/go-toolset:latest
ARG BASE_IMAGE=registry.access.redhat.com/ubi9/ubi-minimal:latest

FROM ${BUILDER_IMAGE} AS builder
ARG TARGETOS=linux
ARG TARGETARCH
ARG TARGETPLATFORM

USER root
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY test/ test/

ENV GOEXPERIMENT=strictfipsruntime
RUN CGO_ENABLED=1 GOEXPERIMENT=strictfipsruntime \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go test -c -race -tags=e2e -o /e2e-tests ./test/e2e/

ARG GOTESTSUM_VERSION=v1.13.0
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go install gotest.tools/gotestsum@${GOTESTSUM_VERSION} && \
    mv $(go env GOPATH)/bin/gotestsum /gotestsum

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /test2json $(go env GOROOT)/src/cmd/test2json

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /e2e-run ./test/e2e/cmd/run/

FROM --platform=$TARGETPLATFORM ${BASE_IMAGE}
ARG USER_ID=65532
WORKDIR /e2e
COPY --from=builder /e2e-tests .
COPY --from=builder /gotestsum /usr/local/bin/gotestsum
COPY --from=builder /test2json /usr/local/bin/test2json
COPY --from=builder /e2e-run .
RUN mkdir -p /artifacts && chown ${USER_ID}:${USER_ID} /artifacts

USER ${USER_ID}:${USER_ID}

LABEL com.redhat.component=mcp-lifecycle-operator
LABEL description="MCP lifecycle operator e2e tests"
LABEL io.k8s.description="MCP lifecycle operator e2e tests"
LABEL io.k8s.display-name="MCP lifecycle operator e2e tests"
LABEL io.openshift.tags="openshift,mcp,operator,e2e"
LABEL name="mcp-lifecycle-operator-beta/mcp-lifecycle-operator-e2e-rhel9"
LABEL summary="MCP lifecycle operator e2e tests"

ENTRYPOINT ["/e2e/e2e-run"]
CMD ["./..."]
