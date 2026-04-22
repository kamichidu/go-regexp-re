# GEMINI.md - go-regexp-re Project Constitution

This document defines the foundational principles and technical mandates for the `go-regexp-re` project. As the Gemini CLI agent, you must prioritize these instructions over general defaults for all development, refactoring, and optimization tasks.

## 1. Project Philosophy
`go-regexp-re` is a **Pure Go, high-performance DFA regular expression engine** designed to surpass the physical throughput limits of the standard `regexp` package.

- **Objective**: Achieve 5x to 100x higher throughput than Go's standard `regexp` while strictly guaranteeing $O(n)$ time complexity.
- **Vision**: To evolve the concept of `Regexp::Assemble` into a modern engine optimized for CPU cache locality and pipeline efficiency.

## 2. Core Architectural Mandates
Every implementation must adhere to these pillars to ensure maximum performance:

### 2.1 Deterministic Finite Automaton (DFA)
- **Deterministic Transitions**: Patterns must be pre-compiled into a single transition table or a bit-parallel state vector where one input byte leads to exactly one deterministic set of states.
- **Constant Time Per Byte**: Processing cost per byte must be fixed at $O(1)$, regardless of pattern complexity.

### 2.2 Byte-Oriented Scanning
- **Eliminate Rune Decoding**: Abandon mandatory UTF-8 to `rune` decoding. Scan `[]byte` directly to maximize CPU pipeline efficiency.
- **Byte-Level Transitions**: All state transitions must operate on raw bytes to minimize branching and memory latency.

### 2.3 Cache Locality Optimization
- **Flattened Memory Layout**: Transition tables MUST be stored as a contiguous `int32` array. Access must use `table[(state << 8) | byte]` to eliminate multiplication and maximize L1/L2 cache hit rates.
- **Minimize Memory Latency**: Keep core structures small enough to fit within L2/L3 caches even for large pattern sets.

### 2.4 Execution Switching Strategy
To maximize throughput, the engine MUST select the most efficient execution loop based on pattern characteristics:
- **0-Pass (Literal Bypass)**: Selected for pure constant strings and anchored literals (e.g., `^abc$`, `^abc`, `abc$`). Bypasses all regex engines using SIMD-accelerated standard library search (e.g., `bytes.Index`, `bytes.HasPrefix`). It utilizes a **unified, interface-free `LiteralMatcher` component** with a **Capture Template** to provide submatch indices with zero state-machine and zero dynamic-dispatch overhead. **When this strategy is selected, the engine MUST explicitly bypass all DFA construction to minimize compilation latency and memory footprint.**
- **Bit-parallel Path (Glushkov BP-DFA)**: The **"Express Pass"** for small, simple patterns. Utilizes ultra-fast `uint64` bitwise operations to eliminate memory loads.
- **Fast Path (Pure DFA)**: Automatically selected for larger patterns. It utilizes a minimalist table-based execution loop with **manual restarts and SIMD-accelerated prefix skipping**.
- **Anchor-Aware Guarded SIMD Warp**: Selected for patterns with anchors. Utilizes a separate `anchorTransitions` table and **guarded warp points** to allow SIMD skipping even in the presence of anchors (e.g., `^`, `$`, `\b`).
- **Explicit Hot-Loop Monomorphization**: To ensure zero-overhead, the engine MUST avoid Go generics (`GCShape` sharing) for the primary execution loops. Instead, it employs manually monomorphized functions (e.g., `fastMatchExecLoop`, `extendedMatchExecLoop`, `extendedSubmatchExecLoop`) to ensure the Go compiler can completely eliminate unreachable branches (like `if hasAnchors`) and avoid runtime dictionary lookups.

### 2.5 Submatch Extraction Architecture (3-Pass Sparse TDFA)
The engine follows a **3-Pass Sparse TDFA** strategy to guarantee peak performance, $O(n)$ time complexity, and 100% Go-compatible precision.

- **NFA-Free & Calculation-Free Mandate**: Runtime NFA simulation, backtracking, or dynamic priority comparison is **STRICTLY PROHIBITED**. All submatch extraction decisions MUST be pre-calculated and "burned into" the transition tables during compilation.
- **Pass 1: Naked Discovery**: A high-speed scan (DFA or BP-DFA) determines match boundaries `[start, end]` and records a history of deterministic states.
    - **Priority Separation**: To prevent incorrect match boundary extensions (e.g., `aa|a` matching `aaaa` as `[0 4]`), Pass 1 DFA states MUST include the **MatchPriority** (the highest priority NFA path in the state) in their identity key. This ensures that states with the same NFA IDs but different leftmost-first implications are physically separated.
