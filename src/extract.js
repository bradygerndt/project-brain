import Anthropic from '@anthropic-ai/sdk';

const MODEL = 'claude-haiku-4-5-20251001';

const SYSTEM_PROMPT = `You are a fact extraction assistant.
Extract all distinct, reusable facts from the user's text.
Return ONLY a valid JSON array of objects with this shape:
  [{ "key": "dot.namespaced.key", "value": "string value", "tags": ["tag1", "tag2"] }]

Rules:
- Keys must be dot-namespaced (e.g. "user.preference.theme", "project.stack.database")
- Only extract concrete, stable facts — not transient observations or opinions
- Omit anything already obvious or derivable
- Return an empty array [] if nothing worth storing is found
- Do not include any explanation or markdown — just the JSON array`;

export async function extractFacts(text, context) {
  const apiKey = process.env.ANTHROPIC_API_KEY;
  if (!apiKey) {
    throw new Error('ANTHROPIC_API_KEY is not set — memory_extract requires it');
  }

  const client = new Anthropic({ apiKey });
  const userMessage = context
    ? `Context: ${context}\n\nText to extract from:\n${text}`
    : text;

  const response = await client.messages.create({
    model: MODEL,
    max_tokens: 1024,
    system: SYSTEM_PROMPT,
    messages: [{ role: 'user', content: userMessage }],
  });

  const raw = response.content[0]?.text ?? '[]';
  try {
    const facts = JSON.parse(raw);
    if (!Array.isArray(facts)) throw new Error('not an array');
    return facts.filter(f => f.key && f.value);
  } catch {
    throw new Error(`Extraction returned invalid JSON: ${raw.slice(0, 200)}`);
  }
}
