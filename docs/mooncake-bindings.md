# Mooncake interface survey (2026-09-30)

MoonChess pins Mooncake to stable release `v0.3.13.post1` (`719735896c86b56fabec6cf3e825fb2ea640597a`). The survey below is tied to that release rather than a moving `main` branch.

| Option | Current state | Integration cost | V0 assessment |
| --- | --- | --- | --- |
| C++ `Client` | Primary, full Store API | Application inherits Mooncake's CMake and C++ dependency graph | Stable but couples every service to the heaviest toolchain |
| C ABI (`store_c.h`) | Official create/setup/put/get/remove API | Still links the C++ Store libraries | Best foundation for other native bindings, not the simplest service boundary |
| Go | Official package under `mooncake-store/go`; CGo over the C ABI | Idiomatic call site, but build/runtime must carry headers and native libraries | Excellent concurrency fit; native packaging is disproportionate for V0 |
| Rust | Official crate with static-link and `dlopen` backends | Good safety and Tokio actor fit; still needs the shared library at runtime | Viable, but more deployment glue than Go plus HTTP |
| Store REST | Official `mc_store_rest_server` exposes put/get/exist/remove and owns a Store client | Plain HTTP from any language; the infrastructure image contains the Python wrapper | Chosen for V0: pure-Go business services, one pinned Mooncake container boundary |

Sources:

- [Mooncake v0.3.13.post1 release](https://github.com/kvcache-ai/Mooncake/releases/tag/v0.3.13.post1)
- [C API header](https://github.com/kvcache-ai/Mooncake/blob/v0.3.13.post1/mooncake-store/include/store_c.h)
- [official Go binding](https://github.com/kvcache-ai/Mooncake/tree/v0.3.13.post1/mooncake-store/go)
- [official Rust binding](https://github.com/kvcache-ai/Mooncake/tree/v0.3.13.post1/mooncake-store/rust)
- [Store REST service](https://github.com/kvcache-ai/Mooncake/blob/v0.3.13.post1/mooncake-wheel/mooncake/mooncake_store_service.py)

## Decision

Use Go for gateway, worker, room actor, chess rules, and PostgreSQL control plane. Use the pinned Mooncake image's Store REST process as the data-plane adapter.

This is not an HTTP reimplementation of Mooncake. It is Mooncake's own service and binding, isolated behind MoonChess's `state.Store` interface. A later native adapter can replace `MooncakeHTTPStore` without changing actors or the control plane. This keeps V0 free of application-side Python and CGo while avoiding a bespoke sidecar.

## Known boundary

Mooncake Store is a distributed cache. In this Compose topology, snapshots survive worker death or pause because the standalone Store process owns the memory. They do not survive loss of the Store process or host. Durable restart recovery is intentionally outside V0 and would require Mooncake persistence/offload or another `StateStore` implementation.