- **Pass 2: Path Identity Selection**: Identifies the unique "winning NFA path" by **performing a backward trace** from the match end point. This leverages the `MatchPriority` determined in Pass 1 to reconstruct the priority identity sequence without lookahead or ambiguity. **To maintain submatch precision for multi-byte runes, Pass 2 MUST trace every byte transition in the history, ensuring that priority shifts occurring within continuation bytes are correctly captured.** If multiple paths lead to the same result, the one with the **minimum InputPriority** MUST be selected to satisfy Go's leftmost-first rule.
- **Pass 3: Group-Specific Recap (Licking)**: Iterates forward along the confirmed winning path and applies delta tags from the `RecapTable`. This pass MUST be a pure, sequential update loop ("licking") where later tags on the path define the final boundaries. **When a Warp (jump) occurs, any tags bundled within the warped bytes MUST be applied at their relative offsets.**
- **Transition-Resident Tags (Delta-Only)**: Because DFA states are "Naked", capturing group boundaries (tags) MUST be associated exclusively with **transitions** (`RecapEntry`). To prevent boundary corruption, these tags MUST be stored as **deltas** (newly encountered during that specific transition).
- **Priority Delta Propagation**: During DFA execution, priority (`prio`) MUST be tracked using **Priority Deltas** stored in `TransitionUpdate.BasePriority`. This prevents absolute priority accumulation from corrupting search restart logic.

#### 2.5.1 NFA-Free Path Selection Mandate (Pass 2)
Path selection in Pass 2 MUST NOT employ runtime NFA simulation or dynamic priority comparisons. The identity of the winning path must be reconstructed solely by following pre-calculated priority transitions (`InputPriority` -> `NextPriority`) stored within the `RecapTable`.
- **Zero-Allocation Execution**: All recap paths MUST be strictly iterative and utilize stack-resident or pre-allocated buffers, ensuring zero heap allocations during execution.
- **Naked History Isolation (Panic Prevention)**: To maintain $O(1)$ table access in Pass 2 and 3 without redundant boundary checks, the execution history (`mc.history`) MUST store only the raw state index. All control flags (Tagged, Anchor, Warp) MUST be physically stripped via `StateIDMask` before recording the trace.

### 2.6 Physical Prevention of State Explosion (Naked State Identity)
To achieve scalability, DFA construction (Subset Construction) employs **Naked State Identity**:
- **Identity via NFA Set**: DFA state identity is primarily defined by the NFA state set.
- **Additive Memory Structure**: Limits memory usage to `O(DFA States + Σ Group Tables)`, ensuring that total memory consumption is linear relative to the number of capturing groups.

### 2.7 Static Compatibility Check & Structural Rejection
To maintain the integrity of the NFA-free architecture, the engine MUST perform a static analysis during compilation on the optimized AST:
- **Epsilon Cycle Rejection**: Patterns that match empty strings in a loop (e.g., `(|a)*`), where deterministic path selection is impossible, MUST be rejected.
- **Ambiguous Capture Rejection**: Patterns with structural ambiguities that the 3-pass TDFA cannot reliably resolve MUST be rejected at compile time:
    - **Explicit Empty Alternatives in Captures**: e.g., `(|a)`, `(a|)`, `(a||b)`.
    - **Optional Empty Captures**: e.g., `(a*)?`, `(a?|b?)?`.
- **Deterministic Guarantee**: Only patterns whose submatch extraction can be perfectly "burned" into a deterministic table are supported.
- **Error Type**: Violations MUST return a **`regexp.UnsupportedError`** (aliased from `syntax.UnsupportedError`). This allows callers to distinguish between syntax errors and engine limitations.

### 2.8 Architectural Shortcut (Compilation Efficiency)
To minimize compilation overhead, the engine MUST use an **Architectural Shortcut** for simple patterns.
- **Literal-Only Bypass**: If a pattern is identified as a literal-only or anchor-literal sequence, the engine MUST skip `ir.DFA` construction entirely and delegate all operations to the `LiteralMatcher`.
- **Skip Heavy DFA**: If a pattern is simple (NFA nodes $\le 62$, no non-greedy, **and no anchors**), the engine MUST skip the heavy DFA transition table construction and only build the `BitParallelDFA`.
- **ASCII Restriction**: BP-DFA is currently optimized for ASCII-only runes (0-127). Patterns requiring multi-byte UTF-8 support (e.g., non-ASCII runes or `.`) MUST fallback to the table-based DFA.

### 2.10 Prefix-Skip Optimization (SIMD Acceleration)
- **Mandatory Prefix Extraction**: During compilation, the longest constant prefix is extracted to ensure `LiteralPrefix()` compatibility.
- **SIMD-Accelerated Skipping**: All execution loops MUST use `bytes.Index` to rapidly skip non-matching segments (SIMD Warp).

### 2.11 Pure Go (No CGO)
- **Zero Overhead**: Native Go only. CGO is strictly prohibited.

