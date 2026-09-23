# Exploratory material

The files in this directory are sketches used to test the emerging product model.

They are deliberately non-normative:

- They do not define an accepted configuration format.
- They do not establish a technical architecture.
- Names such as renderer, profile, compiler, runner, or ledger are provisional.
- Specific AWS and systemd mappings are examples of required capability, not selected implementation technology.
- References to a `cache` component and the claim that it cannot hold authoritative data predate the agreed `key-value store` role and explicit Store Data Role in [ADR 0068](../adr/0068-model-key-value-stores-with-explicit-data-role.md).

## Contents

- `configuration-sketch.md`: an illustrative configuration assembled during discovery.
- `diagrams/`: visual explorations of component mappings and deployment behavior.
