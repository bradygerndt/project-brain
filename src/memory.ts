import { z } from 'zod';
import type { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { db, getMemoryRowsForHits, purgeExpiredMemory, recordAccess } from './db.ts';
import type { MemoryRow, MemorySearchRow } from './db.ts';
import { vectorUpsert, vectorDelete, vectorSearch, vectorsForNamespace } from './lance.ts';

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
  const results = ranked.map((r, i) => {
    const row = rows[i];
    // Defensive: vector hits should already be archived-clean (consolidation
    // vector-deletes archived originals), but don't rely solely on that —
    // vectorDelete is fire-and-forget and can silently fail.
    if (!row || row.archived) return null;
    return { ...row, tags: JSON.parse(row.tags), _score: r._score };
  }).filter((r): r is NonNullable<typeof r> => r !== null);
  recordAccess(results.map(r => ({ key: r.key, namespace: r.namespace })));
  return results;
}

export interface FindClustersOptions {
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

// Clusters a namespace's live (non-archived) memories by embedding
// similarity — read-only, no merging or writing. The caller (an LLM agent)
// reads the returned entries, decides how to merge them, and writes the
// result back itself via memory_set + memory_archive.
export async function findClusters({ namespace = 'default', similarity_threshold = 0.85, min_cluster_size = 2 }: FindClustersOptions) {
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

  const clusters = [...groups.values()]
    .filter(g => g.length >= min_cluster_size)
    .map(group => {
      const keys = group.map(i => ({ key: vectors[i].key, namespace }));
      const rows = (getMemoryRowsForHits(keys).filter(Boolean) as MemoryRow[])
        .sort((a, b) => b.updated_at - a.updated_at)
        .map(r => ({ key: r.key, value: r.value, tags: JSON.parse(r.tags) as string[], updated_at: r.updated_at, access_count: r.access_count, last_accessed_at: r.last_accessed_at }));
      return { keys: rows.map(r => r.key), entries: rows };
    });

  return { namespace, clusters };
}

export interface ArchiveMemoryOptions {
  key: string;
  namespace?: string;
}

// Soft-deletes an entry: still fetchable by exact key via memory_get, but
// excluded from memory_list/memory_search/memory_search_semantic/
// memory_search_hybrid and dropped from the vector index.
export function archiveMemory({ key, namespace = 'default' }: ArchiveMemoryOptions): { archived: boolean } {
  const result = db.prepare('UPDATE memory SET archived=1, updated_at=? WHERE key=? AND namespace=?')
    .run(Date.now(), key, namespace);
  if (result.changes > 0) setImmediate(() => vectorDelete(key));
  return { archived: result.changes > 0 };
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
      recordAccess([{ key, namespace }]);
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
        SELECT m.key, m.value, m.tags, m.namespace, m.source, m.updated_at, m.access_count, m.last_accessed_at,
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
      recordAccess(results.map(r => ({ key: r.key, namespace: r.namespace })));
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
      }).filter((r): r is NonNullable<typeof r> => r !== null);
      recordAccess(results.map(r => ({ key: r.key, namespace: r.namespace })));
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
    'memory_find_clusters',
    { description: 'Read-only: cluster near-duplicate/related memories in a namespace by embedding similarity and return the full entries in each cluster. Does not merge or write anything — read the entries yourself, decide how to merge them, then write the result with memory_set (on the key you want to keep) and archive the rest with memory_archive.', inputSchema: {
      namespace: z.string().optional().describe('Defaults to "default". Scans one namespace at a time by design, to avoid mixing unrelated projects/users in one call.'),
      similarity_threshold: z.number().min(0).max(1).optional().describe('Cosine similarity above which two entries are grouped together. Default 0.85 (conservative — only near-duplicates).'),
      min_cluster_size: z.number().int().min(2).optional().describe('Minimum cluster size to report. Default 2.'),
    } },
    async ({ namespace, similarity_threshold, min_cluster_size }) => {
      const result = await findClusters({ namespace, similarity_threshold, min_cluster_size });
      return { content: [{ type: 'text' as const, text: JSON.stringify(result) }] };
    }
  );

  server.registerTool(
    'memory_archive',
    { description: 'Soft-delete a memory entry: still fetchable by exact key via memory_get, but excluded from memory_list/memory_search/memory_search_semantic/memory_search_hybrid. Use this instead of memory_delete when you want to hide an entry (e.g. one merged away by memory_find_clusters) without losing it.', inputSchema: {
      key: z.string(),
      namespace: z.string().optional(),
    } },
    async ({ key, namespace }) => {
      const result = archiveMemory({ key, namespace });
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
}