### 2.12 Priority Normalization & Absolute Tracking
- **Priority Normalization**: During DFA construction, NFA path priorities within each state MUST be normalized.
- **Absolute Priority Tracking**: The engine MUST track cumulative priority to identify the true leftmost-first match during Phase 1.

### 2.13 Early Exit Optimization (IsBestMatch)
- **Strict Greedy Finality**: A DFA state is considered to have an unbeatable match (`IsBestMatch == true`) ONLY if it is an accepting state with the highest possible priority (`matchP == 0`) and starts at the earliest possible position in the current search window (`minP == 0`). This ensures that greedy repetitions do not terminate prematurely.

### 2.14 State Explosion Protection (Configurable & Scalable)
- **Default Memory Threshold**: The DFA transition table is typically limited to **64MiB**.
- **Graceful Failure**: If a pattern exceeds the configured `MaxMemory`, the engine MUST return `regexp: pattern too large or ambiguous`.
- **Dynamic Offloading**: When `MaxMemory` exceeds 1GiB, the engine SHOULD switch the NFA path set storage from memory to a **File-based backend**.

### 2.15 Syntax-Level Optimization & AST Rewriting
- **Mandatory Optimization**: The engine MUST call `syntax.Simplify` and `syntax.Optimize` before compilation. Static compatibility checks MUST be performed on the resulting optimized AST to avoid false positives.

### 2.19 Submatch Precision & Go Compatibility (Field-Proven)
- **Leftmost-First Tagging**: During Pass 3 licking, Start tags (even bits: 2, 4, ...) MUST be fixed once set (leftmost), while End tags (odd bits: 3, 5, ...) MUST be updated by the latest encounter on the winning path.
- **Lead-Byte Warp (Jump Optimization)**: For the "any-rune" (dot) and wide character classes, the DFA MUST employ a **Warp-on-Lead-Byte** strategy. **The Warp flag is applied ONLY when all NFA paths in a state accept any valid UTF-8 continuation (e.g., InstRuneAny).**
- **Warp Flag Preservation**: The `WarpStateFlag` (Bit 23) MUST be preserved during DFA minimization.

#### 2.19.1 Multi-byte Dot ('.') Determinism Mandate
To ensure DFA determinism and $O(1)$ transitions, the behavior of `.` (dot) is strictly defined as matching a single byte (for ASCII and invalid UTF-8 bytes) or a static lead-byte unit (for valid multi-byte UTF-8).
- **Invalid UTF-8 Handling**: Single bytes in the ranges `80-BF`, `C0-C1`, and `F5-FF` are treated as valid 1-byte matches for `.` to maintain parity with Go's standard library.
- **Warp Safety**: The execution loop MUST use a robust `GetTrailingByteCount` to ensure index increments do not overflow when encountering these invalid bytes during a Warp skip.
- **Submatch Precision**: Internal capturing group boundaries that fall within an invalid UTF-8 sequence are handled on a best-effort basis and may deviate from standard `regexp` to maintain $O(n)$ performance.

#### 2.19.2 Calculation-Free Boundary Analysis Mandate
Junction verification for anchors (`\b`, `^`, `$`) MUST NOT employ `utf8.DecodeRune`. Word boundaries (`\b`) are defined as **ASCII Word Boundaries**; multi-byte bytes (0x80+) are treated as non-word characters.

## 3. Feature Selection Policy

### 3.1 Supported Features
- **Standard Syntax**: Support `syntax.Prog`.
- **Anchors & Boundaries**: Supported via Virtual Byte Insertion.
- **Capturing Groups**: Supported via the DFA-First Hybrid Strategy.

### 3.2 Excluded Features
- **Backreferences & Dynamic Lookaround**: Strictly excluded.
- **POSIX Semantics**: Unsupported to maintain $O(n)$.

## 4. Engineering & Validation Standards
- **Two-Stage Submatch Evaluation**: Test validation MUST distinguish between engine search correctness and submatch extraction precision:
    - **Overall Match Mismatch**: (Indices 0, 1) If the engine fails to identify the correct match boundaries [start, end], it MUST be treated as a **FAIL**.
    - **Submatch Boundary Mismatch**: (Indices 2+) If the match boundaries are correct but internal group boundaries deviate from standard `regexp`, it MAY be treated as a **SKIP** (Known Limitation) to document 3-Pass TDFA boundary ambiguity.
- **Memory Accumulation Prevention**: Dispose of compiled `Regexp` objects promptly during mass testing.
- **100% DFA Validation**: DFA match boundaries MUST strictly match the standard library's boundaries except where documented (e.g., Dot behavior).

## 5. Coding Conventions
- **Explicit Aliasing**:
  - `regexp` -> `goregexp`
  - `regexp/syntax` -> `gosyntax`

---
**Note**: Any modification to the compilation shortcut or rescan dispatch must be validated against the **"Efficiency First, Precision Mandatory"** principle.
