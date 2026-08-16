---
name: critical-systems-design
description: Design and implement a change using safety-critical software and performance-engineering practices.
---

# Critical systems design

Use this skill for a feature, refactor, API change, persistence or network change, concurrency change, or any change whose failure or resource behavior is not immediately obvious.

1. State the desired outcome and the invariants that must remain true. Identify the invalid states and failure paths as well as the happy path.
2. Write a brief design sketch before editing: data ownership/lifetime, bounded resources (time, memory, queue depth, retries, input size), and the major control-flow path. Identify where each important invariant is checked.
3. Estimate the dominant cost across network, disk, memory, and CPU. Note likely latency sources and batch opportunities. Prefer predictable work over clever but variable behavior.
4. Choose the smallest explicit design. Keep branching and state mutation in a coordinator; put leaf computation in simple helpers. Avoid needless abstractions, aliases, dependencies, and defaults.
5. Implement with narrow scopes, descriptive names and units, complete error handling, and assertions/validation at trust boundaries. Explain surprising decisions and rationale in comments.
6. Test normal cases, error cases, boundary values, and invalid transitions. Include a short test comment when it clarifies goal or methodology.
7. Re-read the patch for unbounded work, accidental copies/allocations, unchecked errors, ambiguous names, hidden rounding, stale aliases, and control flow that a reviewer cannot validate locally.

Return a compact summary of the design, bounds, invariant checks, performance estimate, and tests run. Escalate any unresolved trade-off rather than hiding it.
