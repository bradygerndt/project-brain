import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { isInitializeRequest } from '@modelcontextprotocol/sdk/types.js';
import express, { type Request, type Response } from 'express';
import { db, getMemoryRowsForHits } from './db.ts';
import type { MemoryRow, MemorySearchRow, ArtifactRow, AgentRow, LockRow } from './db.ts';
import { artifactsDir, resolveHost, createArtifact, deleteArtifact } from './artifacts.ts';
import { registerMemoryTools } from './memory.ts';
import { registerArtifactTools } from './artifacts.ts';
import { registerAgentTools } from './agents.ts';

const BRAIN_NAME = process.env.BRAIN_NAME ?? 'default';
const MCP_PORT = parseInt(process.env.MCP_PORT ?? '3579', 10);
const ARTIFACTS_PORT = parseInt(process.env.ARTIFACTS_PORT ?? '3580', 10);

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// Surfaced to clients via the initialize handshake — covers the strategy
// gaps individual tool schemas don't (when to use which tool, namespacing
// convention), not per-field format, which each tool's own parameter
// descriptions already carry.
const SERVER_INSTRUCTIONS = `project-brain is a persistent memory service for this project or user, shared across sessions and agents.

Facts are stored under dot-namespaced keys (e.g. "user.role", "project.deadline", "decision.api-style") — reuse an existing key to update a fact rather than creating a near-duplicate under a slightly different name. Use memory_set for a single fact you've already identified; use memory_extract for a large blob of raw text (a transcript, notes, a document) you want decomposed into multiple facts automatically — it makes its own call to Claude Haiku server-side and requires ANTHROPIC_API_KEY, so fall back to memory_set if it's unavailable.

For search: memory_search (keyword/full-text) is fast and exact — use it when you know the term. memory_search_semantic (vector similarity) finds conceptually related entries even without exact keyword overlap — use it when you're unsure of the exact phrasing, or want to check for anything related before writing a new fact.

namespace (default: "default") partitions memories — use a distinct namespace per project or user if this instance is shared across contexts that shouldn't mix.

Agent presence (agent_ping/agent_list) and resource locks (lock_acquire/lock_release) are opt-in — nothing is tracked automatically just by connecting. Call agent_ping if you want other agents to see you're active.`;

// --- MCP server factory (one per session) ---

function makeMcpServer(): McpServer {
  const server = new McpServer(
    { name: `project-brain-${BRAIN_NAME}`, version: '1.0.0' },
    { capabilities: { logging: {} }, instructions: SERVER_INSTRUCTIONS }
  );
  registerMemoryTools(server);
  registerArtifactTools(server);
  registerAgentTools(server);

  server.registerTool(
    'ui_url',
    { description: 'Get the HTTP URL for this brain instance\'s web UI (browse and search memory, artifacts, and agent presence).', inputSchema: {} },
    async () => ({ content: [{ type: 'text' as const, text: JSON.stringify({ url: `http://${resolveHost()}:${MCP_PORT}/ui` }) }] })
  );

  return server;
}

// --- Main Express app: MCP + REST API + UI ---

const app = express();
app.use(express.json({ limit: '50mb' }));

const transports = new Map<string, StreamableHTTPServerTransport>();

app.post('/mcp', async (req: Request, res: Response) => {
  try {
    const sessionId = req.headers['mcp-session-id'] as string | undefined;
    let transport = sessionId ? transports.get(sessionId) : undefined;

    if (!transport) {
      if (!isInitializeRequest(req.body)) {
        res.status(400).json({ error: 'No session. Send an initialize request first.' });
        return;
      }
      const id = randomUUID();
      transport = new StreamableHTTPServerTransport({ sessionIdGenerator: () => id });
      transport.onclose = () => transports.delete(id);
      transports.set(id, transport);
      const mcpServer = makeMcpServer();
      await mcpServer.connect(transport);
    }

    await transport.handleRequest(req, res, req.body);
  } catch (err) {
    console.error('[mcp] POST error:', err);
    if (!res.headersSent) res.status(500).json({ error: errorMessage(err) });
  }
});

