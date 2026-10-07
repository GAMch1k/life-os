package main

import (
	"context"
	"github.com/GAMch1k/life-os/services/mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log"
	"net/http"
	"os"
)

type ListInput struct {
	Domain string `json:"domain" jsonschema:"enum=finance,enum=tasks,enum=inbox,enum=calendar"`
	Path   string `json:"path" jsonschema:"description=Approved API path"`
}
type Output struct {
	Message string `json:"message"`
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	_ = cfg
	s := mcp.NewServer(&mcp.Implementation{Name: "life-os", Version: "0.1.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "lifeos_status", Description: "Report MCP service readiness"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, Output, error) {
		return nil, Output{Message: "Life OS MCP server is ready; core tool registration is in progress."}, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.New(os.Stderr, "mcp: ", 0).Print(err)
	}
	_ = http.MethodGet
}
