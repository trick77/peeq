# MCP server

peeq serves its library to MCP clients (loom, or any other MCP-capable tool) at
`POST /mcp`: search over transcripts, summaries, chapters and in-depth analyses,
plus reading one video or its transcript. Read-only.

## Protocol

- Stateless Streamable HTTP, one endpoint, JSON responses only (no SSE stream, no
  session). `GET`/`DELETE` answer `405`.
- **2026-07-28** clients: `server/discover`, `tools/list`, `tools/call`. Every
  request carries `params._meta["io.modelcontextprotocol/protocolVersion"]` plus the
  `MCP-Protocol-Version`, `Mcp-Method` and (for `tools/call`) `Mcp-Name` headers
  (`=?base64?…?=` values are decoded). Mismatch → `400` / `-32020`; unsupported
  version → `400` / `-32022` with `data.supported`; unknown method → `404` / `-32601`.
  Results carry `resultType: "complete"` and `_meta["io.modelcontextprotocol/serverInfo"]`;
  `server/discover` and `tools/list` carry `ttlMs: 900000`, `cacheScope: "private"`.
- **Older clients** using `initialize` (2025-03-26, 2025-06-18, 2025-11-25) are
  answered without a session id; `notifications/initialized` → `202`, `ping` → `{}`.
- Unknown tool → `-32602` on HTTP `200`. A tool that fails or gets bad arguments
  answers a normal result with `isError: true` and a readable message.
- Request body limit 1 MiB. Each tool's text is capped at 24 KB; a tool call is
  bounded at 25 s.
- A request with an `Origin` header other than peeq's public origin (`https://<PUBLIC_FQDN>`; `http://<BACKEND_ADDR>` in dev mode) → `403`.
  Server-to-server calls send none.

## Token

`BACKEND_MCP_TOKEN` is the bearer secret. Unset → `/mcp` is not mounted (`404`). Links in results are absolute, built on peeq's public URL (oidc mode refuses to boot without `PUBLIC_FQDN`).
It is deliberately **not** the extension's API token from Settings: that token
may only write a cookie, this one reads the library.

```sh
openssl rand -base64 32
```

Put the value in peeq's `.env` as `BACKEND_MCP_TOKEN=…` and the same value in
loom's `.env` as `PEEQ_MCP_TOKEN=…`, then restart both. Missing or wrong token →
`401` with `WWW-Authenticate: Bearer`.

## loom

`conf/mcp.json`:

```json
{
  "mcpServers": {
    "peeq": {
      "type": "http",
      "url": "${PEEQ_MCP_URL}",
      "headers": { "Authorization": "Bearer ${PEEQ_MCP_TOKEN}" },
      "users": "${BACKEND_PEEQ_USERS}",
      "trustInstructions": true
    }
  }
}
```

