# Do not promise transactional deployments

A multi-component deployment is an ordered, resumable operation with explicit progress and recovery rather than an all-or-nothing transaction. Stateless routing and workloads may often roll back, while stateful changes may require compensating or forward-recovery actions. Provision keeps partial execution visible and never reports it as though the entire infrastructure operation atomically succeeded or vanished.
