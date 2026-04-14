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

### 2.5 Submatch Extraction Architecture (DFA-First Hybrid)
The engine follows a **DFA-First Hybrid** strategy to guarantee both performance and Go-compatible precision.

- **Phase 1: Boundary Discovery**: High-speed DFA scan or Bit-parallel scan determines the match boundaries `[start, end]`. For unanchored searches, the execution loop performs manual restarts at each position, utilizing `bytes.Index` to skip ahead whenever a constant prefix is available.
- **Phase 2: Strategy Dispatch**:
    - **Literal Template (O(1))**: For 0-Pass matches, submatches are applied via a pre-calculated relative offset template.
    - **Principal (DFA Rescan)**: For all patterns, a second DFA pass (rescan) is used to extract submatches deterministically.
    - **Winning Bit Selection (BP-DFA Handover)**: To resolve greedy match ambiguity (e.g., `a*`), the engine hands over the final DFA state to the BP-DFA at the `end` position. The BP-DFA acts as a tie-breaker, selecting the highest priority NFA path (winning bit) to finalize the capture registers.
- **Zero-NFA Mandate**: The engine MUST NOT fall back to a backtracking or thread-managed NFA for submatch extraction. All cases must be handled via the DFA/BP-DFA hybrid strategy to maintain $O(n)$ performance and eliminate runtime memory allocations.

### 2.6 Isolated Bit-parallel DFA (BP-DFA)
For patterns with 64 or fewer NFA nodes, the engine utilizes a specialized Bit-parallel implementation.
- **Physical Separation**: BP-DFA data (bitmasks, epsilons) MUST be stored in a dedicated `BitParallelDFA` structure, physically isolated from the primary table-based `DFA`.
- **Zero Memory Load Transitions**: Transitions must be performed using `uint64` bitwise operations.
- **L1 Cache Optimization**: BP-DFA utilizing a **16KB Successor Table** (`[8][256]uint64`) ensures that state transitions stay within the L1D cache. The transition loop MUST use 8-bit chunk lookups to achieve $O(1)$ performance per byte.
- **Context-Aware Anchor Resolution**: BP-DFA utilizes pre-compiled **`ContextMasks`** to resolve all 6 types of anchors (`^`, `$`, `\b`, etc.) via a single bitwise AND operation, eliminating branching in the hot loop.
- **Winning Bit Arbiter (2-Pass)**: In the 2-pass strategy, BP-DFA acts as the final tie-breaker. It converts the terminal DFA state into a bitmask to identify the highest-priority NFA path at the exact match end, resolving the "1-byte ambiguity" of greedy loops without NFA rescan.
- **Priority Tracking Challenge**: Since Go's `syntax.Prog` optimizes for shared prefixes (e.g., `aa|a` -> `a(a|)`), the BP-DFA cannot naturally distinguish submatch priority using only bitsets. If strict leftmost-first priority is required for overlapping paths, the engine MUST fallback to the table-based DFA for the rescan path.

### 2.7 Architectural Shortcut (Compilation Efficiency)
To minimize compilation overhead, the engine MUST use an **Architectural Shortcut** for simple patterns.
- **Skip Heavy DFA**: If a pattern is simple (NFA nodes $\le 62$, no non-greedy), the engine MUST skip the heavy DFA transition table construction and only build the `BitParallelDFA`.
- **Priority Safety Guard**: The shortcut is restricted to patterns where alternative priorities do not clash.
    - **Heuristic**: Allow simple greedy loops (back-edge Alts) but **exclude forward-pointing alternations** (`a|b`) and **non-greedy branches** to guarantee 100% Go submatch compatibility.
    - **ASCII Restriction**: BP-DFA is currently optimized for ASCII-only runes (0-127). Patterns requiring multi-byte UTF-8 support (e.g., non-ASCII runes or `.`) MUST fallback to the table-based DFA, which provides mature byte-level expansion via its UTF-8 trie.
    - For complex alternations or multi-byte matching, always prefer the DFA path to guarantee 100% Go compatibility.

### 2.8 Prefix-Skip Optimization (SIMD Acceleration)
- **Mandatory Prefix Extraction**: During compilation, the longest constant prefix is extracted.
- **SIMD-Accelerated Skipping**: All execution loops MUST use `bytes.Index` to rapidly skip non-matching segments.

### 2.9 Pure Go (No CGO)
- **Zero Overhead**: Native Go only. CGO is strictly prohibited.

### 2.10 Priority Normalization & Absolute Tracking
- **Priority Normalization**: During DFA construction, NFA path priorities within each state MUST be normalized.
- **Absolute Priority Tracking**: The engine MUST track cumulative priority to identify the true leftmost-first match during Phase 1.

### 2.11 Early Exit Optimization (IsBestMatch)
- **Deterministic Finality**: If a DFA state identifies a match whose priority is unbeatable (`IsBestMatch == true`), the engine MUST stop scanning immediately for the current start position.

