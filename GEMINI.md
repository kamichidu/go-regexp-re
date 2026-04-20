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
- **0-Pass (Literal Bypass)**: Selected for pure constant strings and anchored literals (e.g., `^abc$`, `^abc`, `abc$`). Bypasses all regex engines using SIMD-accelerated standard library search (e.g., `bytes.Index`, `bytes.HasPrefix`). It utilizes a **unified, interface-free `LiteralMatcher` component** with a **Capture Template** to provide submatch indices with zero state-machine and zero dynamic-dispatch overhead.
- **Bit-parallel Path (Glushkov BP-DFA)**: The **"Express Pass"** for small, simple patterns. Utilizes ultra-fast `uint64` bitwise operations to eliminate memory loads.
- **Fast Path (Pure DFA)**: Automatically selected for larger patterns. It utilizes a minimalist table-based execution loop with **manual restarts and SIMD-accelerated prefix skipping**.
- **Anchor-Aware Guarded SIMD Warp**: Selected for patterns with anchors. Utilizes a separate `anchorTransitions` table and **guarded warp points** to allow SIMD skipping even in the presence of anchors (e.g., `^`, `$`, `\b`).
- **Explicit Hot-Loop Monomorphization**: To ensure zero-overhead, the engine MUST avoid Go generics (`GCShape` sharing) for the primary execution loops. Instead, it employs manually monomorphized functions (e.g., `fastExecLoop`, `extendedExecLoop`) to ensure the Go compiler can completely eliminate unreachable branches (like `if hasAnchors`) and avoid runtime dictionary lookups.

### 2.5 Submatch Extraction Architecture (3-Pass Sparse TDFA)
The engine follows a **3-Pass Sparse TDFA** strategy to guarantee peak performance, $O(n)$ time complexity, and 100% Go-compatible precision.

- **NFA-Free & Calculation-Free Mandate**: Runtime NFA simulation, backtracking, or dynamic priority comparison is **STRICTLY PROHIBITED**. All submatch extraction decisions MUST be pre-calculated and "burned into" the transition tables during compilation.
- **Pass 1: Naked Discovery**: A single high-speed scan (DFA or BP-DFA) determines match boundaries `[start, end]` and records a history of deterministic states. This pass uses a minimal DFA that excludes priority and tag information to physically block the exponential state explosion.
- **Pass 2: Path Identity Selection**: Identifies the unique "winning NFA path" from start to end based on Go's leftmost-first rules using the recorded history.
- **Pass 3: Group-Specific Recap**: Uses independent "burned tables" for each capturing group to determine boundaries along the winning path.

#### 2.5.1 NFA-Free Path Selection Mandate (Pass 2)
Path selection in Pass 2 MUST NOT employ runtime NFA simulation or dynamic priority comparisons. The identity of the winning path must be reconstructed solely by following pre-calculated priority transitions (`InputPriority` -> `NextPriority`) stored within the `RecapTable`. Pass 2 must operate as a strictly linear, table-driven loop that updates the current priority identity based on the `StateID` history from Pass 1, ensuring total compliance with the engine's calculation-free philosophy.
- **Zero-Allocation Execution**: All recap paths MUST be strictly iterative and utilize stack-resident or pre-allocated buffers, ensuring zero heap allocations during execution.
- **Naked History Isolation (Panic Prevention)**: To maintain $O(1)$ table access in Pass 2 and 3 without redundant boundary checks, the execution history (`mc.history`) MUST store only the raw state index. All control flags (Tagged, Anchor, Warp) MUST be physically stripped via `StateIDMask` before recording the trace. This isolation is the project's primary defense against memory access violations during submatch reconstruction.

### 2.6 Physical Prevention of State Explosion (Naked State Identity)
To achieve scalability, DFA construction (Subset Construction) employs **Naked State Identity**:
- **Identity via NFA Set**: DFA state identity is defined purely by the NFA state set (including NodeID). Paths with different tag histories are merged into the same DFA state.
- **Additive Memory Structure**: Limits memory usage to `O(DFA States + Σ Group Tables)`, ensuring that total memory consumption is linear relative to the number of capturing groups.
- **Decoupled Priority**: Priority resolution is deferred to Pass 2 and is not considered during Pass 1 DFA construction.

### 2.7 Static Compatibility Check & Epsilon Cycle Rejection
To maintain the integrity of the NFA-free architecture, the engine MUST perform a static analysis during compilation:
- **Epsilon Cycle Rejection**: Patterns that match empty strings in a loop (e.g., `(|a)*`), where deterministic path selection is impossible, MUST be detected and rejected at compile time.
- **Deterministic Guarantee**: Only patterns whose submatch extraction can be perfectly "burned" into a deterministic table are supported. If a pattern violates this, return `DFA: unsupported epsilon loop`.

