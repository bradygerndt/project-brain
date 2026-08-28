import Anthropic from '@anthropic-ai/sdk';

export interface ConsolidatedFact {
  value: string;
}

const MODEL = 'claude-haiku-4-5-20251001';

const SYSTEM_PROMPT = `You merge a cluster of near-duplicate or closely related facts into one consolidated fact.
Return ONLY a valid JSON object of this shape:
  { "value": "string value" }

Rules:
- Preserve every distinct piece of information from the inputs — don't drop details that aren't true duplicates
- Where inputs conflict, prefer the most recently updated one (inputs are given most-recent first)
- Write one coherent value, not a list of the originals
- Do not include any explanation or markdown — just the JSON object`;

export interface FactToConsolidate {
  key: string;
  value: string;
  updated_at: number;
}

export async function consolidateCluster(facts: FactToConsolidate[]): Promise<ConsolidatedFact> {
  const apiKey = process.env.ANTHROPIC_API_KEY;
  if (!apiKey) {
    throw new Error('ANTHROPIC_API_KEY is not set — memory_consolidate requires it');
  }

  const client = new Anthropic({ apiKey });
  const userMessage = facts.map(f => `- [${f.key}] ${f.value}`).join('\n');

  const response = await client.messages.create({
    model: MODEL,
    max_tokens: 1024,
    system: SYSTEM_PROMPT,
    messages: [{ role: 'user', content: userMessage }],
  });

  const firstBlock = response.content[0];
  const raw = firstBlock?.type === 'text' ? firstBlock.text : '{}';
  try {
    const result = JSON.parse(raw);
    if (typeof result?.value !== 'string' || !result.value) throw new Error('missing value');
    return { value: result.value };
  } catch {
    throw new Error(`Consolidation returned invalid JSON: ${raw.slice(0, 200)}`);
  }
}
