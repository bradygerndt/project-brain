import { z } from 'zod';
import type { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { db } from './db.ts';
import type { AgentRow, LockRow } from './db.ts';

export function registerAgentTools(server: McpServer) {
  server.tool(
    'agent_ping',
    'Announce presence and current activity. Returns the list of all currently active agents.',
    {
      agent_id: z.string().describe('Unique identifier for this agent, e.g. "web-agent", "mobile-agent"'),
      status: z.string().optional().describe('"idle" | "working" | "blocked" | any custom string'),
      focus: z.string().optional().describe('What the agent is currently working on, e.g. "src/api/auth.ts"'),
      ttl_seconds: z.number().int().positive().optional().describe('How long until this presence expires (default 60s)'),
    },
    async ({ agent_id, status = 'working', focus, ttl_seconds = 60 }) => {
      const now = Date.now();
      const expires_at = now + ttl_seconds * 1000;
      db.prepare(`
        INSERT INTO agents(agent_id, status, focus, expires_at, updated_at)
        VALUES(?, ?, ?, ?, ?)
        ON CONFLICT(agent_id) DO UPDATE SET
          status=excluded.status,
          focus=excluded.focus,
          expires_at=excluded.expires_at,
          updated_at=excluded.updated_at
      `).run(agent_id, status, focus ?? null, expires_at, now);

      const agents = db.prepare('SELECT * FROM agents WHERE expires_at > ? ORDER BY updated_at DESC').all(now) as unknown as AgentRow[];
      return { content: [{ type: 'text' as const, text: JSON.stringify(agents) }] };
    }
  );

  server.tool(
    'agent_list',
    'List all currently active (non-expired) agents.',
    {},
    async () => {
      const now = Date.now();
      db.prepare('DELETE FROM agents WHERE expires_at <= ?').run(now);
      const agents = db.prepare('SELECT * FROM agents ORDER BY updated_at DESC').all() as unknown as AgentRow[];
      return { content: [{ type: 'text' as const, text: JSON.stringify(agents) }] };
    }
  );

  server.tool(
    'lock_acquire',
    'Atomically acquire a named resource lock. Expired locks are auto-released before the attempt.',
    {
      resource: z.string().describe('The resource to lock, e.g. "src/api/auth.ts" or "db/schema"'),
      agent_id: z.string(),
      ttl_seconds: z.number().int().positive().optional().describe('Lock TTL in seconds (default 300). Prevents deadlock if agent crashes.'),
    },
    async ({ resource, agent_id, ttl_seconds = 300 }) => {
      const now = Date.now();
      const expires_at = now + ttl_seconds * 1000;

      // Release any expired lock on this resource first
      db.prepare('DELETE FROM locks WHERE resource = ? AND expires_at <= ?').run(resource, now);

      const result = db.prepare(`
        INSERT OR IGNORE INTO locks(resource, agent_id, expires_at, acquired_at)
        VALUES(?, ?, ?, ?)
      `).run(resource, agent_id, expires_at, now);

      if (result.changes > 0) {
        return { content: [{ type: 'text' as const, text: JSON.stringify({ acquired: true, resource, agent_id, expires_at }) }] };
      }

      const held = db.prepare('SELECT agent_id, expires_at FROM locks WHERE resource = ?').get(resource) as LockRow | undefined;
      return { content: [{ type: 'text' as const, text: JSON.stringify({ acquired: false, held_by: held?.agent_id, expires_at: held?.expires_at }) }] };
    }
  );

  server.tool(
    'lock_release',
    'Release a resource lock. Only the agent that holds the lock can release it.',
    {
      resource: z.string(),
      agent_id: z.string(),
    },
    async ({ resource, agent_id }) => {
      const result = db.prepare('DELETE FROM locks WHERE resource = ? AND agent_id = ?').run(resource, agent_id);
      return { content: [{ type: 'text' as const, text: JSON.stringify({ released: result.changes > 0 }) }] };
    }
  );
}
