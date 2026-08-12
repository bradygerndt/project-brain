import { z } from 'zod';
import type { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { db } from './db.ts';
import type { MemoryRow, MemorySearchRow } from './db.ts';
import { vectorUpsert, vectorDelete, vectorSearch } from './lance.ts';
import { extractFacts } from './extract.ts';

export function registerMemoryTools(server: McpServer) {
  server.tool(
    'memory_set',
    'Store or update a memory entry. Also indexes for semantic search.',
    {
      key: z.string().describe('Dot-namespaced key, e.g. "decision.api-style"'),
      value: z.string(),
      tags: z.array(z.string()).optional(),
      namespace: z.string().optional(),
      source: z.string().optional().describe('Who wrote this: agent name, "manual", "extracted"'),
      search_text: z.string().optional().describe('Override text used for semantic indexing'),
    },
    async ({ key, value, tags = [], namespace = 'default', source, search_text }) => {
      const now = Date.now();
      const tagsJson = JSON.stringify(tags);
      const existing = db.prepare('SELECT created_at FROM memory WHERE key = ? AND namespace = ?').get(key, namespace);

      if (existing) {
        db.prepare(
          'UPDATE memory SET value=?, tags=?, source=?, search_text=?, updated_at=? WHERE key=? AND namespace=?'
        ).run(value, tagsJson, source ?? null, search_text ?? null, now, key, namespace);
      } else {
        db.prepare(
          'INSERT INTO memory(key, value, tags, namespace, source, search_text, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)'
        ).run(key, value, tagsJson, namespace, source ?? null, search_text ?? null, now, now);
      }

      setImmediate(() => vectorUpsert(key, namespace, search_text ?? value));
      return { content: [{ type: 'text' as const, text: JSON.stringify({ ok: true, key, namespace, updated: !!existing }) }] };
    }
  );

  server.tool(
    'memory_get',
    'Retrieve a memory entry by exact key.',
    {
      key: z.string(),
      namespace: z.string().optional(),
    },
    async ({ key, namespace = 'default' }) => {
      const row = db.prepare('SELECT * FROM memory WHERE key = ? AND namespace = ?').get(key, namespace) as MemoryRow | undefined;
      if (!row) return { content: [{ type: 'text' as const, text: 'null' }] };
      return { content: [{ type: 'text' as const, text: JSON.stringify({ ...row, tags: JSON.parse(row.tags) }) }] };
    }
  );

  server.tool(
    'memory_search',
    'Full-text keyword search across memory entries (fast, exact matching).',
    {
      query: z.string(),
      namespace: z.string().optional(),
      limit: z.number().int().positive().optional(),
    },
    async ({ query, namespace, limit = 20 }) => {
      const params: (string | number)[] = [query];
      let sql = `
        SELECT m.key, m.value, m.tags, m.namespace, m.source, m.updated_at,
               snippet(memory_fts, 1, '[', ']', '...', 20) AS snippet
        FROM memory m
        JOIN memory_fts f ON m.rowid = f.rowid
        WHERE memory_fts MATCH ?`;
      if (namespace) { sql += ' AND m.namespace = ?'; params.push(namespace); }
      sql += ' LIMIT ?';
      params.push(limit);

      const rows = db.prepare(sql).all(...params) as unknown as MemorySearchRow[];
      const results = rows.map(r => ({ ...r, tags: JSON.parse(r.tags) }));
      return { content: [{ type: 'text' as const, text: JSON.stringify(results) }] };
    }
  );

  server.tool(
    'memory_search_semantic',
    'Vector similarity search — finds related entries even without keyword overlap.',
    {
      query: z.string(),
      namespace: z.string().optional(),
      limit: z.number().int().positive().optional(),
    },
    async ({ query, namespace, limit = 10 }) => {
      const hits = await vectorSearch(query, namespace, limit);
      const results = hits.map(h => {
        const row = db.prepare('SELECT * FROM memory WHERE key = ? AND namespace = ?').get(h.key, h.namespace ?? namespace ?? 'default') as MemoryRow | undefined;
        if (!row) return null;
        return { ...row, tags: JSON.parse(row.tags), _score: h._distance };
      }).filter(Boolean);
      return { content: [{ type: 'text' as const, text: JSON.stringify(results) }] };
    }
  );

  server.tool(
    'memory_list',
    'List memory entries with optional filters.',
    {
      namespace: z.string().optional(),
      tag: z.string().optional(),
      limit: z.number().int().positive().optional(),
      offset: z.number().int().nonnegative().optional(),
    },
    async ({ namespace, tag, limit = 50, offset = 0 }) => {
      let sql = 'SELECT * FROM memory WHERE 1=1';
      const params: (string | number)[] = [];
      if (namespace) { sql += ' AND namespace = ?'; params.push(namespace); }
      if (tag) { sql += ' AND tags LIKE ?'; params.push(`%"${tag}"%`); }
      sql += ' ORDER BY updated_at DESC LIMIT ? OFFSET ?';
      params.push(limit, offset);
      const rows = db.prepare(sql).all(...params) as unknown as MemoryRow[];
      return { content: [{ type: 'text' as const, text: JSON.stringify(rows.map(r => ({ ...r, tags: JSON.parse(r.tags) }))) }] };
    }
  );

  server.tool(
    'memory_delete',
    'Remove a memory entry by key.',
    {
      key: z.string(),
      namespace: z.string().optional(),
    },
    async ({ key, namespace = 'default' }) => {
      const result = db.prepare('DELETE FROM memory WHERE key = ? AND namespace = ?').run(key, namespace);
      setImmediate(() => vectorDelete(key));
      return { content: [{ type: 'text' as const, text: JSON.stringify({ deleted: result.changes > 0 }) }] };
    }
  );

  server.tool(
    'memory_extract',
    'Use Claude Haiku to extract structured facts from raw text and store them as memories. Requires ANTHROPIC_API_KEY.',
    {
      text: z.string().describe('Raw text to extract facts from (conversation, note, document, etc.)'),
      namespace: z.string().optional(),
      context: z.string().optional().describe('Optional hint about what this text is about'),
    },
    async ({ text, namespace = 'default', context }) => {
      const facts = await extractFacts(text, context);
      const now = Date.now();
      const stored: { key: string; value: string; tags: string[] }[] = [];

      for (const { key, value, tags = [] } of facts) {
        const tagsJson = JSON.stringify(tags);
        const existing = db.prepare('SELECT created_at FROM memory WHERE key = ? AND namespace = ?').get(key, namespace);
        if (existing) {
          db.prepare('UPDATE memory SET value=?, tags=?, source=?, updated_at=? WHERE key=? AND namespace=?')
            .run(value, tagsJson, 'extracted', now, key, namespace);
        } else {
          db.prepare('INSERT INTO memory(key, value, tags, namespace, source, created_at, updated_at) VALUES(?,?,?,?,?,?,?)')
            .run(key, value, tagsJson, namespace, 'extracted', now, now);
        }
        setImmediate(() => vectorUpsert(key, namespace, value));
        stored.push({ key, value, tags });
      }

      return { content: [{ type: 'text' as const, text: JSON.stringify({ extracted: stored.length, facts: stored }) }] };
    }
  );
}