with `PEEQ_MCP_URL=https://peeq.example.com/mcp` in loom's `.env`, and
`BACKEND_PEEQ_USERS` set to the loom accounts that may read this library: their
loom user id (the `id` loom's `GET /api/me` returns), comma separated.
Usernames and OIDC subjects do not count, and an empty value means nobody.
`trustInstructions` lets peeq's channel hint into loom's system prompt, which is
what makes loom search peeq unprompted on those channels' topics.

## Authelia

peeq uses Authelia as its OIDC identity provider only; its Traefik router has no
forward-auth middleware, so a bearer request to `/mcp` reaches peeq untouched and
needs no Authelia change. If forward-auth is ever added to that router, add an
`access_control` rule above the `group:Peeq` rule:

```yaml
- domain: peeq.example.com
  resources: ['^/mcp$']
  policy: bypass
```

peeq still enforces the token.

## Tools

Each tool carries a display title, sent both as `title` and `annotations.title`
(quoted below), for clients to show instead of the name. All are
`readOnlyHint: true`, `openWorldHint: false`.

- `search` ("Search your video library") — `query` (required), `channel` (name, @handle or id; case-insensitive,
  typo-tolerant, resolved against the channels list_youtube_channels shows plus any with videos; an unknown one is an error listing the closest names, a listed one with no videos yet says so), `category`
  (peeq's fixed category enum), `youtube_category` (YouTube's own label, e.g.
  "Science & Technology", "Education"; exact, case-insensitive), `subcategory`
  (a `category/subcategory` key, e.g. `ai/building`: videos labelled with it),
  `channel_subcategory` (same keys: videos from channels labelled with it, the
  only form that reaches caption-only reads; e.g. `sports/cycling` for "videos
  from cycling channels"; both enums list the whole vocabulary), `after`/`before`
  (`YYYY-MM-DD`, inclusive), `mode` (`hybrid` default: keyword + semantic fused like
  the Search page, no query-understanding model call; `keyword`: literal full-text
  match), `k` (videos, default 6, max 10). If the query cannot be embedded, hybrid
  falls back to keyword-only and says so.
- `get_video` ("Get a video's details and summaries") — `id`: metadata (peeq categories and subcategories, YouTube categories, tags), short summary,
  key points, chapters, in-depth summary, links.
- `get_transcript` ("Read a video's transcript") — `id`, optional `start_seconds`, `end_seconds` (exclusive):
  `[mm:ss] text` lines. A cut transcript ends with the `start_seconds` to request next.
- `list_youtube_channels` ("List YouTube channels in your library") — subscribed channels plus channels whose caption-only reads are
  kept: id, handle, description, video count, top peeq and YouTube categories, the
  channel's subcategories, and a header line naming every channel subcategory in use.

Subcategories come from `backend/internal/videos/subcategory.go` (fixed, every
category ends in `other`): up to 3 per video from its own category and up to 3 per
channel from any, primary first, labelled by the summarize worker's idle sweep
(inbox reads never get video labels). A channel's labels can be set by hand on its
page; the classifier never overwrites those.

### Auto-search hint

The instructions (`server/discover` and `initialize`) and the `search` description
are built per request (reused for a minute). They list every subscribed channel (at most 40, keep_reads
channels first, then by video count) under "search peeq automatically when the
user's question touches these channels' topics", each with its subcategories (or,
unlabelled, its top categories) and, while the ~3 KB budget allows, a trimmed
description. Both texts also say that the `subcategory` and `channel_subcategory`
filters exist. Ask's understand step offers `channel_subcategory` too, listing only
the keys some channel carries (a latency-bound gate; refreshed every minute). keep_reads channels are marked
"(full catalogue indexed)": peeq keeps every downloaded video's transcript and
analysis anyway; keep_reads additionally keeps videos it only read from captions.
The user can always ask explicitly ("search peeq for …", "in channel X …").

## Contract

`tools/call search` result (modern; an older client gets the same without
`resultType`/`_meta`):

```json
{"resultType": "complete",
 "content": [{"type": "text", "text": "- <title> — <channel>, 2026-05-02, category science · youtube: Science & Technology\n   summary: …\n   [12:34] transcript: …the **matched** words…\n   [03:10] chapter: …\n   summary match: …\n   peeq: https://peeq…/video/ID?t=754  youtube: https://youtu.be/ID?t=754\n- …"}],
 "structuredContent": {"sources": [{"url": "https://peeq…/video/ID?t=754", "title": "<title>",
   "label": "peeq · <channel>", "snippet": "<best match, ≤240 chars>",
   "image": "https://i.ytimg.com/vi/ID/mqdefault.jpg"}]},
 "_meta": {"io.modelcontextprotocol/serverInfo": {"name": "peeq", "version": "…"}}}
```

- Items are unnumbered (`- <title> — …`) and in `sources` order: `sources[i]` ↔ the
  i-th item. The citing client numbers them (loom prints its own `Source [n]` list). One source per video, linking the best match's
  timestamp (no `?t=` when only the summary matched). Other moments are in the text.
- `image` is YouTube's public thumbnail (peeq's own thumbnail route needs a session),
  present only when the id has YouTube's 11-character shape.
- `get_video` returns one source for the video; the other tools return text only.
- No results → a plain sentence and `"sources": []`.
- Past the 24 KB cap, trailing items are dropped whole together with their sources and
  the text ends "(N more results omitted; narrow the query)".
- A failed search (deadline, or keyword and semantic lanes both down) is `isError: true`
  "search failed (…)", never an empty result.
- `/video/<id>?t=<seconds>` is the SPA's player deep link; it opens at that moment.

## Smoke test

```sh
curl -s https://peeq.example.com/mcp \
  -H "Authorization: Bearer $BACKEND_MCP_TOKEN" -H 'Content-Type: application/json' \
  -H 'MCP-Protocol-Version: 2026-07-28' -H 'Mcp-Method: tools/call' -H 'Mcp-Name: search' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"bike geometry"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}'
```

Without the `Authorization` header the same call answers `401`.
