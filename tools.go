//go:build tools

package tools

// Keep the downstream E2E image's build tool in go.mod after rebases.
import _ "gotest.tools/gotestsum"
