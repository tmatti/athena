// Package mcpserver exposes the Brain service as an MCP server. Tools are
// thin adapters over internal/service; no business logic lives here.
package mcpserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmatti/athena/internal/service"
)

// New builds an MCP server with all Athena tools registered against brain.
func New(brain *service.Brain) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "athena", Version: "0.1.0"}, nil)
	registerTools(server, brain)
	return server
}

// HTTPHandler serves the MCP server over the SDK's Streamable HTTP
// transport, suitable for mounting behind the existing bearer-auth
// middleware in internal/api.
func HTTPHandler(brain *service.Brain) http.Handler {
	server := New(brain)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)
}
