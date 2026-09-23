# Open questions

The product model is agreed. The following technical-architecture questions remain undecided.

## Technical architecture

- Internal Go package structure beyond the three core modules?
- Exact AWS-backed shared-state services and schema?
- Provisioning implementation: SDKs, Terraform/OpenTofu, CloudFormation, Pulumi, or another mechanism?
- Plugin model, if any?
- Credential and secret handling?
- Failure recovery and resumability?
- Coordinator and remote-runner protocol for collaboration and delegated execution?
- Testing, simulation, and local development strategy?
