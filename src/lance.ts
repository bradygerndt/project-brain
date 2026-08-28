import * as lancedb from '@lancedb/lancedb';
import type { Connection, Table } from '@lancedb/lancedb';
import { pipeline, type FeatureExtractionPipeline } from '@huggingface/transformers';
import { mkdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { dataPath } from './db.ts';

export interface VectorHit {
  key: string;
  namespace: string;
  text: string;
  updated_at: number;
  _distance: number;
}

export interface NamespaceVectorRow {
  key: string;
  namespace: string;
  vector: number[];
  text: string;
  updated_at: number;
}

const vectorsDir = resolve(dataPath, 'vectors');
mkdirSync(vectorsDir, { recursive: true });

// Cached as in-flight promises, not resolved values — vectorUpsert/vectorDelete/
// vectorSearch can all fire concurrently (e.g. two memory_set calls back to
// back), and caching only the resolved value leaves a window where concurrent
// first calls all see "not ready yet" and redo the setup. For getTable() that
// meant two concurrent createTable() calls racing and corrupting the table.
// Assigning the promise itself synchronously (before any internal await runs)
// closes that window; the .catch() clears the cache on failure so a transient
// error doesn't wedge every future call.
let dbPromise: Promise<Connection> | undefined;
let tablePromise: Promise<Table> | undefined;
let embedderPromise: Promise<FeatureExtractionPipeline> | undefined;

function getDb(): Promise<Connection> {
  dbPromise ??= lancedb.connect(vectorsDir).catch(err => { dbPromise = undefined; throw err; });
  return dbPromise;
}

async function createOrOpenTable(): Promise<Table> {
  const db = await getDb();
  const names = await db.tableNames();
  if (names.includes('memory_vectors')) {
    return db.openTable('memory_vectors');
  }
  const tbl = await db.createTable('memory_vectors', [
    { key: '__seed__', namespace: 'default', vector: new Array(384).fill(0.0), text: '', updated_at: 0 }
  ]);
  await tbl.delete("key = '__seed__'");
  return tbl;
}

function getTable(): Promise<Table> {
  tablePromise ??= createOrOpenTable().catch(err => { tablePromise = undefined; throw err; });
  return tablePromise;
}

function getEmbedder(): Promise<FeatureExtractionPipeline> {
  embedderPromise ??= pipeline('feature-extraction', 'Xenova/all-MiniLM-L6-v2')
    .catch(err => { embedderPromise = undefined; throw err; });
  return embedderPromise;
}

export async function embed(text: string): Promise<number[]> {
  const embedder = await getEmbedder();
  const out = await embedder(text, { pooling: 'mean', normalize: true });
  return Array.from(out.data as ArrayLike<number>);
}

export async function vectorUpsert(key: string, namespace: string, text: string): Promise<void> {
  try {
    const tbl = await getTable();
    const vector = await embed(text);
    await tbl.mergeInsert('key')
      .whenMatchedUpdateAll()
      .whenNotMatchedInsertAll()
      .execute([{ key, namespace, vector, text, updated_at: Date.now() }]);
  } catch (err) {
    console.error('[lance] upsert failed:', err instanceof Error ? err.message : err);
  }
}

export async function vectorDelete(key: string): Promise<void> {
  try {
    const tbl = await getTable();
    await tbl.delete(`key = '${key.replace(/'/g, "''")}'`);
  } catch (err) {
    console.error('[lance] delete failed:', err instanceof Error ? err.message : err);
  }
}

// Filter-only scan (no query vector) — used by findClusters (memory.ts) to
// pull every embedding in a namespace for clustering.
export async function vectorsForNamespace(namespace: string): Promise<NamespaceVectorRow[]> {
  try {
    const tbl = await getTable();
    const rows = await tbl.query().where(`namespace = '${namespace.replace(/'/g, "''")}'`).toArray() as unknown as (Omit<NamespaceVectorRow, 'vector'> & { vector: Iterable<number> })[];
    // .query() (unlike .search()) returns `vector` as an Arrow Vector, not a plain array.
    return rows.map(r => ({ ...r, vector: Array.from(r.vector) }));
  } catch (err) {
    console.error('[lance] namespace scan failed:', err instanceof Error ? err.message : err);
    return [];
  }
}

export async function vectorSearch(query: string, namespace?: string, limit = 10): Promise<VectorHit[]> {
  try {
    const tbl = await getTable();
    const vector = await embed(query);
    let search = tbl.search(vector).limit(limit);
    if (namespace) {
      search = search.where(`namespace = '${namespace.replace(/'/g, "''")}'`);
    }
    return await search.toArray() as unknown as VectorHit[];
  } catch (err) {
    console.error('[lance] search failed:', err instanceof Error ? err.message : err);
    return [];
  }
}
