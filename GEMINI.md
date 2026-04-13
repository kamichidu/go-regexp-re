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
- **0-Pass (Literal Bypass)**: Selected for pure constant strings and anchored literals (e.g., `^abc$`, `^abc`, `abc$`). Bypasses all regex engines using SIMD-accelerated standard library search (e.g., `bytes.Index`, `bytes.HasPrefix`). It utilizes a **Capture Template** to provide submatch indices with zero state-machine overhead.
- **Bit-parallel Path (Glushkov BP-DFA)**: The **"Express Pass"** for small, simple patterns. Utilizes ultra-fast `uint64` bitwise operations to eliminate memory loads.
- **Fast Path (Pure DFA)**: Automatically selected for larger patterns. It utilizes a minimalist table-based execution loop with **manual restarts and SIMD-accelerated prefix skipping**.
- **Anchor-Aware Guarded SIMD Warp**: Selected for patterns with anchors. Utilizes a separate `anchorTransitions` table and **guarded warp points** to allow SIMD skipping even in the presence of anchors (e.g., `^`, `$`, `\b`).

### 2.5 Submatch Extraction Architecture (DFA-First Hybrid)
The engine follows a **DFA-First Hybrid** strategy to guarantee both performance and Go-compatible precision.

- **Phase 1: Boundary Discovery**: High-speed DFA scan or Bit-parallel scan determines the match boundaries `[start, end]`. For unanchored searches, the execution loop performs manual restarts at each position, utilizing `bytes.Index` to skip ahead whenever a constant prefix is available.
- **Phase 2: Strategy Dispatch**:
    - **Literal Template (O(1))**: For 0-Pass matches, submatches are applied via a pre-calculated relative offset template.
    - **Principal (DFA Rescan)**: For non-greedy or literal-heavy patterns, a second DFA pass (rescan) is used to extract submatches deterministically.
    - **Exception (Targeted NFA Rescan)**: For patterns involving greedy operators (e.g., `a*`, `a+`) or when DFA is skipped (Bit-parallel only), an optimized NFA rescans the confirmed `[start, end]` range.
- **Priority Sync**: During DFA rescan, the engine MUST synchronize the relative priority with the absolute winner identified in Phase 1 using `<=` matching to capture all valid tag candidates.

### 2.6 Isolated Bit-parallel DFA (BP-DFA)
For patterns with 64 or fewer NFA nodes, the engine utilizes a specialized Bit-parallel implementation.
- **Physical Separation**: BP-DFA data (bitmasks, epsilons) MUST be stored in a dedicated `BitParallelDFA` structure, physically isolated from the primary table-based `DFA`.
- **Zero Memory Load Transitions**: Transitions must be performed using `uint64` bitwise operations.
- **L1 Cache Optimization**: BP-DFA utilizing a **16KB Successor Table** (`[8][256]uint64`) ensures that state transitions stay within the L1D cache. The transition loop MUST use 8-bit chunk lookups to achieve $O(1)$ performance per byte.
- **Context-Aware Anchor Resolution**: BP-DFA utilizes pre-compiled **`ContextMasks`** to resolve all 6 types of anchors (`^`, `$`, `\b`, etc.) via a single bitwise AND operation, eliminating branching in the hot loop.
- **Priority Tracking Challenge**: Since Go's `syntax.Prog` optimizes for shared prefixes (e.g., `aa|a` -> `a(a|)`), the BP-DFA cannot naturally distinguish submatch priority using only bitsets. If strict leftmost-first priority is required for overlapping paths, the engine MUST fallback to the table-based DFA.

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
