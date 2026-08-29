Architecture decision records capture the choices that shaped Noema, the alternatives considered, and the consequences accepted. They are reproduced here in order. ADR-0002 was corrected in 0.2: the event sequence is not gap-free, cursors tolerate gaps by construction, and a test proves it. ADR-0009 and ADR-0010 record the two decisions the review added: redaction as the only write on append-only history, and Amazon Bedrock over a standard-library signer rather than a vendor SDK.

{{include docs/adr/0001-global-workspace.md demote=1 keep-title}}

{{include docs/adr/0002-event-immutability.md demote=1 keep-title}}

{{include docs/adr/0003-memory-representation.md demote=1 keep-title}}

{{include docs/adr/0004-llm-abstraction.md demote=1 keep-title}}

{{include docs/adr/0005-capability-system.md demote=1 keep-title}}

{{include docs/adr/0006-authentication.md demote=1 keep-title}}

{{include docs/adr/0007-frontend-architecture.md demote=1 keep-title}}

{{include docs/adr/0008-storage-and-migrations.md demote=1 keep-title}}

{{include docs/adr/0009-redaction.md demote=1 keep-title}}

{{include docs/adr/0010-bedrock-signing.md demote=1 keep-title}}
