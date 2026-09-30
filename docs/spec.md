# MoonChess V0 acceptance specification

This file records the implementation target supplied for this repository.

- Research Mooncake's current non-Python interfaces and bindings, especially C++, C ABI, Rust, and Go.
- Select Go, Rust, or C++ for the control plane, worker, and gateway using these priorities: minimal glue, stable Mooncake access, room-actor-friendly concurrency, and simple containers.
- Store immutable game snapshots in Mooncake.
- Store ownership, epoch, lease, and current snapshot pointer in PostgreSQL.
- Run two workers and support takeover after owner failure.
- Provide a gateway/API and minimal web chessboard.
- Start with one Docker Compose command.
- Demonstrate both killed-worker takeover and stale-worker fencing after pause/unpause.
- Do not add Kubernetes, Redis, RDMA, GPU, or a Python application service.
- Preserve `ControlPlane` and `StateStore` abstractions for future backends.
- Include reproducible documentation, scripts, tests, and actual verification.
