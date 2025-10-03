# Widgets & Payments API (stdlib mostly)

This is a self-contained Go HTTP API server that uses only the standard library and in-memory storage with mutexes. It exposes endpoints for Widgets and idempotent Payments.

## Requirements
- Go 1.20+ (almost no external dependencies).

## Run the server
```bash
go run ./main.go
```
The server listens on :8080

## Endpoints and Examples
Create Widget (non-idempotent)
```bash
curl -i -X POST http://localhost:8080/widgets \
-H 'Content-Type: application/json' \
-d '{"name":"gizmo"}'
```

List Widgets
```bash
curl -s http://localhost:8080/widgets
```

Get Widget by ID
```bash
curl -s http://localhost:8080/widgets/<ID>
```

Create Payment (idempotent via Idempotency-Key)
First attempt:
```bash
curl -i -X POST http://localhost:8080/payments \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: abc-123' \
  -d '{"amount":1499,"currency":"EUR","method":"card"}' \
  -w "\nTime: %{time_total}s\n"
```

Repeat with the same key (returns same 201, same body/Location, possibly in shorter time):
```bash
curl -i -X POST http://localhost:8080/payments \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: abc-123' \
  -d '{"amount":1499,"currency":"EUR","method":"card"}' \
  -w "\nTime: %{time_total}s\n"
```

Get Payment by ID
```bash
curl -s http://localhost:8080/payments/<ID>
```

## OpenAPI Document
The OpenAPI spec is in the file `openapi.yaml`

## View Go Documentation
Install godoc (once)
```bash
go install golang.org/x/tools/cmd/godoc@latest
```

Ensure $(go env GOPATH)/bin is on your PATH.

# Web UI for viewing documentation
From the project root:
```bash
godoc -http=:6060
```

Then open in your browser:

Package index: 
`http://localhost:6060/pkg/`

Navigate to the module/package (look for “postapi”).

# CLI output for viewing documentation
From the project root, for example:

```bash
go doc .
go doc . Payment
go doc . Widget
go doc . createWidget
go doc . createPayment
```

## Notes
- Storage is in-memory (maps + mutexes). The idempotency cache has no TTL; for long-running/high-traffic services consider TTL or LRU eviction.
- You could use an in-memory DB like SQLite (:memory:) instead of just a map with locking, so that you can use SQL-Queries to retrieve from the DB.
