#!/bin/bash
# Drives one MCP session through every scenario. Usage: TOKEN=<jwt> drive.sh
TOKEN=${TOKEN:?set TOKEN to a signed test JWT}
URL=http://127.0.0.1:3000/mcp
H=(-H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -H "MCP-Protocol-Version: 2025-06-18")
SID=$(curl -s -D - -o /dev/null "${H[@]}" "$URL" -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture-client","version":"1.0.0"}}}' | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2}' | tr -d '\r')
echo "session=$SID"
S=(-H "Mcp-Session-Id: $SID")
curl -s "${H[@]}" "${S[@]}" "$URL" -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' -o /dev/null -w 'initialized %{http_code}\n'
curl -s "${H[@]}" "${S[@]}" "$URL" -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | tr -d '\n' | grep -o '"name":"[a-z_]*"' | tr '\n' ' '; echo
call() { # id name args [curl args]
  echo -n "call $2: "
  local id=$1 name=$2 args=$3; shift 3
  curl -s "${H[@]}" "${S[@]}" "$@" "$URL" -d "{\"jsonrpc\":\"2.0\",\"id\":$id,\"method\":\"tools/call\",\"params\":{\"name\":\"$name\",\"arguments\":$args}}" -w ' [http %{http_code}]' 2>&1 | tr -d '\n' | cut -c1-220; echo
}
call 10 search_docs '{"query":"refunds"}'
call 11 lookup_order '{"order_id":"o1"}'
call 12 update_order '{}'
call 13 crash_order '{}'
call 14 beta_ping '{}'
call 15 get_invoice '{"path":{"id":"inv_1"}}'
call 16 get_invoice '{"path":{"id":"missing"}}'
call 17 create_ticket '{"title":"x"}'
call 18 no_such_tool '{}'
echo -n "not-sampled traceparent: "
curl -s "${H[@]}" "${S[@]}" -H "traceparent: 00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00" "$URL" -d '{"jsonrpc":"2.0","id":19,"method":"tools/call","params":{"name":"search_docs","arguments":{"query":"unsampled"}}}' -w ' [http %{http_code}]' | tr -d '\n' | cut -c1-120; echo
echo -n "no token: "
curl -s -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" "$URL" -d '{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"search_docs","arguments":{}}}' -o /dev/null -w '[http %{http_code}]\n'
