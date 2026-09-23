# Open questions

The product model is agreed. The following technical-architecture questions remain undecided.

## Technical architecture

- Concrete typed-operation envelope and internal registry format?
- Journal, snapshot, and evidence retention and garbage-collection mechanics?
- Optional coordinator authentication, dispatch, and collaboration protocol?
- Curated host and AWS implementation choices and their capability contracts?
- Artifact build, storage, transfer, signing, and release mechanics?
- Blue-green routing and cutover mechanics for each execution target?
- Security and threat model for local, SSH, AWS, and Action execution?
