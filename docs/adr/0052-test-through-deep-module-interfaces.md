# Test through deep-module interfaces

Production callers, fakes, the deterministic simulator, and verification suites use the same Configuration Compiler, Planner, Executor, State Backend, Implementation Adapter, and Operation Handler interfaces. Golden, property, state-machine fault-injection, shared backend contract, real-provider contract, and end-to-end scenario tests cover progressively wider behavior. The simulator accelerates failure exploration but never substitutes for real provider tests.
