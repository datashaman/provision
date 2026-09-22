# Keep one active revision per environment

An environment has one active application revision outside a deployment transition. Blue-green deployment may temporarily prepare and verify a candidate beside the active revision, but activation ends with one selected revision. Deployments to the same environment are serialized, while concurrent isolated testing uses separate environments. This keeps shared-environment history and behavior unambiguous rather than turning one environment into an implicit multi-tenant revision host.
