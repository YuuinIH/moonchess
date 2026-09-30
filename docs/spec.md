# MoonChess final architecture

- etcd is the only coordination authority: owner lease, epoch, fencing, committed head/sequence, checkpoints, migration phase and target, and advisory worker progress.
- Mooncake contains immutable checkpoint and delta payloads. It provides storage placement, replication and transfer, without deciding canonical history.
- MaterializedState = Checkpoint + DeltaSuffix. A warm worker follows missing prev links to its materialized head; a cold worker follows them to the latest checkpoint.
- A move first writes a uniquely addressed immutable delta, then advances the canonical head by a fenced etcd transaction. Only transaction success is acknowledged.
- Graceful migration and crash failover both materialize the target before acquiring ownership; explicit migration pins the target.
- Checkpoints are periodic. The latest and fallback remain usable while covered older deltas are collected safely.
- Kill owner, SIGSTOP/SIGCONT stale owner, and explicit migration are E2E acceptance scenarios.
- No PostgreSQL or Kubernetes dependency. Go remains the runtime language; Mooncake native Go/C ABI integration should be preferred when its packaged library is available.
