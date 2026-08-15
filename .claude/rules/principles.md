# Critical Systems Engineering Principles

## Priorities

1. Safety and correctness.
2. Performance and predictable resource use.
3. Developer experience.

Choose the simplest design that improves all three. Do the necessary design thinking before implementation. Do not knowingly introduce technical debt, unbounded work, avoidable latency spikes, or silently ignored failure paths.

## Code rules

- Use direct, explicit control flow. Avoid recursion unless it is demonstrably bounded and the trade-off is documented. Prefer a small number of excellent abstractions to layers of indirection.
- Put bounds on loops, queues, retries, batches, input sizes, resource use, and time. Assert or otherwise make an intentionally non-terminating loop explicit.
- Treat programmer mistakes as invariant failures and operational failures as expected errors that must be handled. Never silently discard an error.
- Validate inputs, outputs, preconditions, postconditions, and important invariants. Check important properties at more than one boundary when data crosses a trust, persistence, or serialization boundary. Keep assertions focused: split compound checks when that produces clearer failure information.
- State invariants positively and make both valid and invalid cases clear. Prefer clear branch trees over dense compound conditions or complex `else if` chains.
- Keep functions small enough to understand without scrolling (target 70 lines or fewer). Keep control flow and state mutation in the coordinating function; extract non-branching computations into small, preferably pure helpers.
- Keep variable scope narrow. Create, validate, and consume values close together. Do not duplicate mutable state or introduce aliases that can drift out of sync.
- Use precise types and explicit units. Treat indexes, counts, sizes, durations, and offsets as distinct concepts. Make division/rounding semantics explicit.
- Do not abbreviate ordinary identifiers. Put units and qualifiers last (for example, `latency_ms_max`), and avoid overloaded names.
- Prefer simple signatures and return types. Use named options when same-typed arguments could be confused; name nullable values so the meaning of `null` is obvious at the call site. Put callback parameters last.
- Be explicit at integration points: choose library options deliberately instead of relying on defaults that could change. Prefer batching and controlled work scheduling over reacting directly to each external event.
- Explain *why* in comments and commits, and explain test intent and method when it is not obvious. Write comments as complete prose. Do not use comments to restate code.
- Consider network, disk, memory, and CPU cost during design. Estimate bandwidth and latency before optimizing; batch work to amortize expensive operations.
- Minimize dependencies and tooling. Add one only when its continuing cost is justified by a clear, material benefit.

## Change checklist

Before completing a non-trivial change, use the `critical-systems-design` skill. For changes on correctness-critical, resource-sensitive, or hot paths, also ask the appropriate specialist reviewer subagent for a focused review.
