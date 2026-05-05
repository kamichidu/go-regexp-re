# Performance Analysis & TODO

## Performance Bottlenecks (2026-05-05)

### 1. `HTTP/1.1$` (Anchors/pat=HTTP/1.1$)
- **Problem**: High overhead in `AnchorInfo.Validate` and `bytes.Index`.
- **Root Cause**:
    - `HTTP/1` is selected as the primary anchor.
    - Apache logs contain many `HTTP/1.0`, triggering `bytes.Index("HTTP/1")` matches.
    - Each match triggers `Validate` for the remaining `.1$`.
    - `(?m)` is NOT enabled in the benchmark, so `$` refers to the very end of the large corpus, leading to constant validation failures.
- **Optimization Idea**: **Bounded Gap Anchors (Template Matching)**.
    - Pattern `HTTP/1.1` can be seen as `Prefix("HTTP/1")` + `Gap(1-4 bytes)` + `Suffix("1")`.
    - Instead of generic validation, we can look for the suffix `1` at a bounded distance after the prefix.
    - If the gap is a single byte (like literal `.`), we can use `SIMD` or a specialized multi-literal search.

### 2. `NFAWorstCase` (`(a+)+b`)
- **Problem**: Extreme slowness ($O(N^2)$ behavior).
- **Root Cause**:
    - **Manual Search Loop**: To ensure leftmost-longest semantics, the engine restarts the DFA from every position `i` when a match isn't found or to find a better match.
    - For `(a+)+b` against `aaaa...ac`, the DFA scans to the end, fails, and restarts from `i+1`.
    - **DFA Step Cost**: The `fastMatchExecLoop` has multiple flags per byte (Anchor, Tag, CCWarp), making it slower than a pure DFA.
- **Optimization Idea**:
    - **Searching DFA**: Integrate the search into the DFA itself (merging the start state into a "Searching" state) to avoid $O(N^2)$ restarts.
    - **NFA-to-DFA Optimization**: Detect and simplify `(a+)+` to `a+`.

## TODO

- [ ] Implement **Bounded Gap Anchors (Right-Anchored Template Matching)** in `internal/ir/anchor.go`.
    - Detect `Literal + Dot/Class + Literal + $` sequences.
    - **Placeholder + EOF Synergy**:
        - For `HTTP/1.1$`, we know the distance from `HTTP/1` to `$` is exactly 2 bytes (if `.` is literal) or 2-5 bytes (if `.` is UTF-8).
        - **Fast Pruning**: If `HTTP/1` is found at position `p`, and `len(input) - (p + len("HTTP/1"))` is not within [2..5], the match is **impossible**.
        - This allows `$` to act as a distance-based filter, eliminating `Validate` calls for any `HTTP/1` that isn't at the very end of the input (or line, if `(?m)`).
    - **SIMD Lookahead**: Use `b[len(input)-1] == '1'` to quickly check the last byte before even looking for `HTTP/1` if selectivity is high.
- [ ] Add **Constraint Backpropagation** to DFA construction.
    - If a state is only reachable via `HTTP/1` and must be followed by `.1`, we can use this information to prune SearchWarp or add lookahead guards to transitions.
