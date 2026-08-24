# ADR-0010: Amazon Bedrock over a standard-library SigV4 signer

Date: 2026-09-15  
Status: Accepted

## Context
Operators asked for Amazon Bedrock. ADR-0004 kept the LLM layer replaceable and deferred Bedrock until a request signer was justified. The AWS SDK for Go is large, pulls in many transitive modules, and would be the only non-database dependency in the runtime; the surface Noema needs is one signed POST per call.

## Decision
Implement AWS Signature Version 4 in `internal/llm/providers/sigv4.go` with `crypto/hmac` and `crypto/sha256` only, restricted to what Bedrock uses (POST, JSON body, host, date, content type, optional session token, no query string). The signer is pinned by tests to signatures produced by botocore for the same requests, with and without a session token, so drift from the reference implementation fails the build. Chat uses the Converse API, embeddings use InvokeModel with the Titan and Cohere shapes. The credential is one secret reference, `ACCESS_KEY_ID:SECRET_ACCESS_KEY[:SESSION_TOKEN]`, encrypted together like every other provider key. The region is read from the endpoint host, which must be a `bedrock-runtime.<region>.amazonaws.com` URL over HTTPS; other hosts are refused unless the provider explicitly allows private endpoints (tests and local gateways). Provider errors are scrubbed of the secret and token before they reach logs or the UI.

## Consequences
- No new module dependency; the module graph stays auditable.
- Only the signed request shapes Bedrock needs are supported; presigned URLs, S3-style single encoding and chunked signing are out of scope and would need new tests against the reference signer.
- Converse has no JSON-mode switch, so JSON roles rely on the instruction plus Noema's own parsing and validation, as with every provider.
- Temporary credentials expire; rotation is the operator's job through the ordinary secret-reference update path.
