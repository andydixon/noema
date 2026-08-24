-- Allow the Amazon Bedrock provider kind. Existing rows are unaffected.
ALTER TABLE llm_providers DROP CONSTRAINT IF EXISTS llm_providers_kind_check;
ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_kind_check CHECK (kind IN ('openai','anthropic','ollama','bedrock'));
