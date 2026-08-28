import { z } from 'zod';
import type { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { db, getMemoryRowsForHits, purgeExpiredMemory } from './db.ts';
import type { MemoryRow, MemorySearchRow } from './db.ts';
import { vectorUpsert, vectorDelete, vectorSearch, vectorsForNamespace } from './lance.ts';
import { extractFacts } from './extract.ts';
import { consolidateCluster } from './consolidate.ts';

// Hard-deletes memory rows past their TTL and drops their vectors. Called at
// the top of every discovery/read path so expired entries never surface.
export function purgeExpired(): void {
  for (const { key } of purgeExpiredMemory()) setImmediate(() => vectorDelete(key));
}

export interface HybridSearchOptions {
  query: string;
  namespace?: string;
  prefix?: string;
  limit?: number;
}

const RRF_K = 60;

// Fuses FTS keyword results and vector semantic results via Reciprocal Rank
// Fusion. Shared by the memory_search_hybrid MCP tool and the
// GET /api/memory/hybrid REST endpoint.
export async function hybridSearch({ query, namespace, prefix, limit = 10 }: HybridSearchOptions) {
  purgeExpired();
  const candidateLimit = Math.min(limit * 3, 100);

  const kwParams: (string | number)[] = [query];
  let kwSql = `SELECT m.key, m.namespace FROM memory m JOIN memory_fts f ON m.rowid = f.rowid WHERE memory_fts MATCH ? AND m.archived = 0`;
  if (namespace) { kwSql += ' AND m.namespace = ?'; kwParams.push(namespace); }
  if (prefix) { kwSql += ' AND m.key LIKE ?'; kwParams.push(`${prefix}%`); }
  kwSql += ' LIMIT ?'; kwParams.push(candidateLimit);
  const keywordRows = db.prepare(kwSql).all(...kwParams) as unknown as { key: string; namespace: string }[];

  let vectorHits = await vectorSearch(query, namespace, prefix ? candidateLimit * 4 : candidateLimit);
  if (prefix) vectorHits = vectorHits.filter(h => h.key.startsWith(prefix)).slice(0, candidateLimit);

  const scores = new Map<string, number>();
  const idOf = (item: { key: string; namespace: string }) => JSON.stringify([item.namespace, item.key]);
  const addRanked = (list: { key: string; namespace: string }[]) => {
    list.forEach((item, i) => {
      const id = idOf(item);
      scores.set(id, (scores.get(id) ?? 0) + 1 / (RRF_K + i + 1));
    });
  };
  addRanked(keywordRows);
  addRanked(vectorHits);

  const ranked = [...scores.entries()]
    .sort((a, b) => b[1] - a[1])
    .slice(0, limit)
    .map(([id, score]) => {
      const [ns, key] = JSON.parse(id) as [string, string];
      return { key, namespace: ns, _score: score };
    });

  const rows = getMemoryRowsForHits(ranked);
  return ranked.map((r, i) => {
    const row = rows[i];
    // Defensive: vector hits should already be archived-clean (consolidation
    // vector-deletes archived originals), but don't rely solely on that —
    // vectorDelete is fire-and-forget and can silently fail.
    if (!row || row.archived) return null;
    return { ...row, tags: JSON.parse(row.tags), _score: r._score };
  }).filter(Boolean);
}

export interface ConsolidateOptions {
  namespace?: string;
  similarity_threshold?: number;
  min_cluster_size?: number;
}

class UnionFind {
  parent: number[];
  constructor(n: number) { this.parent = Array.from({ length: n }, (_, i) => i); }
  find(x: number): number { return this.parent[x] === x ? x : (this.parent[x] = this.find(this.parent[x])); }
  union(a: number, b: number) { const ra = this.find(a), rb = this.find(b); if (ra !== rb) this.parent[ra] = rb; }
}

function dot(a: number[], b: number[]): number {
  let sum = 0;
  for (let i = 0; i < a.length; i++) sum += a[i] * b[i];
  return sum;
}

// Clusters a namespace's memories by embedding similarity and uses Haiku to
// merge each cluster into one fact, archiving the rest (soft delete — still
// fetchable by exact key via memory_get). Shared by the memory_consolidate
// MCP tool and the POST /api/memory/consolidate REST endpoint.
export async function consolidateNamespace({ namespace = 'default', similarity_threshold = 0.85, min_cluster_size = 2 }: ConsolidateOptions) {
  purgeExpired();

  const liveKeys = new Set(
    (db.prepare('SELECT key FROM memory WHERE namespace = ? AND archived = 0').all(namespace) as { key: string }[]).map(r => r.key)
  );
  const vectors = (await vectorsForNamespace(namespace)).filter(v => liveKeys.has(v.key));

  const uf = new UnionFind(vectors.length);
  for (let i = 0; i < vectors.length; i++) {
    for (let j = i + 1; j < vectors.length; j++) {
      if (dot(vectors[i].vector, vectors[j].vector) >= similarity_threshold) uf.union(i, j);
    }
  }

  const groups = new Map<number, number[]>();
  for (let i = 0; i < vectors.length; i++) {
    const root = uf.find(i);
    const group = groups.get(root);
    if (group) group.push(i); else groups.set(root, [i]);
  }

  const clusters = [...groups.values()].filter(g => g.length >= min_cluster_size);
  const consolidated: { key: string; value: string; tags: string[]; merged_from: string[] }[] = [];
  let entriesArchived = 0;

  for (const cluster of clusters) {
    const keys = cluster.map(i => ({ key: vectors[i].key, namespace }));
    const rows = (getMemoryRowsForHits(keys).filter(Boolean) as MemoryRow[])
      .sort((a, b) => b.updated_at - a.updated_at);
    if (rows.length < min_cluster_size) continue;

    const [survivor, ...rest] = rows;
    const { value } = await consolidateCluster(rows.map(r => ({ key: r.key, value: r.value, updated_at: r.updated_at })));
    const tags = [...new Set(rows.flatMap(r => JSON.parse(r.tags) as string[]).concat('consolidated'))];
    const now = Date.now();

    db.prepare('UPDATE memory SET value=?, tags=?, source=?, updated_at=? WHERE key=? AND namespace=?')
      .run(value, JSON.stringify(tags), 'consolidated', now, survivor.key, namespace);
    setImmediate(() => vectorUpsert(survivor.key, namespace, value));

    for (const r of rest) {
      db.prepare('UPDATE memory SET archived=1, updated_at=? WHERE key=? AND namespace=?').run(now, r.key, namespace);
      setImmediate(() => vectorDelete(r.key));
    }
    entriesArchived += rest.length;

    consolidated.push({ key: survivor.key, value, tags, merged_from: rows.map(r => r.key) });
  }

  return { namespace, clusters_found: consolidated.length, entries_archived: entriesArchived, consolidated };
}

export function registerMemoryTools(server: McpServer) {
  server.registerTool(
    'memory_set',
    { description: 'Store or update a memory entry. Also indexes for semantic search.', inputSchema: {
      key: z.string().describe('Dot-namespaced key, e.g. "decision.api-style"'),
      value: z.string(),
      tags: z.array(z.string()).optional(),
      namespace: z.string().optional(),
      source: z.string().optional().describe('Who wrote this: agent name, "manual", "extracted". Defaults to the connecting MCP client\'s name if omitted.'),
      search_text: z.string().optional().describe('Override text used for semantic indexing'),
      ttl_seconds: z.number().int().positive().optional().describe('If set, the entry auto-expires after this many seconds. Omit on update to clear a previous TTL.'),
    } },
    async ({ key, value, tags = [], namespace = 'default', source, search_text, ttl_seconds }) => {
      const now = Date.now();
      const tagsJson = JSON.stringify(tags);
      const effectiveSource = source ?? server.server.getClientVersion()?.name ?? null;
      const expiresAt = ttl_seconds ? now + ttl_seconds * 1000 : null;
      const existing = db.prepare('SELECT created_at FROM memory WHERE key = ? AND namespace = ?').get(key, namespace);

      if (existing) {
        db.prepare(
          'UPDATE memory SET value=?, tags=?, source=?, search_text=?, expires_at=?, updated_at=? WHERE key=? AND namespace=?'
        ).run(value, tagsJson, effectiveSource, search_text ?? null, expiresAt, now, key, namespace);
      } else {
        db.prepare(
          'INSERT INTO memory(key, value, tags, namespace, source, search_text, expires_at, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)'
        ).run(key, value, tagsJson, namespace, effectiveSource, search_text ?? null, expiresAt, now, now);
      }

      setImmediate(() => vectorUpsert(key, namespace, search_text ?? value));
      return { content: [{ type: 'text' as const, text: JSON.stringify({ ok: true, key, namespace, updated: !!existing }) }] };
    }
  );

  server.registerTool(
    'memory_get',
    { description: 'Retrieve a memory entry by exact key.', inputSchema: {
      key: z.string(),
      namespace: z.string().optional(),
    } },
    async ({ key, namespace = 'default' }) => {
      purgeExpired();
      // Exact-key lookup intentionally ignores `archived` — a consolidated-away
      // entry should still be directly fetchable by key.
      const row = db.prepare('SELECT * FROM memory WHERE key = ? AND namespace = ?').get(key, namespace) as MemoryRow | undefined;
      if (!row) return { content: [{ type: 'text' as const, text: 'null' }] };
      return { content: [{ type: 'text' as const, text: JSON.stringify({ ...row, tags: JSON.parse(row.tags) }) }] };
    }
  );

  server.registerTool(
    'memory_search',
    { description: 'Full-text keyword search across memory entries (fast, exact matching).', inputSchema: {
      query: z.string(),
      namespace: z.string().optional(),
      prefix: z.string().optional().describe('Only match keys starting with this prefix, e.g. "decision."'),
      limit: z.number().int().positive().optional(),
    } },
    async ({ query, namespace, prefix, limit = 20 }) => {
      purgeExpired();
      const params: (string | number)[] = [query];
      let sql = `
        SELECT m.key, m.value, m.tags, m.namespace, m.source, m.updated_at,
               snippet(memory_fts, 1, '[', ']', '...', 20) AS snippet
        FROM memory m
        JOIN memory_fts f ON m.rowid = f.rowid
        WHERE memory_fts MATCH ? AND m.archived = 0`;
      if (namespace) { sql += ' AND m.namespace = ?'; params.push(namespace); }
      if (prefix) { sql += ' AND m.key LIKE ?'; params.push(`${prefix}%`); }
      sql += ' LIMIT ?';
      params.push(limit);

      const rows = db.prepare(sql).all(...params) as unknown as MemorySearchRow[];
      const results = rows.map(r => ({ ...r, tags: JSON.parse(r.tags) }));
      return { content: [{ type: 'text' as const, text: JSON.stringify(results) }] };
    }
  );

  server.registerTool(
    'memory_search_semantic',
    { description: 'Vector similarity search — finds related entries even without keyword overlap.', inputSchema: {
      query: z.string(),
      namespace: z.string().optional(),
      prefix: z.string().optional().describe('Only match keys starting with this prefix, e.g. "decision."'),
      limit: z.number().int().positive().optional(),
    } },
    async ({ query, namespace, prefix, limit = 10 }) => {
      purgeExpired();
      let hits = await vectorSearch(query, namespace, prefix ? Math.min(limit * 4, 100) : limit);
      if (prefix) hits = hits.filter(h => h.key.startsWith(prefix)).slice(0, limit);
      const rows = getMemoryRowsForHits(hits);
      const results = hits.map((h, i) => {
        const row = rows[i];
        if (!row || row.archived) return null;
        return { ...row, tags: JSON.parse(row.tags), _score: h._distance };
      }).filter(Boolean);
      return { content: [{ type: 'text' as const, text: JSON.stringify(results) }] };
    }
  );

  server.registerTool(
    'memory_search_hybrid',
    { description: 'Fuses keyword and semantic search results (Reciprocal Rank Fusion) — a good default when unsure which style of search fits.', inputSchema: {
      query: z.string(),
      namespace: z.string().optional(),
      prefix: z.string().optional().describe('Only match keys starting with this prefix, e.g. "decision."'),
      limit: z.number().int().positive().optional(),
    } },
    async ({ query, namespace, prefix, limit = 10 }) => {
      const results = await hybridSearch({ query, namespace, prefix, limit });
      return { content: [{ type: 'text' as const, text: JSON.stringify(results) }] };
    }
  );

  server.registerTool(
    'memory_list',
    { description: 'List memory entries with optional filters.', inputSchema: {
      namespace: z.string().optional(),
      tag: z.string().optional(),
      prefix: z.string().optional().describe('Only match keys starting with this prefix, e.g. "decision."'),
      limit: z.number().int().positive().optional(),
      offset: z.number().int().nonnegative().optional(),
    } },
    async ({ namespace, tag, prefix, limit = 50, offset = 0 }) => {
      purgeExpired();
      let sql = 'SELECT * FROM memory WHERE archived = 0';
      const params: (string | number)[] = [];
      if (namespace) { sql += ' AND namespace = ?'; params.push(namespace); }
      if (tag) { sql += ' AND tags LIKE ?'; params.push(`%"${tag}"%`); }
      if (prefix) { sql += ' AND key LIKE ?'; params.push(`${prefix}%`); }
      sql += ' ORDER BY updated_at DESC LIMIT ? OFFSET ?';
      params.push(limit, offset);
      const rows = db.prepare(sql).all(...params) as unknown as MemoryRow[];
      return { content: [{ type: 'text' as const, text: JSON.stringify(rows.map(r => ({ ...r, tags: JSON.parse(r.tags) }))) }] };
    }
  );

  server.registerTool(
    'memory_consolidate',
    { description: 'Cluster near-duplicate/related memories in a namespace by embedding similarity and merge each cluster into one fact via Claude Haiku. Merged-away entries are archived (soft-deleted), not lost — still fetchable by exact key via memory_get. Requires ANTHROPIC_API_KEY.', inputSchema: {
      namespace: z.string().optional().describe('Defaults to "default". Scans one namespace at a time by design, to avoid accidentally merging unrelated projects/users in one call.'),
      similarity_threshold: z.number().min(0).max(1).optional().describe('Cosine similarity above which two entries are clustered together. Default 0.85 (conservative — only near-duplicates).'),
      min_cluster_size: z.number().int().min(2).optional().describe('Minimum cluster size to merge. Default 2.'),
    } },
    async ({ namespace, similarity_threshold, min_cluster_size }) => {
      const result = await consolidateNamespace({ namespace, similarity_threshold, min_cluster_size });
      return { content: [{ type: 'text' as const, text: JSON.stringify(result) }] };
    }
  );

  server.registerTool(
    'memory_delete',
    { description: 'Remove a memory entry by key.', inputSchema: {
      key: z.string(),
      namespace: z.string().optional(),
    } },
    async ({ key, namespace = 'default' }) => {
      const result = db.prepare('DELETE FROM memory WHERE key = ? AND namespace = ?').run(key, namespace);
      setImmediate(() => vectorDelete(key));
      return { content: [{ type: 'text' as const, text: JSON.stringify({ deleted: result.changes > 0 }) }] };
    }
  );

  server.registerTool(
    'memory_extract',
    { description: 'Use Claude Haiku to extract structured facts from raw text and store them as memories. Requires ANTHROPIC_API_KEY.', inputSchema: {
      text: z.string().describe('Raw text to extract facts from (conversation, note, document, etc.)'),
      namespace: z.string().optional(),
      context: z.string().optional().describe('Optional hint about what this text is about'),
    } },
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
