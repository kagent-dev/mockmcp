package mockmcp

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// EchoParams is the input schema for the "echo" tool.
type EchoParams struct {
	Message string `json:"message" jsonschema:"the message to echo back"`
}

// AddNumbersParams is the input schema for the "add_numbers" tool.
type AddNumbersParams struct {
	A float64 `json:"a" jsonschema:"the first number"`
	B float64 `json:"b" jsonschema:"the second number"`
}

// GetTimeParams is the input schema for the "get_time" tool.
type GetTimeParams struct {
	Timezone string `json:"timezone,omitempty" jsonschema:"IANA timezone (e.g. UTC, America/New_York); defaults to UTC"`
}

// RegisterDefaultTools attaches mockmcp's default tool set (echo,
// add_numbers, get_time) to the supplied MCP server. Exposed so embedders
// can compose the default set with their own tools on a server they own.
func RegisterDefaultTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Echo a message back to the caller",
	}, echo)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_numbers",
		Description: "Return a + b",
	}, addNumbers)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_time",
		Description: "Return the current time in the given IANA timezone (default UTC)",
	}, getTime)
}

func echo(_ context.Context, _ *mcp.CallToolRequest, params *EchoParams) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: params.Message}},
	}, nil, nil
}

func addNumbers(_ context.Context, _ *mcp.CallToolRequest, params *AddNumbersParams) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%g", params.A+params.B)}},
	}, nil, nil
}

func getTime(_ context.Context, _ *mcp.CallToolRequest, params *GetTimeParams) (*mcp.CallToolResult, any, error) {
	tz := params.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, nil, fmt.Errorf("unknown timezone %q: %w", tz, err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: time.Now().In(loc).Format(time.RFC3339)}},
	}, nil, nil
}
