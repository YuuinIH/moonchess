# Mooncake interface survey (2026-09-30)

MoonChess pins Mooncake to stable release `v0.3.13.post1` (`719735896c86b56fabec6cf3e825fb2ea640597a`). The survey below is tied to that release rather than a moving `main` branch.

| Option | Current state | Integration cost | Assessment |
| --- | --- | --- | --- |
| C++ `Client` | Primary, full Store API | Application inherits Mooncake's CMake and C++ dependency graph | Stable but couples every service to the heaviest toolchain |
| C ABI (`store_c.h`) | Official create/setup/put/get/remove API | Still links the C++ Store libraries | Best foundation for other native bindings, not the simplest service boundary |
| Go | Official package under `mooncake-store/go`; CGo over the C ABI | Build/runtime must carry headers and native libraries | Preferred native path for this Go runtime; still needs packaged native libraries and an E2E check |
| Rust | Official crate with static-link and `dlopen` backends | Good safety and Tokio actor fit; still needs the shared library at runtime | Viable, but more deployment glue than Go plus HTTP |
| Store REST | Official `mc_store_rest_server` exposes put/get/exist/remove and owns a Store client | Plain HTTP from Go; the infrastructure image contains a Python wrapper | Current Compose adapter; does not expose locality or placement controls to Go |

Sources:

- [Mooncake v0.3.13.post1 release](https://github.com/kvcache-ai/Mooncake/releases/tag/v0.3.13.post1)
- [C API header](https://github.com/kvcache-ai/Mooncake/blob/v0.3.13.post1/mooncake-store/include/store_c.h)
- [official Go binding](https://github.com/kvcache-ai/Mooncake/tree/v0.3.13.post1/mooncake-store/go)
- [official Rust binding](https://github.com/kvcache-ai/Mooncake/tree/v0.3.13.post1/mooncake-store/rust)
- [Store REST service](https://github.com/kvcache-ai/Mooncake/blob/v0.3.13.post1/mooncake-wheel/mooncake/mooncake_store_service.py)

## Decision

Use Go for gateway, worker, room actor, chess rules, and etcd control plane. The pinned Mooncake image's Store REST process is the currently runnable data-plane adapter. The official Go binding is the native integration target; this repository has not yet demonstrated its CGo packaging or direct placement calls. The pinned image contains `mooncake/store.so`, but no `libmooncake_store.so` or `store_c.h`; a symbol probe confirmed that the Python extension does not export the C-ABI create/setup/put/get functions. Native Go integration therefore requires a separate C-ABI library build and a matching runtime image, not merely linking against the shipped Python extension.

Mooncake's own service sits behind `state.Store`. The adapter is UTF-8 JSON only. Its reads count payload bytes at the Go boundary, not physical transfer bytes. It cannot assert Mooncake locality aware placement or a physical cold/warm/hot path; those require a native Store client per worker and placement telemetry.

## Known boundary

Mooncake Store is a distributed cache. In this Compose topology, payloads survive worker death or pause because the standalone Store process owns the memory. They do not survive loss of that process or host.