app.get('/mcp', async (req: Request, res: Response) => {
  const sessionId = req.headers['mcp-session-id'] as string | undefined;
  const transport = sessionId ? transports.get(sessionId) : undefined;
  if (!transport) { res.status(404).json({ error: 'Session not found' }); return; }
  try {
    await transport.handleRequest(req, res);
  } catch (err) {
    console.error('[mcp] GET error:', err);
  }
});

app.delete('/mcp', async (req: Request, res: Response) => {
  const sessionId = req.headers['mcp-session-id'] as string | undefined;
  const transport = sessionId ? transports.get(sessionId) : undefined;
  if (transport) {
    await transport.close().catch(() => {});
    transports.delete(sessionId!);
  }
  res.json({ ok: true });
});

// --- REST API for the web UI ---

app.get('/health', (_req: Request, res: Response) => {
  res.json({ ok: true, brain: BRAIN_NAME, sessions: transports.size });
});

app.get('/api/memory', (req: Request, res: Response) => {
  try {
    const { q, type = 'keyword', ns, limit = '20' } = req.query;
    if (!q) { res.json([]); return; }
    const lim = Math.min(parseInt(String(limit), 10) || 20, 100);

    if (type === 'keyword') {
      const params: (string | number)[] = [String(q)];
      let sql = `SELECT m.key, m.value, m.tags, m.namespace, m.source, m.updated_at,
                        snippet(memory_fts, 1, '[', ']', '...', 20) AS snippet
                 FROM memory m JOIN memory_fts f ON m.rowid = f.rowid
                 WHERE memory_fts MATCH ?`;
      if (ns) { sql += ' AND m.namespace = ?'; params.push(String(ns)); }
      sql += ' LIMIT ?'; params.push(lim);
      const rows = db.prepare(sql).all(...params) as unknown as MemorySearchRow[];
      res.json(rows.map(r => ({ ...r, tags: JSON.parse(r.tags) })));
    } else {
      // semantic search is async — handled via a dedicated endpoint
      res.status(400).json({ error: 'Use /api/memory/semantic for semantic search' });
    }
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

app.get('/api/memory/semantic', async (req: Request, res: Response) => {
  try {
    const { q, ns, limit = '10' } = req.query;
    if (!q) { res.json([]); return; }
    const lim = Math.min(parseInt(String(limit), 10) || 10, 50);
    const { vectorSearch } = await import('./lance.ts');
    const hits = await vectorSearch(String(q), ns ? String(ns) : undefined, lim);
    const rows = getMemoryRowsForHits(hits);
    const results = hits.map((h, i) => {
      const row = rows[i];
      if (!row) return null;
      return { ...row, tags: JSON.parse(row.tags), _score: h._distance };
    }).filter(Boolean);
    res.json(results);
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

app.get('/api/memory/list', (req: Request, res: Response) => {
  try {
    const { ns, tag, limit = '50', offset = '0' } = req.query;
    let sql = 'SELECT * FROM memory WHERE 1=1';
    const params: (string | number)[] = [];
    if (ns) { sql += ' AND namespace = ?'; params.push(String(ns)); }
    if (tag) { sql += ' AND tags LIKE ?'; params.push(`%"${String(tag)}"%`); }
    sql += ' ORDER BY updated_at DESC LIMIT ? OFFSET ?';
    params.push(Math.min(parseInt(String(limit), 10) || 50, 200), parseInt(String(offset), 10) || 0);
    const rows = db.prepare(sql).all(...params) as unknown as MemoryRow[];
    res.json(rows.map(r => ({ ...r, tags: JSON.parse(r.tags) })));
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

app.get('/api/artifacts', (req: Request, res: Response) => {
  try {
    const { tag, limit = '50', offset = '0' } = req.query;
    const APORT = process.env.ARTIFACTS_PORT ?? '3580';
    let sql = 'SELECT * FROM artifacts WHERE 1=1';
    const params: (string | number)[] = [];
    if (tag) { sql += ' AND tags LIKE ?'; params.push(`%"${String(tag)}"%`); }
    sql += ' ORDER BY created_at DESC LIMIT ? OFFSET ?';
    params.push(Math.min(parseInt(String(limit), 10) || 50, 200), parseInt(String(offset), 10) || 0);
    const rows = db.prepare(sql).all(...params) as unknown as ArtifactRow[];
    const getHost = () => process.env.ARTIFACTS_HOST ?? '127.0.0.1';
    res.json(rows.map(r => ({
      ...r,
      tags: JSON.parse(r.tags),
      url: `http://${getHost()}:${APORT}/artifacts/${r.id}/${r.filename}`
    })));
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

app.post('/api/memory', (req: Request, res: Response) => {
  try {
    const { key, value, tags = [], namespace = 'default', source } = req.body;
    if (!key || !value) { res.status(400).json({ error: 'key and value are required' }); return; }
    const now = Date.now();
    const tagsJson = JSON.stringify(tags);
    const existing = db.prepare('SELECT created_at FROM memory WHERE key = ? AND namespace = ?').get(key, namespace);
    if (existing) {
      db.prepare('UPDATE memory SET value=?, tags=?, source=?, updated_at=? WHERE key=? AND namespace=?')
        .run(value, tagsJson, source ?? 'manual', now, key, namespace);
    } else {
      db.prepare('INSERT INTO memory(key, value, tags, namespace, source, created_at, updated_at) VALUES(?,?,?,?,?,?,?)')
        .run(key, value, tagsJson, namespace, source ?? 'manual', now, now);
    }
    setImmediate(async () => { const { vectorUpsert } = await import('./lance.ts'); vectorUpsert(key, namespace, value); });
    res.json({ ok: true });
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

app.delete('/api/memory', (req: Request, res: Response) => {
  try {
    const { key, namespace = 'default' } = req.query;
    if (!key) { res.status(400).json({ error: 'key is required' }); return; }
    const result = db.prepare('DELETE FROM memory WHERE key = ? AND namespace = ?').run(String(key), String(namespace));
    setImmediate(async () => { const { vectorDelete } = await import('./lance.ts'); vectorDelete(String(key)); });
    res.json({ deleted: result.changes > 0 });
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

app.post('/api/artifacts', (req: Request, res: Response) => {
  try {
    const { name, content, encoding, mime_type, tags } = req.body;
    if (!name || !content) { res.status(400).json({ error: 'name and content are required' }); return; }
    const result = createArtifact({ name, content, encoding, mime_type, tags });
    res.json(result);
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

app.delete('/api/artifacts/:id', (req: Request, res: Response) => {
  try {
    const deleted = deleteArtifact(String(req.params.id));
    res.json({ deleted });
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

app.get('/api/agents', (_req: Request, res: Response) => {
  try {
    const now = Date.now();
    db.prepare('DELETE FROM agents WHERE expires_at <= ?').run(now);
    res.json(db.prepare('SELECT * FROM agents ORDER BY updated_at DESC').all() as unknown as AgentRow[]);
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

app.get('/api/locks', (_req: Request, res: Response) => {
  try {
    const now = Date.now();
    db.prepare('DELETE FROM locks WHERE expires_at <= ?').run(now);
    res.json(db.prepare('SELECT * FROM locks ORDER BY acquired_at DESC').all() as unknown as LockRow[]);
  } catch (err) {
    res.status(500).json({ error: errorMessage(err) });
  }
});

// Serve the web UI
const uiPath = resolve(import.meta.dirname, 'ui', 'index.html');
app.get('/ui', (_req: Request, res: Response) => res.sendFile(uiPath));
app.get('/', (_req: Request, res: Response) => res.redirect('/ui'));

app.listen(MCP_PORT, '0.0.0.0', () => {
  console.log(`[brain:${BRAIN_NAME}] MCP server → http://0.0.0.0:${MCP_PORT}/mcp`);
  console.log(`[brain:${BRAIN_NAME}] Web UI     → http://0.0.0.0:${MCP_PORT}/ui`);
});

// --- Artifacts static server ---

const staticApp = express();
staticApp.use('/artifacts', express.static(artifactsDir));
staticApp.listen(ARTIFACTS_PORT, '0.0.0.0', () => {
  console.log(`[brain:${BRAIN_NAME}] Artifacts  → http://0.0.0.0:${ARTIFACTS_PORT}/artifacts`);
});

// --- Graceful shutdown ---

async function shutdown() {
  for (const [id, t] of transports) {
    await t.close().catch(() => {});
    transports.delete(id);
  }
  process.exit(0);
}
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);