### 2.8 Architectural Shortcut (Compilation Efficiency)
To minimize compilation overhead, the engine MUST use an **Architectural Shortcut** for simple patterns.
- **Skip Heavy DFA**: If a pattern is simple (NFA nodes $\le 62$, no non-greedy, **and no anchors**), the engine MUST skip the heavy DFA transition table construction and only build the `BitParallelDFA`. Patterns with anchors MUST use the table-based DFA to leverage SIMD-accelerated prefix skipping (SIMD Warp).
- **ASCII Restriction**: BP-DFA is currently optimized for ASCII-only runes (0-127). Patterns requiring multi-byte UTF-8 support (e.g., non-ASCII runes or `.`) MUST fallback to the table-based DFA, which provides mature byte-level expansion via its UTF-8 trie.

### 2.9 Bit-parallel Optimization (BP-DFA Zero-Traverse Mandate)
To maintain maximum throughput for small patterns, the BP-DFA MUST avoid all runtime NFA traversals:
- **Precalculated StartMasks**: The engine MUST precalculate `StartMasks [64]uint64` during compilation, representing the epsilon closure of the start state for all 64 possible empty-width contexts.
- **Precalculated MatchMasks**: Match detection MUST be performed using context-dependent `MatchMasks [64]uint64`, allowing $O(1)$ match verification per byte.
- **Zero-Alloc Hot Loop**: The `bitParallelExecLoop` MUST NOT perform any function calls or heap allocations, operating entirely on stack-allocated state vectors and precalculated tables.

### 2.10 Prefix-Skip Optimization (SIMD Acceleration)
- **Mandatory Prefix Extraction**: During compilation, the longest constant prefix is extracted.
- **SIMD-Accelerated Skipping**: All execution loops MUST use `bytes.Index` to rapidly skip non-matching segments.

### 2.11 Pure Go (No CGO)
- **Zero Overhead**: Native Go only. CGO is strictly prohibited.

### 2.12 Priority Normalization & Absolute Tracking
- **Priority Normalization**: During DFA construction, NFA path priorities within each state MUST be normalized.
- **Absolute Priority Tracking**: The engine MUST track cumulative priority to identify the true leftmost-first match during Phase 1.

### 2.13 Early Exit Optimization (IsBestMatch)
- **Deterministic Finality**: If a DFA state identifies a match whose priority is unbeatable (`IsBestMatch == true`), the engine MUST stop scanning immediately for the current start position.

### 2.14 State Explosion Protection (Configurable & Scalable)
- **Default Memory Threshold**: The DFA transition table is typically limited to **64MiB**.
- **Dynamic Offloading**: When `MaxMemory` exceeds 1GiB, the engine MUST switch the NFA path set storage from memory to a **File-based backend** to prevent OOM during massive state explorations.
- **Graceful Failure**: If a pattern exceeds the configured `MaxMemory`, return `regexp: pattern too large or ambiguous`.

### 2.15 Syntax-Level Optimization & AST Rewriting
- **Factoring**: Identical AST nodes MUST be factored out (e.g., `a*c|b*c` -> `(?:a*|b*)c`) to reduce state divergence.
- **Simplification**: Use `syntax.Simplify` and `syntax.Optimize` to normalize pattern structure.

### 2.16 DFA Construction Memory Discipline (Allocation-Free Mandate)
To ensure scalability to 10,000+ patterns, the DFA construction phase MUST adhere to strict memory discipline:
- **Allocation-Free Hot Loop**: The main build loop MUST NOT perform `make` or `append` that triggers new heap allocations. Use pre-allocated buffers (`scratchBuf`, `nextPaths`) and reuse them across iterations.
- **Pointer-Free NFA Paths**: All structures representing NFA state sets (e.g., `nfaPath`) MUST be pointer-free. This ensures binary safety for raw disk I/O (no serialization overhead) and prevents GC scanning of large state sets.
- **Allocation-Free Minimization**: DFA minimization MUST use a hash-based approach instead of string/byte serialization to eliminate OOM risks during the final optimization phase.
- **Aggressive Cache Eviction**: Internal build caches (e.g., `closureCache`) MUST have explicit size limits and eviction policies to prevent unbounded memory growth during complex pattern compilation.

### 2.19 Proven Implementation Mandates for Maximum Throughput (Field-Proven)
To maintain the 50%+ throughput gains achieved through empirical benchmarking, all execution logic MUST adhere to these Go-compiler-specific mandates:

- **Zero-Overhead Strategy Dispatch (Switch over Closure)**: The choice of execution loop MUST be performed via a `matchStrategy (uint8)` and a flat `switch` statement in `Match` and `FindSubmatchIndex`. NEVER use function pointers or closures for the primary match loop, as indirect calls incur a 5-15% performance penalty and inhibit branch prediction.
- **Hot-Loop Field Hoisting & BCE (Bounds Check Elimination)**: To minimize pointer chasing (dereferencing) in the innermost loops (e.g., `fastExecLoop`, `extendedExecLoop`), ALL required struct fields (e.g., `re.dfa`, `re.prefix`) MUST be hoisted to local variables before the loop begins. Additionally, ALL slice accesses within the loop MUST be preceded by a BCE hint (e.g., `_ = trans[len(trans)-1]`) to force the Go compiler to eliminate runtime bounds checks.
- **Go-Specific Struct Layout Heuristics**: Contrary to general optimization advice, the `Regexp` struct MUST keep larger immutable headers (`string`, `[]byte`) at the beginning and smaller control fields (`strategy`, `bool`) at the end. This layout optimizes the Go compiler's offset calculations for receiver arguments and improves register allocation in the hot path.
- **Avoid Dispatcher Fragmentation**: The `Match` and `FindSubmatchIndex` methods MUST NOT be split into `Hot` and `Slow` helpers to "encourage inlining." Empirically, keeping the dispatch logic in a single, flat method provides more efficient stack frame management and register usage for the Go compiler (gc).
- **Reject Recursive Incremental Updates**: Hot-loop optimizations that introduce branching or state-carrying (e.g., incremental context updates) MUST be avoided if they increase the complexity of the innermost loop. Simple, redundant L1-cached memory reads (e.g., `b[i]`, `b[i-1]`) are significantly cheaper than additional conditional branches in the hot path.

### 2.19 Multi-byte Warp & State Explosion Protection (Mandatory)
To maintain constant-time throughput and sub-linear memory growth for complex patterns, the engine MUST adhere to these stabilization principles:
- **Lead-Byte Warp (Jump Optimization)**: For the "any-rune" (dot) and wide character classes, the DFA MUST employ a **Warp-on-Lead-Byte** strategy. The execution engine calculates the trailing byte count $N$ from the lead byte's bit pattern and performs a pointer jump ($i += 1+N$) in a single step, bypassing intermediate DFA states.
- **Warp-Aware Tag Propagation**: When a shortcut (warp) is taken, any tags (capture boundaries) residing on the skipped NFA path MUST be bundled into the DFA transition's `PostUpdates` and re-applied at the jump destination to ensure submatch precision.
- **Priority-based NFA Path Uniquing**: During DFA construction (Subset Construction), the builder MUST strictly unique the NFA path set by `(StateID, NodeID)`. If multiple paths reach the same NFA state, only the path with the **highest priority (minimum value)** MUST be retained. This is the primary defense against state explosion in loop structures (e.g., `.*`, `a*`).
- **Warp Flag Preservation**: The `WarpStateFlag` (Bit 23) MUST be preserved during DFA minimization to ensure that optimized transitions are not reverted to byte-by-byte scanning in the final engine.

#### 2.19.1 Multi-byte Dot ('.') Determinism Mandate
To ensure DFA determinism and $O(1)$ transitions, the behavior of `.` (dot) is strictly defined as matching a single byte or a static lead-byte unit. Standard library behavior involving complex grapheme clusters or context-dependent consumption is excluded as it requires NFA-like backtracking (violating Mandate 2.5). Patterns like `^あ.う$` matching `あいう` may return false if the dot junction conflicts with lead-byte warp boundaries; this is a conscious architectural trade-off for performance.

#### 2.19.2 Calculation-Free Boundary Analysis Mandate
Junction verification for anchors (`\b`, `^`, `$`) MUST NOT employ `utf8.DecodeRune` or any iterative rune restoration. All boundary conditions must be determined solely by inspecting the bit patterns of the surrounding physical bytes (`b[i-1]`, `b[i]`). To maintain Go-compatibility with $O(1)$ performance, word boundaries (`\b`) are defined as **ASCII Word Boundaries**; multi-byte bytes (0x80+) are strictly treated as non-word characters. This ensures that boundary analysis remains a zero-allocation, constant-time operation aligned with the engine's byte-oriented philosophy.

## 3. Feature Selection Policy

### 3.1 Supported Features
- **Standard Syntax**: Support `syntax.Prog`.
- **Anchors & Boundaries**: Supported via Virtual Byte Insertion.
- **Capturing Groups**: Supported via the DFA-First Hybrid Strategy.

### 3.2 Excluded Features
- **Backreferences & Dynamic Lookaround**: Strictly excluded.
- **POSIX Semantics**: Unsupported to maintain $O(n)$.

## 4. Engineering & Validation Standards
- **Memory Accumulation Prevention**: Dispose of compiled `Regexp` objects promptly during mass testing.
- **Explicit GC Discipline**: Use `runtime.GC()` strategically after processing complex patterns to prevent OOM.
- **100% DFA Validation**: DFA match boundaries MUST strictly match the standard library's boundaries.

## 5. Coding Conventions
- **Explicit Aliasing**:
  - `regexp` -> `goregexp`
  - `regexp/syntax` -> `gosyntax`

---
**Note**: Any modification to the compilation shortcut or rescan dispatch must be validated against the **"Efficiency First, Precision Mandatory"** principle.