### 2.12 State Explosion Protection (Configurable & Scalable)
- **Default Memory Threshold**: The DFA transition table is typically limited to **64MiB**.
- **Dynamic Offloading**: When `MaxMemory` exceeds 1GiB, the engine MUST switch the NFA path set storage from memory to a **File-based backend** to prevent OOM during massive state explorations.
- **Graceful Failure**: If a pattern exceeds the configured `MaxMemory`, return `regexp: pattern too large or ambiguous`.

### 2.13 Syntax-Level Optimization & AST Rewriting
- **Factoring**: Identical AST nodes MUST be factored out (e.g., `a*c|b*c` -> `(?:a*|b*)c`) to reduce state divergence.
- **Simplification**: Use `syntax.Simplify` and `syntax.Optimize` to normalize pattern structure.

### 2.14 DFA Construction Memory Discipline (Allocation-Free Mandate)
To ensure scalability to 10,000+ patterns, the DFA construction phase MUST adhere to strict memory discipline:
- **Allocation-Free Hot Loop**: The main build loop MUST NOT perform `make` or `append` that triggers new heap allocations. Use pre-allocated buffers (`scratchBuf`, `nextPaths`) and reuse them across iterations.
- **Pointer-Free NFA Paths**: All structures representing NFA state sets (e.g., `nfaPath`) MUST be pointer-free. This ensures binary safety for raw disk I/O (no serialization overhead) and prevents GC scanning of large state sets.
- **Allocation-Free Minimization**: DFA minimization MUST use a hash-based approach instead of string/byte serialization to eliminate OOM risks during the final optimization phase.
- **Aggressive Cache Eviction**: Internal build caches (e.g., `closureCache`) MUST have explicit size limits and eviction policies to prevent unbounded memory growth during complex pattern compilation.

### 2.15 Zero-Overhead Execution (Manual Monomorphization Mandate)
To achieve the goal of $O(1)$ performance per byte without hidden overhead, the engine MUST adhere to these execution principles:
- **Avoid Runtime Generic/Interface Dispatch**: Go's current implementation of generics often uses `GCShape` sharing with runtime dictionaries, and interfaces introduce `itab` lookups. For the primary match loops (Phase 1) and submatch extraction (Phase 2), these introduce unacceptable latency. The engine MUST use specialized, non-generic, and concrete-struct-based functions for the "Literal Path", "Fast Path", and "Extended Path".
- **Allocation-Free Rescan Strategy (Phase 2)**: For the rescan phase, the engine utilizes a stack-based approach with pre-allocated registers. By avoiding NFA thread management and heap-allocated buffers, it ensures zero GC overhead.
- **Constant Folding of Strategy**: Branches based on pattern traits (e.g., `hasAnchors`) MUST be resolved at the function dispatch level (via `bindMatchStrategy`), ensuring the loop body itself is free of irrelevant checks.
- **Anchor Usage Masking**: The engine MUST track `UsedAnchors` in the DFA to skip context calculation (`CalculateContext`) at positions where the specific anchors in the pattern cannot possibly match, further reducing CPU cycles.

### 2.16 Proven Implementation Mandates for Maximum Throughput (Field-Proven)
To maintain the 50%+ throughput gains achieved through empirical benchmarking, all execution logic MUST adhere to these Go-compiler-specific mandates:

- **Zero-Overhead Strategy Dispatch (Switch over Closure)**: The choice of execution loop MUST be performed via a `matchStrategy (uint8)` and a flat `switch` statement in `Match` and `FindSubmatchIndex`. NEVER use function pointers or closures for the primary match loop, as indirect calls incur a 5-15% performance penalty and inhibit branch prediction.
- **Hot-Loop Field Hoisting & BCE (Bounds Check Elimination)**: To minimize pointer chasing (dereferencing) in the innermost loops (e.g., `fastExecLoop`, `extendedExecLoop`), ALL required struct fields (e.g., `re.dfa`, `re.prefix`) MUST be hoisted to local variables before the loop begins. Additionally, ALL slice accesses within the loop MUST be preceded by a BCE hint (e.g., `_ = trans[len(trans)-1]`) to force the Go compiler to eliminate runtime bounds checks.
- **Go-Specific Struct Layout Heuristics**: Contrary to general optimization advice, the `Regexp` struct MUST keep larger immutable headers (`string`, `[]byte`) at the beginning and smaller control fields (`strategy`, `bool`) at the end. This layout optimizes the Go compiler's offset calculations for receiver arguments and improves register allocation in the hot path.
- **Avoid Dispatcher Fragmentation**: The `Match` and `FindSubmatchIndex` methods MUST NOT be split into `Hot` and `Slow` helpers to "encourage inlining." Empirically, keeping the dispatch logic in a single, flat method provides more efficient stack frame management and register usage for the Go compiler (gc).
- **Reject Recursive Incremental Updates**: Hot-loop optimizations that introduce branching or state-carrying (e.g., incremental context updates) MUST be avoided if they increase the complexity of the innermost loop. Simple, redundant L1-cached memory reads (e.g., `b[i]`, `b[i-1]`) are significantly cheaper than additional conditional branches in the hot path.

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
