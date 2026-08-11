import { randomUUID } from 'node:crypto';
import { createServer } from 'node:http';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { isInitializeRequest } from '@modelcontextprotocol/sdk/types.js';
import express from 'express';
import { db } from './db.js';
import { artifactsDir } from './artifacts.js';
import { registerMemoryTools } from './memory.js';
import { registerArtifactTools } from './artifacts.js';
import { registerAgentTools } from './agents.js';

const BRAIN_NAME = process.env.BRAIN_NAME ?? 'default';
const MCP_PORT = parseInt(process.env.MCP_PORT ?? '3579', 10);
const ARTIFACTS_PORT = parseInt(process.env.ARTIFACTS_PORT ?? '3580', 10);

// --- MCP server factory (one per session) ---

function makeMcpServer() {
  const server = new McpServer(
    { name: `project-brain-${BRAIN_NAME}`, version: '1.0.0' },
    { capabilities: { logging: {} } }
  );
  registerMemoryTools(server);
  registerArtifactTools(server);
  registerAgentTools(server);
  return server;
}

// --- Main Express app: MCP + REST API + UI ---

const app = express();
app.use(express.json({ limit: '50mb' }));

const transports = new Map(); // sessionId -> StreamableHTTPServerTransport

app.post('/mcp', async (req, res) => {
  try {
    const sessionId = req.headers['mcp-session-id'];
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
    if (!res.headersSent) res.status(500).json({ error: err.message });
  }
});

app.get('/mcp', async (req, res) => {
  const sessionId = req.headers['mcp-session-id'];
  const transport = transports.get(sessionId);
  if (!transport) { res.status(404).json({ error: 'Session not found' }); return; }
  try {
    await transport.handleRequest(req, res);
  } catch (err) {
    console.error('[mcp] GET error:', err);
  }
});

app.delete('/mcp', async (req, res) => {
  const sessionId = req.headers['mcp-session-id'];
  const transport = transports.get(sessionId);
  if (transport) {
    await transport.close().catch(() => {});
    transports.delete(sessionId);
  }
  res.json({ ok: true });
});

// --- REST API for the web UI ---

app.get('/health', (_req, res) => {
  res.json({ ok: true, brain: BRAIN_NAME, sessions: transports.size });
});

app.get('/api/memory', (req, res) => {
  try {
    const { q, type = 'keyword', ns, limit = '20' } = req.query;
    if (!q) { res.json([]); return; }
    const lim = Math.min(parseInt(limit, 10) || 20, 100);

    if (type === 'keyword') {
      const params = [String(q)];
      let sql = `SELECT m.key, m.value, m.tags, m.namespace, m.source, m.updated_at,
                        snippet(memory_fts, 1, '[', ']', '...', 20) AS snippet
                 FROM memory m JOIN memory_fts f ON m.rowid = f.rowid
                 WHERE memory_fts MATCH ?`;
      if (ns) { sql += ' AND m.namespace = ?'; params.push(String(ns)); }
      sql += ' LIMIT ?'; params.push(lim);
      const rows = db.prepare(sql).all(...params);
      res.json(rows.map(r => ({ ...r, tags: JSON.parse(r.tags) })));
    } else {
      // semantic search is async — handled via a dedicated endpoint
      res.status(400).json({ error: 'Use /api/memory/semantic for semantic search' });
    }
  } catch (err) {
    res.status(500).json({ error: err.message });
  }
});

app.get('/api/memory/semantic', async (req, res) => {
  try {
    const { q, ns, limit = '10' } = req.query;
    if (!q) { res.json([]); return; }
    const lim = Math.min(parseInt(limit, 10) || 10, 50);
    const { vectorSearch } = await import('./lance.js');
    const hits = await vectorSearch(String(q), ns ? String(ns) : undefined, lim);
    const results = hits.map(h => {
      const row = db.prepare('SELECT * FROM memory WHERE key = ? AND namespace = ?').get(h.key, h.namespace ?? 'default');
      if (!row) return null;
      return { ...row, tags: JSON.parse(row.tags), _score: h._distance };
    }).filter(Boolean);
    res.json(results);
  } catch (err) {
    res.status(500).json({ error: err.message });
  }
});

app.get('/api/memory/list', (req, res) => {
  try {
    const { ns, tag, limit = '50', offset = '0' } = req.query;
    let sql = 'SELECT * FROM memory WHERE 1=1';
    const params = [];
    if (ns) { sql += ' AND namespace = ?'; params.push(String(ns)); }
    if (tag) { sql += ' AND tags LIKE ?'; params.push(`%"${String(tag)}"%`); }
    sql += ' ORDER BY updated_at DESC LIMIT ? OFFSET ?';
    params.push(Math.min(parseInt(limit, 10) || 50, 200), parseInt(offset, 10) || 0);
    const rows = db.prepare(sql).all(...params);
    res.json(rows.map(r => ({ ...r, tags: JSON.parse(r.tags) })));
  } catch (err) {
    res.status(500).json({ error: err.message });
  }
});

app.get('/api/artifacts', (req, res) => {
  try {
    const { tag, limit = '50', offset = '0' } = req.query;
    const APORT = process.env.ARTIFACTS_PORT ?? '3580';
    let sql = 'SELECT * FROM artifacts WHERE 1=1';
    const params = [];
    if (tag) { sql += ' AND tags LIKE ?'; params.push(`%"${String(tag)}"%`); }
    sql += ' ORDER BY created_at DESC LIMIT ? OFFSET ?';
    params.push(Math.min(parseInt(limit, 10) || 50, 200), parseInt(offset, 10) || 0);
    const rows = db.prepare(sql).all(...params);
    const getHost = () => process.env.ARTIFACTS_HOST ?? '127.0.0.1';
    res.json(rows.map(r => ({
      ...r,
      tags: JSON.parse(r.tags),
      url: `http://${getHost()}:${APORT}/artifacts/${r.id}/${r.filename}`
    })));
  } catch (err) {
    res.status(500).json({ error: err.message });
  }
});

app.post('/api/memory', (req, res) => {
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
    setImmediate(async () => { const { vectorUpsert } = await import('./lance.js'); vectorUpsert(key, namespace, value); });
    res.json({ ok: true });
  } catch (err) {
    res.status(500).json({ error: err.message });
  }
});

app.get('/api/agents', (req, res) => {
  try {
    const now = Date.now();
    db.prepare('DELETE FROM agents WHERE expires_at <= ?').run(now);
    res.json(db.prepare('SELECT * FROM agents ORDER BY updated_at DESC').all());
  } catch (err) {
    res.status(500).json({ error: err.message });
  }
});

// Serve the web UI
const uiPath = resolve(import.meta.dirname, 'ui', 'index.html');
app.get('/ui', (_req, res) => res.sendFile(uiPath));
app.get('/', (_req, res) => res.redirect('/ui'));

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
