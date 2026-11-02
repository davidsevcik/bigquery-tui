# Research 04: MCP server library for Go, and what the protocol imposes

Ticket: [04-research-mcp-go-sdk](https://github.com/davidsevcik/bigquery-tui/issues/53). Researched 2026-10-12 against primary sources (spec, SDK repos and source, Claude Code docs, vendor docs). Items I could not verify are marked **unverified**.

## Recommendation

1. **Use the official SDK, `github.com/modelcontextprotocol/go-sdk` (v1.8.0), over stdio for v1.** `bqt mcp` is a subcommand that serves `mcp.StdioTransport`. No HTTP transport is needed for v1; the SDK has one (`NewStreamableHTTPHandler`) if wanted later.
2. **Declare tools with `mcp.AddTool` and typed Go structs**, so input and output JSON Schemas are inferred from `json`/`jsonschema` tags. The core library's request/response types can double as the tool schemas.
3. **Lead with tools, not resources or prompts.** Claude Code defers MCP tools by default (tool search), so a small named toolset costs little context. Add one or two resources and prompts only as extras.
4. **Treat output size as a tool-design problem.** The protocol paginates only list operations, not `tools/call`. Give every tool a hard row/byte cap, a `limit`/`offset` or opaque `page_token` argument, and a truncation notice in the result.
5. **Enforce read-only and cost limits in bqt's core, not in MCP annotations.** `readOnlyHint` is explicitly untrusted. Prior art splits into SQL-statement checks, dry-run before run, `MaxBytesBilled`, and dataset allowlists (details below).

## 1. Library options

| | Official `modelcontextprotocol/go-sdk` | `mark3labs/mcp-go` |
|---|---|---|
| Status | Org-owned, "maintained in collaboration with Google". Latest release v1.8.0 on 2026-09-14; HEAD commits dated 2026-10-12; 5.2k stars, not archived | Community. Latest release v1.2.1 on 2026-10-09 (v1.0.0 on 2026-09-02); last push 2026-10-09; 9.2k stars, not archived, README says "under active development" |
| Spec coverage | v1.7.0+ supports 2026-07-28 plus 2025-11-25, 2025-06-18, 2025-03-26, 2024-11-05 | README states 2025-11-25, with 2025-06-18, 2025-03-26, 2024-11-05 for back-compat. v1.0.0 notes added 2026-07-28 support (README not yet updated) |
| Transports | stdio (`StdioTransport`, `CommandTransport`), streamable HTTP (`StreamableHTTPHandler`, stateless mode), in-memory | stdio, SSE, streamable HTTP |
| Tool schema | Generic `mcp.AddTool[In, Out]`: infers input and output schemas from struct types, validates args and output, fills `StructuredContent` and text `Content` automatically | Builder style (`mcp.NewTool` with `mcp.WithString`, ...) plus `WithInputSchema[T]()` / `WithOutputSchema[T]()` generics |
| Annotations | `Tool.Annotations *ToolAnnotations` (`ReadOnlyHint`, `DestructiveHint`, `IdempotentHint`, `OpenWorldHint`) | `WithReadOnlyHintAnnotation(...)` option etc. |
| Pagination | Built in, `ServerOptions.PageSize`, default 1000 | Not checked |
| Go version / license | `go 1.25.0`; Apache-2.0 for new code, MIT for existing | `go 1.25.5`; MIT |
| Caveat | README states no general API stability guarantee, though it is on v1.x. v1.8.0 release notes mention `MCPGODEBUG` flags slated for removal in v1.9.0, so expect minor behavior churn | Same: no stability promise checked |

Both are healthy and current as of today, and both need Go 1.25. The official SDK wins on: tracking the spec first (it is the reference implementation), org ownership (lower abandonment risk), Google co-maintenance, and the typed-struct schema flow, which fits "core library with thin frontends". mark3labs is a fine fallback; swapping is cheap because bqt's tool handlers should be thin wrappers over the core library.

Sources:
- go-sdk README (version table, license, transports): https://github.com/modelcontextprotocol/go-sdk and https://raw.githubusercontent.com/modelcontextprotocol/go-sdk/main/README.md
- go-sdk releases: https://api.github.com/repos/modelcontextprotocol/go-sdk/releases ; commits: https://api.github.com/repos/modelcontextprotocol/go-sdk/commits ; go.mod: https://raw.githubusercontent.com/modelcontextprotocol/go-sdk/main/go.mod
- go-sdk server docs (AddTool, schema inference, pagination, resources, prompts): https://github.com/modelcontextprotocol/go-sdk/blob/main/docs/server.md ; protocol docs (transports): https://github.com/modelcontextprotocol/go-sdk/blob/main/docs/protocol.md
- go-sdk source (`ToolAnnotations`, `DefaultPageSize = 1000`, `NewStreamableHTTPHandler`, `AddTool[In, Out]`): https://github.com/modelcontextprotocol/go-sdk/tree/main/mcp
- mcp-go: https://github.com/mark3labs/mcp-go ; releases https://api.github.com/repos/mark3labs/mcp-go/releases ; repo stats https://api.github.com/repos/mark3labs/mcp-go ; go.mod https://raw.githubusercontent.com/mark3labs/mcp-go/main/go.mod ; tools https://raw.githubusercontent.com/mark3labs/mcp-go/main/mcp/tools.go

## 2. Tools vs resources vs prompts for bqt

The spec assigns each primitive a controller:

- **Tools are model-controlled**: the model discovers and calls them on its own. A human in the loop "SHOULD" be able to deny calls. ([spec: tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools))
- **Resources are application-driven**: the host decides how to use them, e.g. a picker. ([spec: resources](https://modelcontextprotocol.io/specification/2025-11-25/server/resources))
- **Prompts are user-controlled**: explicit selection, e.g. slash commands. ([spec: prompts](https://modelcontextprotocol.io/specification/2025-11-25/server/prompts))

Claude Code behavior ([Claude Code MCP docs](https://code.claude.com/docs/en/mcp)):
- Resources are referenced by the user with `@server:protocol://path`, and Claude Code "automatically provides tools to list and read MCP resources when servers support them".
- Prompts appear as `/mcp__servername__promptname` commands.
- Tools are deferred by default (tool search): only tool names and server `instructions` load at session start.

Mapping for bqt (all names are suggestions, for the agent-layer ticket to settle):

| Capability | Primitive | Why |
|---|---|---|
| Search tables/columns (FTS over the local store) | Tool `search` | Model-initiated, parameterized, paginated |
| Schema lookup for one table | Tool `get_table_schema` (ID in, columns out) | The model needs it mid-task. A resource template like `bqt://schema/{project}.{dataset}.{table}` can expose the same data for `@` mentions, but should not be the only path since resources are application-driven |
| Dry-run | Tool `dry_run` (SQL in, bytes processed and validity out) | Model-initiated, cheap, safe |
| Read-only query | Tool `run_query` (SQL in, capped rows out) | Model-initiated; carries the safety model |
| List projects/datasets | Tool `list_*`, or a small resource | Browsing aid |
| Favorites / history | Optional resources (`bqt://favorites/{name}`); prompts for "run this favorite" are optional | User-driven selection fits resources/prompts better than model tools |

Tool-level details from the spec:
- Result content may be unstructured (`content`) or structured (`structuredContent`, with an optional `outputSchema`). A tool returning structured content SHOULD also return the serialized JSON as text. The Go SDK does this automatically.
- Two error channels: JSON-RPC protocol errors, and tool execution errors with `isError: true`. Use `isError` for SQL errors and policy rejections, since "Clients SHOULD provide tool execution errors to language models to enable self-correction". In the Go SDK, returning an ordinary error from a handler sets `IsError`.
- Tool names: 1 to 128 characters, `[A-Za-z0-9_.-]`.
- Claude Code skips tools whose input schema is invalid JSON Schema 2020-12 or whose top-level property names break the same character rules. It flattens root-level `anyOf`/`oneOf`/`allOf`. Keep input schemas as plain objects.
- Server `instructions` and each tool description are truncated at 2,048 characters by Claude Code; put key guidance first. Set `ServerOptions.Instructions` to a short "what bqt is, when to use it" paragraph since tool search keys on it.
- Claude Code `_meta` extensions a server can set on tools: `anthropic/alwaysLoad` (load upfront), `anthropic/maxResultSizeChars` (raise the output cap, ceiling 500,000), `anthropic/requiresUserInteraction` (prompt on every call). The last one could be applied to any future write-capable tool (out of scope for v1).

## 3. How agents configure a local stdio server (zero setup)

`bqt mcp` must speak MCP on stdout and log only to stderr. The spec says the server "MUST NOT write anything to its stdout that is not a valid MCP message" and may log to stderr ([spec: transports](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)). Any stray `fmt.Println` or library banner breaks the session. Clients "SHOULD support stdio whenever possible".

Per-client setup, each a one-liner or a few lines:

- **Claude Code**: `claude mcp add bqt -- bqt mcp` (syntax `claude mcp add [options] <name> -- <command> [args...]`; scopes local/project/user). Or commit a project `.mcp.json` with `command`/`args`/`env` (env vars expandable with `${VAR}` / `${VAR:-default}`). Project-scoped servers prompt for approval in interactive sessions but load without a prompt under `claude -p` and the Agent SDK. Source: https://code.claude.com/docs/en/mcp . The `.mcp.json` stdio example was not shown verbatim on that page (only an http one); the `command`/`args`/`env` field names are confirmed by the env-expansion section. **Unverified**: exact `"type": "stdio"` requirement.
- **Codex**: `codex mcp add bqt -- bqt mcp`, or `[mcp_servers.bqt]` with `command`, `args`, `env` in `~/.codex/config.toml` / `.codex/config.toml`. Source: https://learn.chatgpt.com/docs/extend/mcp?surface=cli
- **Gemini CLI**: `mcpServers.bqt` in `settings.json` with `command`, `args`, `env`, `trust`, `includeTools`/`excludeTools`. Source: https://raw.githubusercontent.com/google-gemini/gemini-cli/main/docs/tools/mcp-server.md

Implication: ship a `bqt mcp` subcommand that needs no flags, reads the same config file as the TUI (execution project, region), and uses Application Default Credentials. The "zero setup" is then one line per client. A `bqt mcp --print-config` helper that emits the JSON/TOML snippets is a cheap extra.

## 4. Output size limits and pagination

**What the protocol gives:** cursor pagination (`cursor`/`nextCursor`, server-chosen page size, opaque tokens) for `tools/list`, `resources/list`, `resources/templates/list`, `prompts/list` only ([spec: pagination](https://modelcontextprotocol.io/specification/2025-11-25/server/utilities/pagination)). `tools/call` has no pagination. The spec sets no output size limit.

**What Claude Code does** ([MCP docs, "MCP output limits and warnings"](https://code.claude.com/docs/en/mcp)):
- Warns when a tool output exceeds 10,000 tokens.
- Default max 25,000 tokens (`MAX_MCP_OUTPUT_TOKENS` raises it).
- Successful text results over 50,000 characters are saved to a file and replaced by a file path, unless the tool raises its own limit via `_meta["anthropic/maxResultSizeChars"]` (ceiling 500,000).

**Conventions to adopt** (design guidance derived from the above and the prior art below, not a spec requirement):
- Every list-like tool takes `limit` (default small, e.g. 20 to 50) and a continuation argument (`offset` or opaque `page_token`) and returns `total`/`truncated`/`next_page_token` fields. Prior art defaults: genai-toolbox `maxQueryResultRows` defaults to 50; Google's remote BigQuery server caps results at 3,000 rows.
- Return compact structured content: table IDs and column `name:type` lists, not full JSON schemas with nested descriptions. Offer a `detail` or `fields` argument for the heavier view.
- Budget by bytes or tokens as well as rows, because wide rows and nested columns blow the limit before the row cap does. Stay under about 10k tokens by default.
- When truncating, say so in the text the model reads and say how to continue (e.g. "showing 50 of 1,240; call again with page_token=..."). Silent truncation misleads the agent.
- For big query results, consider writing the full result to a file (Favorites/History directory or a temp path) and returning the path plus a preview, matching how Claude Code itself spills large results.
- Use the SDK's `PageSize` for `tools/list`-style list operations (default 1000) only if bqt ever exposes many resources; with a handful of tools it does not matter.

## 5. Prior art: read-only and cost enforcement

| Server | Read-only mechanism | Cost / size limits |
|---|---|---|
| **Google MCP Toolbox** (`googleapis/mcp-toolbox`, formerly genai-toolbox; Apache-2.0, Go), BigQuery source | `writeMode`: `allowed` (default), `blocked` (strict: only `SELECT`; write-capable tools are not registered), `protected` (shared session, temp tables ok, permanent datasets protected). `readOnly: true` defaults `writeMode` to `blocked`. `allowedDatasets` rejects other datasets, and also rejects dataset-level ops and anything whose table access cannot be statically analyzed (`EXECUTE IMMEDIATE`, `CREATE PROCEDURE`) | `maxQueryResultRows` (default 50); `location` for jobs; `impersonateServiceAccount` |
| **Google remote BigQuery MCP** (`https://bigquery.googleapis.com/mcp`, streamable HTTP) | Separate `execute_sql_readonly` tool (no DML/DDL/Python UDFs/GQL) and `execute_sql`, the only non-read-only tool; IAM deny policy can block `execute_sql` | 3,000-row result cap; queries cancelled after 3 minutes by default; no server-side quota of its own. Docs do not mention max-bytes-billed or dry-run |
| **`ergut/mcp-bigquery-server`** | `SELECT` only; every query validated by BigQuery's own dry run before execution; rejects `INSERT/UPDATE/DELETE/DROP/TRUNCATE/EXPORT DATA/MERGE` | `maximumBytesBilled` default 1 GB, flag `--maximum-bytes-billed`. Release dates not shown on the page; maintenance status **unverified** |
| **`LucasHild/mcp-server-bigquery`** | Not stated | Optional `--dataset` allowlist and `--timeout`; no cost cap. Tools `execute-query`, `list-tables`, `describe-table`. Maintenance status **unverified** |
| **Postgres reference server** (`modelcontextprotocol/servers-archived`, archived 2025-05-29) | All queries run in a `READ ONLY` transaction; one `query` tool; per-table schema as a resource `postgres://<host>/<table>/schema` | None |
| **`crystaldba/postgres-mcp`** | Restricted mode: read-only transaction plus a SQL parse (pglast) that rejects `COMMIT`/`ROLLBACK` to stop `ROLLBACK; DROP ...` tricks. Warns that unsafe stored-procedure languages can bypass it | Execution-time limit in restricted mode; `explain_query` for cost |

Sources: https://googleapis.github.io/genai-toolbox/ (repo https://github.com/googleapis/genai-toolbox ; BigQuery source doc https://raw.githubusercontent.com/googleapis/mcp-toolbox/main/docs/en/integrations/bigquery/source.md) ; https://docs.cloud.google.com/bigquery/docs/use-bigquery-mcp ; https://github.com/ergut/mcp-bigquery-server ; https://github.com/LucasHild/mcp-server-bigquery ; https://github.com/modelcontextprotocol/servers-archived/tree/main/src/postgres ; https://github.com/crystaldba/postgres-mcp

Observations for bqt's agent safety model:
- Best practice is layered: (1) credentials/IAM that cannot write; (2) statement-type classification before running; (3) server-side cost cap; (4) result row/byte cap; (5) timeout. No single layer is trusted.
- MCP `readOnlyHint` and friends are advisory: the spec says clients "MUST consider tool annotations to be untrusted unless they come from trusted servers", and the spec's `ToolAnnotations` schema describes all of them as hints that may not accurately describe the tool's behavior (https://raw.githubusercontent.com/modelcontextprotocol/modelcontextprotocol/main/schema/2025-11-25/schema.ts). Set them (`run_query`: `ReadOnlyHint: true`, `OpenWorldHint: true`; local-store tools: read-only, closed-world) because clients use them for UX, but never rely on them. Whether Claude Code changes permission prompts based on `readOnlyHint` is **unverified** (not found in the docs read).
- The BigQuery Go client has the primitives needed: `QueryConfig.DryRun` and `QueryConfig.MaxBytesBilled` exist (confirmed in https://raw.githubusercontent.com/googleapis/google-cloud-go/main/bigquery/query.go). BigQuery fails a query server-side if it would exceed `MaxBytesBilled`, which makes it a hard cost cap rather than a heuristic. **Unverified here**: that dry-run job statistics expose a `statementType` usable to classify read vs write without parsing SQL; the query-run ticket should confirm it.
- Splitting `dry_run` from `run_query` mirrors the Google remote server's split of `execute_sql_readonly`, and lets the agent check cost before spending it.

## 6. Open points handed to other tickets

- Exact tool set, names, argument schemas and per-tool caps: agent-layer design.
- How `run_query` classifies statements (dry-run `statementType` vs SQL parsing vs both) and the default `MaxBytesBilled`: query-run and agent-safety decisions.
- Whether the MCP server accepts a per-session cost budget, and whether it shares the History store with the TUI (concurrent SQLite access from a separate `bqt mcp` process): store layout ticket.
- Whether to ship HTTP transport: not for v1; stdio matches "works with zero setup".
