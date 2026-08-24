-- Fails if any bedrock provider rows exist; delete them first (deliberate: never silently drop configuration).
ALTER TABLE llm_providers DROP CONSTRAINT IF EXISTS llm_providers_kind_check;
ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_kind_check CHECK (kind IN ('openai','anthropic','ollama'));
