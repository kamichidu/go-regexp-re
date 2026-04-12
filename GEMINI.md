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
- **Flattened Memory Layout**: Transition tables MUST be stored as a contiguous `int32` array. Access must use `table[state * stride + byte]` to eliminate pointer chasing and maximize L1/L2 cache hit rates.
- **Minimize Memory Latency**: Keep core structures small enough to fit within L2/L3 caches even for large pattern sets.

### 2.4 Execution Switching Strategy
To maximize throughput, the engine MUST select the most efficient execution loop based on pattern characteristics:
- **0-Pass (Literal Bypass)**: Selected for pure constant strings. Bypasses all regex engines using SIMD-accelerated standard library search (e.g., `bytes.Index`).
- **Bit-parallel Path (Glushkov BP-DFA)**: The **"Express Pass"** for patterns with 64 or fewer NFA instructions, no anchors, and no non-greedy operators. Utilizes ultra-fast `uint64` bitwise operations to eliminate memory loads.
- **Fast Path (Pure DFA)**: Automatically selected for larger anchor-free patterns. It utilizes a minimalist table-based execution loop with zero boundary/context checks.
- **Extended Path (Virtual Byte Insertion)**: Selected for patterns with anchors (e.g., `^`, `$`, `\b`). It employs "Virtual Bytes" (indices 256+) injected at character boundaries.
- **Submatch Path (Hybrid 2-Pass)**: Selected when submatches are requested. It utilizes a high-speed DFA/BP scan to identify match boundaries, followed by an optimized NFA rescan for precise submatch extraction.

### 2.5 Submatch Extraction Architecture (Hybrid 2-Pass Strategy)
The engine adopts a **Hybrid 2-Pass** strategy as its definitive strategy for submatch extraction, balancing DFA execution speed with NFA coordinate precision.

- **Phase 1: DFA/BP Boundary Scan**: A high-speed DFA (table-based) or Bit-parallel engine identifies the overall match boundaries `[start, end]` and determines the winning **Absolute Priority** using leftmost-first semantics.
- **Phase 2: Targeted NFA Rescan**: An optimized NFA rescans ONLY within the identified `[start, end]` bounds. This eliminates "1-byte Index Dragging" and coordinate mismatches inherent in pure DFA tagging.
- **Separation of Concerns**: The DFA/BP is responsible for the deterministic determination of "Match Existence" and "Overall Boundaries," while the NFA is responsible for the precise extraction of "Internal Tag Positions" within that confirmed range.

### 2.6 Bit-parallel DFA (BP-DFA) Implementation
For small patterns (NFA nodes $\le 64$), the engine MUST prioritize Bit-parallel execution:
- **Zero Memory Load**: State transitions must be performed using `uint64` bitwise OR/AND/SHIFT against pre-computed `bpCharMasks`.
- **Leftmost-First Bit Tracking**: Start positions for each bit (NFA thread) must be tracked using a fixed-size `[64]int` array to maintain leftmost-first semantics without heap allocation.
- **O(1) Priority Selection**: The winning path must be identified using `bits.TrailingZeros64` on the result of `state & matchMask`.

### 2.7 Prefix-Skip Optimization (SIMD Acceleration)
- **Mandatory Prefix Extraction**: During compilation, the longest constant prefix is extracted.
- **SIMD-Accelerated Skipping**: All execution loops (DFA, BP, and 0-Pass) MUST use `bytes.Index` to rapidly skip non-matching segments.

### 2.8 Literal Match Bypass (0-Pass Strategy)
- **Direct Literal Resolution**: If the entire pattern is a constant literal and no capturing groups are present, the engine MUST completely bypass all DFA/BP stages and use `bytes.Index` directly.

### 2.9 Pure Go (No CGO)
- **Zero Overhead**: CGO is strictly prohibited to avoid context-switching overhead and maintain Go's native portability.

### 2.10 Priority Normalization & Absolute Tracking
To achieve Go-compatible leftmost-first matching without state explosion:
- **Priority Normalization**: During DFA construction, NFA path priorities within each state MUST be normalized (subtracting the minimum priority).
- **Absolute Priority Tracking**: The engine MUST track the cumulative priority (including `SearchRestartPenalty`) to identify the true leftmost-first match when multiple potential start positions exist in a single scan.

### 2.11 Early Exit Optimization (IsBestMatch)
- **Deterministic Finality**: If a DFA state identifies a match whose priority is unbeatable by any other active path (`IsBestMatch == true`), the engine MUST stop scanning for the current start position. This is critical for non-greedy patterns (`*?`).

### 2.12 State Explosion Protection (64MiB Absolute Limit)
- **Memory Threshold**: The DFA transition table is strictly limited to a maximum estimated size of **64MiB**.
- **Graceful Failure**: If a pattern exceeds this limit, return the error `regexp: pattern too large or ambiguous`. Do NOT fall back to NFA for the first pass; the engine must maintain $O(n)$ time guarantees via DFA.

### 2.13 Syntax-Level Optimization & AST Rewriting
Before DFA compilation, the syntax tree MUST be optimized to reduce redundancy and mitigate state explosion.
- **Prefix/Suffix Factoring**: Identical AST nodes MUST be factored out (e.g., `a*c|b*c` -> `(?:a*|b*)c`) to reduce ambiguity and state divergence.
- **Simplification**: Use `syntax.Simplify` and `syntax.Optimize` to normalize the pattern structure before generating instructions.

## 3. Feature Selection Policy (Performance over Features)

### 3.1 Supported Features
- **Standard Syntax Compatibility**: Accept `syntax.Prog` instruction sequences from the standard Go parser.
- **Anchors & Boundaries**: Support via the **Virtual Byte Insertion** mechanism.
- **Capturing Groups**: Supported via the **Hybrid 2-Pass Strategy**.

### 3.2 Excluded Features
- **Backreferences**: Strictly excluded to maintain $O(n)$ complexity.
- **Dynamic Lookaround**: Restricted to maintain $O(n)$ and prevent backtracking.
- **POSIX Semantics**: Leftmost-longest matching is explicitly unsupported.

## 4. Engineering & Validation Standards
- **Memory Accumulation Prevention**: During mass testing (thousands of patterns), compiled `Regexp` objects must be disposed of promptly. Do NOT use persistent global caches for unique test patterns.
- **Explicit GC Discipline**: In test suites and heavy build processes, invoke `runtime.GC()` strategically after processing complex patterns to prevent OOM Killer intervention.
- **100% DFA Validation**: DFA match boundaries MUST strictly match the standard library's leftmost-first boundaries. Discrepancies are bugs in DFA construction (tag/priority logic) or AST optimization.

## 5. Coding Conventions
- **Explicit Aliasing for Standard Regexp Packages**: 
  - `regexp` must be imported as `goregexp`.
  - `regexp/syntax` must be imported as `gosyntax`.

---
**Note**: If a user request involves changing the 64MiB limit or re-introducing persistent caches that risk OOM, you MUST highlight the impact on system stability and resource mandates before proceeding.
