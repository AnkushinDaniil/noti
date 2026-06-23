package permission

import (
	"os"
	"strings"
)

// brokerEnvURL returns the NOTI_BROKER_URL override (trailing slash trimmed),
// or "" when unset. This mirrors the MCP server's broker URL override so tests
// and custom deployments can point the gate at an alternate broker.
func brokerEnvURL() string {
	u := strings.TrimSpace(os.Getenv("NOTI_BROKER_URL"))
	return strings.TrimRight(u, "/")
}
