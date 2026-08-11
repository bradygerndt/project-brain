import * as lancedb from '@lancedb/lancedb';
import { pipeline } from '@huggingface/transformers';
import { mkdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { dataPath } from './db.js';

const vectorsDir = resolve(dataPath, 'vectors');
mkdirSync(vectorsDir, { recursive: true });

let ldb;
let table;
let embedder;

async function getDb() {
  ldb ??= await lancedb.connect(vectorsDir);
  return ldb;
}

async function getTable() {
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

export async function embed(text) {
  if (!embedder) {
    embedder = await pipeline('feature-extraction', 'Xenova/all-MiniLM-L6-v2');
  }
  const out = await embedder(text, { pooling: 'mean', normalize: true });
  return Array.from(out.data);
}

export async function vectorUpsert(key, namespace, text) {
  try {
    const tbl = await getTable();
    const vector = await embed(text);
    await tbl.mergeInsert('key')
      .whenMatchedUpdateAll()
      .whenNotMatchedInsertAll()
      .execute([{ key, namespace, vector, text, updated_at: Date.now() }]);
  } catch (err) {
    console.error('[lance] upsert failed:', err.message);
  }
}

export async function vectorDelete(key) {
  try {
    const tbl = await getTable();
    await tbl.delete(`key = '${key.replace(/'/g, "''")}'`);
  } catch (err) {
    console.error('[lance] delete failed:', err.message);
  }
}

export async function vectorSearch(query, namespace, limit = 10) {
  try {
    const tbl = await getTable();
    const vector = await embed(query);
    let search = tbl.search(vector).limit(limit);
    if (namespace) {
      search = search.where(`namespace = '${namespace.replace(/'/g, "''")}'`);
    }
    return await search.toArray();
  } catch (err) {
    console.error('[lance] search failed:', err.message);
    return [];
  }
}
