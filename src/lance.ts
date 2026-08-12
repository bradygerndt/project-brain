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

const vectorsDir = resolve(dataPath, 'vectors');
mkdirSync(vectorsDir, { recursive: true });

let ldb: Connection | undefined;
let table: Table | undefined;
let embedder: FeatureExtractionPipeline | undefined;

async function getDb(): Promise<Connection> {
  ldb ??= await lancedb.connect(vectorsDir);
  return ldb;
}

async function getTable(): Promise<Table> {
  if (table) return table;
  const db = await getDb();
  const names = await db.tableNames();
  if (names.includes('memory_vectors')) {
    table = await db.openTable('memory_vectors');
  } else {
    table = await db.createTable('memory_vectors', [
      { key: '__seed__', namespace: 'default', vector: new Array(384).fill(0.0), text: '', updated_at: 0 }
    ]);
    await table.delete("key = '__seed__'");
  }
  return table;
}

export async function embed(text: string): Promise<number[]> {
  if (!embedder) {
    embedder = await pipeline('feature-extraction', 'Xenova/all-MiniLM-L6-v2');
  }
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
