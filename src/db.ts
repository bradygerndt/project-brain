import { DatabaseSync } from 'node:sqlite';
import { mkdirSync } from 'node:fs';
import { resolve } from 'node:path';

export interface MemoryRow {
  key: string;
  value: string;
  tags: string;
  namespace: string;
  source: string | null;
  created_at: number;
  updated_at: number;
  search_text: string | null;
  archived: number;
  expires_at: number | null;
}

export interface MemorySearchRow extends MemoryRow {
  snippet: string;
}

export interface ArtifactRow {
  id: string;
  name: string;
  mime_type: string;
  filename: string;
  size_bytes: number;
  tags: string;
  created_at: number;
  updated_at: number;
}

export interface AgentRow {
  agent_id: string;
  status: string;
  focus: string | null;
  expires_at: number;
  updated_at: number;
}

export interface LockRow {
  resource: string;
  agent_id: string;
  expires_at: number;
  acquired_at: number;
}

const dataDir = process.env.DATA_DIR
  ? resolve(process.env.DATA_DIR)
  : resolve(import.meta.dirname, '..', 'data');

mkdirSync(`${dataDir}/artifacts`, { recursive: true });

export const db = new DatabaseSync(`${dataDir}/memory.sqlite`);
export const dataPath = dataDir;

db.exec(`PRAGMA journal_mode=WAL`);
db.exec(`PRAGMA foreign_keys=ON`);
db.exec(`PRAGMA synchronous=NORMAL`);

db.exec(`
CREATE TABLE IF NOT EXISTS memory (
  key         TEXT NOT NULL,
  value       TEXT NOT NULL,
  tags        TEXT NOT NULL DEFAULT '[]',
  namespace   TEXT NOT NULL DEFAULT 'default',
  source      TEXT,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  search_text TEXT,
  PRIMARY KEY (key, namespace)
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
  key, value, tags, search_text,
  content=memory, content_rowid=rowid
);

CREATE TRIGGER IF NOT EXISTS memory_ai AFTER INSERT ON memory BEGIN
  INSERT INTO memory_fts(rowid, key, value, tags, search_text)
  VALUES (new.rowid, new.key, new.value, new.tags, COALESCE(new.search_text, ''));
END;

CREATE TRIGGER IF NOT EXISTS memory_au AFTER UPDATE ON memory BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, key, value, tags, search_text)
  VALUES ('delete', old.rowid, old.key, old.value, old.tags, COALESCE(old.search_text, ''));
  INSERT INTO memory_fts(rowid, key, value, tags, search_text)
  VALUES (new.rowid, new.key, new.value, new.tags, COALESCE(new.search_text, ''));
END;

CREATE TRIGGER IF NOT EXISTS memory_ad AFTER DELETE ON memory BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, key, value, tags, search_text)
  VALUES ('delete', old.rowid, old.key, old.value, old.tags, COALESCE(old.search_text, ''));
END;

CREATE TABLE IF NOT EXISTS artifacts (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  mime_type   TEXT NOT NULL DEFAULT 'application/octet-stream',
  filename    TEXT NOT NULL,
  size_bytes  INTEGER NOT NULL DEFAULT 0,
  tags        TEXT NOT NULL DEFAULT '[]',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  agent_id   TEXT PRIMARY KEY,
  status     TEXT NOT NULL,
  focus      TEXT,
  expires_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS locks (
  resource    TEXT PRIMARY KEY,
  agent_id    TEXT NOT NULL,
  expires_at  INTEGER NOT NULL,
  acquired_at INTEGER NOT NULL
);
`);

// Additive migration — safe to run every boot. node:sqlite's ALTER TABLE
// ADD COLUMN throws if the column already exists, so guard on table_info.
const memoryCols = (db.prepare(`PRAGMA table_info(memory)`).all() as { name: string }[]).map(c => c.name);
if (!memoryCols.includes('archived')) db.exec(`ALTER TABLE memory ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`);
if (!memoryCols.includes('expires_at')) db.exec(`ALTER TABLE memory ADD COLUMN expires_at INTEGER`);

// Deletes and returns expired memory rows (TTL-based hard delete). Callers
// that also maintain the LanceDB vector index (memory.ts) are responsible
// for vector-deleting the returned keys — kept out of this module so db.ts
// stays free of a dependency on lance.ts.
export function purgeExpiredMemory(): { key: string; namespace: string }[] {
  const now = Date.now();
  const expired = db.prepare('SELECT key, namespace FROM memory WHERE expires_at IS NOT NULL AND expires_at <= ?').all(now) as { key: string; namespace: string }[];
  if (expired.length) db.prepare('DELETE FROM memory WHERE expires_at IS NOT NULL AND expires_at <= ?').run(now);
  return expired;
}

// Batched (key, namespace) -> row lookup for semantic search results, which
// otherwise need one query per hit. Returns rows aligned index-for-index
// with `hits`, undefined where no matching row exists.
export function getMemoryRowsForHits<T extends { key: string; namespace: string }>(hits: T[]): (MemoryRow | undefined)[] {
  if (!hits.length) return [];
  const where = hits.map(() => '(key = ? AND namespace = ?)').join(' OR ');
  const params = hits.flatMap(h => [h.key, h.namespace]);
  const rows = db.prepare(`SELECT * FROM memory WHERE ${where}`).all(...params) as unknown as MemoryRow[];
  const byNamespace = new Map<string, Map<string, MemoryRow>>();
  for (const r of rows) {
    let byKey = byNamespace.get(r.namespace);
    if (!byKey) byNamespace.set(r.namespace, byKey = new Map());
    byKey.set(r.key, r);
  }
  return hits.map(h => byNamespace.get(h.namespace)?.get(h.key));
}
