// Command sink receives OTLP/HTTP trace exports and writes each request as
// protojson, plus a one-line summary per span on stdout.
package main

import (
	"compress/gzip"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4318", "listen address")
	out := flag.String("out", "out", "directory for recorded requests")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	var n atomic.Int64
	http.HandleFunc("/v1/traces", func(w http.ResponseWriter, r *http.Request) {
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			body = gz
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req collectortrace.ExportTraceServiceRequest
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			err = protojson.Unmarshal(raw, &req)
		} else {
			err = proto.Unmarshal(raw, &req)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		i := n.Add(1)
		js, _ := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(&req)
		name := filepath.Join(*out, fmt.Sprintf("%04d.json", i))
		if err := os.WriteFile(name, js, 0o644); err != nil {
			log.Printf("write: %v", err)
		}
		summarize(i, r.Header.Get("Content-Type"), &req)
		resp, _ := proto.Marshal(&collectortrace.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(resp)
	})
	log.Printf("sink on %s writing to %s", *addr, *out)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func summarize(i int64, contentType string, req *collectortrace.ExportTraceServiceRequest) {
	for _, rs := range req.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				fmt.Printf("req=%d ct=%s kind=%s name=%q span=%s parent=%s status=%s %s\n",
					i, contentType, s.Kind, s.Name, hex.EncodeToString(s.SpanId), hex.EncodeToString(s.ParentSpanId),
					s.GetStatus().GetCode(), attrs(s.Attributes))
			}
		}
	}
}

func attrs(kvs []*commonpb.KeyValue) string {
	var parts []string
	for _, kv := range kvs {
		k := kv.Key
		if strings.HasPrefix(k, "mcp.") || strings.HasPrefix(k, "gen_ai.") || strings.HasPrefix(k, "posthog.") ||
			strings.HasPrefix(k, "error.") || k == "http.response.status_code" || k == "jsonrpc.request.id" {
			parts = append(parts, fmt.Sprintf("%s=%s", k, value(kv.Value)))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func value(v *commonpb.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return fmt.Sprintf("%q", x.StringValue)
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprint(x.IntValue)
	case *commonpb.AnyValue_BoolValue:
		return fmt.Sprint(x.BoolValue)
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprint(x.DoubleValue)
	default:
		return v.String()
	}
}
