// Command stub serves a scripted MCP server and a small REST API so a gateway
// in front of them produces every tool-call outcome the fixtures need.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var toolsByServer = map[string][]string{
	"alpha": {"search_docs", "lookup_order", "update_order", "crash_order"},
	"beta":  {"beta_ping"},
}

func main() {
	name := flag.String("name", "alpha", "server name, selects the tool set")
	mcpAddr := flag.String("mcp", "127.0.0.1:3101", "MCP listen address, empty to disable")
	apiAddr := flag.String("api", "", "REST API listen address, empty to disable")
	flag.Parse()

	if *apiAddr != "" {
		go func() { log.Fatal(http.ListenAndServe(*apiAddr, apiHandler())) }()
	}
	if *mcpAddr == "" {
		select {}
	}
	log.Fatal(http.ListenAndServe(*mcpAddr, mcpHandler(*name)))
}

func mcpHandler(server string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeRPC(w, nil, nil, &rpcError{-32700, "parse error"})
			return
		}
		if strings.HasPrefix(req.Method, "notifications/") || len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			w.Header().Set("Mcp-Session-Id", "stub-"+server+"-session")
			writeRPC(w, req.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": server, "version": "0.0.1"},
			}, nil)
		case "ping":
			writeRPC(w, req.ID, map[string]any{}, nil)
		case "tools/list":
			var tools []tool
			for _, n := range toolsByServer[server] {
				tools = append(tools, tool{Name: n, Description: n, InputSchema: map[string]any{"type": "object"}})
			}
			writeRPC(w, req.ID, map[string]any{"tools": tools}, nil)
		case "tools/call":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			callTool(w, req.ID, p.Name)
		default:
			writeRPC(w, req.ID, nil, &rpcError{-32601, "method not found"})
		}
	})
	return mux
}

func callTool(w http.ResponseWriter, id json.RawMessage, name string) {
	text := func(s string, isError bool) map[string]any {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}, "isError": isError}
	}
	switch name {
	case "search_docs", "beta_ping":
		writeRPC(w, id, text("ok", false), nil)
	case "lookup_order":
		writeRPC(w, id, text("order not found", true), nil)
	case "update_order":
		writeRPC(w, id, nil, &rpcError{-32602, "invalid params: order_id is required"})
	case "crash_order":
		http.Error(w, "upstream exploded", http.StatusBadGateway)
	default:
		writeRPC(w, id, nil, &rpcError{-32602, "unknown tool " + name})
	}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("write: %v", err)
	}
}

func apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/invoices/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/invoices/")
		if id == "missing" {
			http.Error(w, `{"message":"invoice not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"total":42}`, id)
	})
	mux.HandleFunc("/tickets", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"ticket service down"}`, http.StatusInternalServerError)
	})
	return mux
}
