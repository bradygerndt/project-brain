import { z } from 'zod';
import { randomUUID } from 'node:crypto';
import { mkdirSync, writeFileSync, readFileSync } from 'node:fs';
import { resolve, extname } from 'node:path';
import { db, dataPath } from './db.ts';
import type { ArtifactRow } from './db.ts';
import { networkInterfaces } from 'node:os';
import type { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';

export const artifactsDir = resolve(dataPath, 'artifacts');

// Host this container is reachable at from outside — used both for artifact
// URLs and (from server.ts) the web UI URL, since both are served off the
// same container as ARTIFACTS_HOST/--artifacts-host already documents.
export function resolveHost(): string {
  const override = process.env.ARTIFACTS_HOST;
  if (override) return override;
  const nets = networkInterfaces();
  for (const ifaces of Object.values(nets)) {
    for (const iface of ifaces ?? []) {
      if (!iface.internal && iface.family === 'IPv4') return iface.address;
    }
  }
  return '127.0.0.1';
}

function artifactUrl(id: string, filename: string): string {
  const port = process.env.ARTIFACTS_PORT ?? '3580';
  return `http://${resolveHost()}:${port}/artifacts/${id}/${filename}`;
}

function safeFilename(name: string, mimeType: string): string {
  const base = name.replace(/[^a-z0-9._-]/gi, '_').replace(/_{2,}/g, '_').slice(0, 100);
  if (extname(base)) return base;
  const ext = mimeType === 'text/html' ? '.html'
    : mimeType === 'application/json' ? '.json'
    : mimeType === 'application/yaml' ? '.yaml'
    : mimeType?.startsWith('image/') ? `.${mimeType.split('/')[1]}`
    : '';
  return base + ext;
}

export function registerArtifactTools(server: McpServer) {
  server.registerTool(
    'artifact_write',
    { description: 'Store a file artifact (HTML, image, JSON, etc.). Returns the artifact id and HTTP URL.', inputSchema: {
      name: z.string().describe('Human-readable name for this artifact'),
      content: z.string().describe('File content as utf8 text or base64 string'),
      encoding: z.enum(['utf8', 'base64']).optional(),
      mime_type: z.string().optional(),
      tags: z.array(z.string()).optional(),
    } },
    async ({ name, content, encoding = 'utf8', mime_type = 'application/octet-stream', tags = [] }) => {
      const id = randomUUID();
      const filename = safeFilename(name, mime_type);
      const dir = resolve(artifactsDir, id);
      mkdirSync(dir, { recursive: true });

      const buf = encoding === 'base64' ? Buffer.from(content, 'base64') : Buffer.from(content, 'utf8');
      writeFileSync(resolve(dir, filename), buf);

      const now = Date.now();
      db.prepare(
        'INSERT INTO artifacts(id, name, mime_type, filename, size_bytes, tags, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)'
      ).run(id, name, mime_type, filename, buf.byteLength, JSON.stringify(tags), now, now);

      return { content: [{ type: 'text' as const, text: JSON.stringify({ id, url: artifactUrl(id, filename) }) }] };
    }
  );

  server.registerTool(
    'artifact_read',
    { description: 'Read the content of a stored artifact.', inputSchema: { id: z.string() } },
    async ({ id }) => {
      const row = db.prepare('SELECT * FROM artifacts WHERE id = ?').get(id) as ArtifactRow | undefined;
      if (!row) return { content: [{ type: 'text' as const, text: 'null' }] };

      const filePath = resolve(artifactsDir, id, row.filename);
      const buf = readFileSync(filePath);
      const isText = row.mime_type.startsWith('text/') || row.mime_type.includes('json') || row.mime_type.includes('yaml');
      const encoding = isText ? 'utf8' : 'base64';
      const content = isText ? buf.toString('utf8') : buf.toString('base64');

      return { content: [{ type: 'text' as const, text: JSON.stringify({ ...row, tags: JSON.parse(row.tags), content, encoding }) }] };
    }
  );

  server.registerTool(
    'artifact_list',
    { description: 'List stored artifacts.', inputSchema: {
      tag: z.string().optional(),
      limit: z.number().int().positive().optional(),
      offset: z.number().int().nonnegative().optional(),
    } },
    async ({ tag, limit = 50, offset = 0 }) => {
      let sql = 'SELECT id, name, mime_type, filename, size_bytes, tags, created_at, updated_at FROM artifacts WHERE 1=1';
      const params: (string | number)[] = [];
      if (tag) { sql += ' AND tags LIKE ?'; params.push(`%"${tag}"%`); }
      sql += ' ORDER BY created_at DESC LIMIT ? OFFSET ?';
      params.push(limit, offset);
      const rows = db.prepare(sql).all(...params) as unknown as ArtifactRow[];
      const results = rows.map(r => ({
        ...r, tags: JSON.parse(r.tags), url: artifactUrl(r.id, r.filename)
      }));
      return { content: [{ type: 'text' as const, text: JSON.stringify(results) }] };
    }
  );

  server.registerTool(
    'artifact_url',
    { description: 'Get the HTTP URL for an artifact without reading its content.', inputSchema: { id: z.string() } },
    async ({ id }) => {
      const row = db.prepare('SELECT filename FROM artifacts WHERE id = ?').get(id) as Pick<ArtifactRow, 'filename'> | undefined;
      if (!row) return { content: [{ type: 'text' as const, text: 'null' }] };
      return { content: [{ type: 'text' as const, text: JSON.stringify({ url: artifactUrl(id, row.filename) }) }] };
    }
  );
}
