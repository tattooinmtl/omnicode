curl.exe -s -X PUT -H "Content-Type: application/json" --data-raw "{\"value\":\"sk-ant-real-key-1234567890\"}" http://127.0.0.1:4499/api/secrets/MY_NEW_KEY
echo ""
curl.exe -s http://127.0.0.1:4499/api/secrets
echo ""
echo "--- create provider ---"
curl.exe -s -X POST -H "Content-Type: application/json" --data-raw "{\"name\":\"Anthropic\",\"kind\":\"anthropic\",\"base_url\":\"https://api.anthropic.com\",\"api_key_ref\":\"ANTHROPIC_API_KEY\",\"default_model\":\"claude-opus-4-6\",\"enabled\":true}" http://127.0.0.1:4499/api/providers
echo ""
echo "--- list providers ---"
curl.exe -s http://127.0.0.1:4499/api/providers
echo ""
echo "--- create project ---"
curl.exe -s -X POST -H "Content-Type: application/json" --data-raw "{\"name\":\"Test\",\"path\":\"C:\\\\Users\\\\ThePa\\\\Downloads\\\\clawd-code-main\\\\clawd-code-main\",\"description\":\"My project\",\"default_model\":\"claude-opus-4-6\"}" http://127.0.0.1:4499/api/projects
echo ""
echo "--- list projects ---"
curl.exe -s http://127.0.0.1:4499/api/projects
echo ""
echo "--- create MCP server ---"
curl.exe -s -X POST -H "Content-Type: application/json" --data-raw "{\"name\":\"filesystem\",\"transport\":\"stdio\",\"command\":\"npx\",\"args\":\"[\\\"-y\\\",\\\"@modelcontextprotocol/server-filesystem\\\"]\",\"enabled\":true}" http://127.0.0.1:4499/api/mcp
echo ""
echo "--- list MCP servers ---"
curl.exe -s http://127.0.0.1:4499/api/mcp
echo ""
echo "--- create session ---"
curl.exe -s -X POST -H "Content-Type: application/json" --data-raw "{\"title\":\"Test session\"}" http://127.0.0.1:4499/api/sessions
echo ""
echo "--- audit log ---"
curl.exe -s http://127.0.0.1:4499/api/audit
echo ""
echo "--- static UI ---"
curl.exe -s -o /dev/null -w "%{http_code} %{size_download}\n" http://127.0.0.1:4499/
curl.exe -s -o /dev/null -w "%{http_code} %{size_download}\n" http://127.0.0.1:4499/static/app.js
curl.exe -s -o /dev/null -w "%{http_code} %{size_download}\n" http://127.0.0.1:4499/static/app.css
curl.exe -s -o /dev/null -w "%{http_code} %{size_download}\n" http://127.0.0.1:4499/static/favicon.svg
