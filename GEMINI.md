# GEMINI.md - go-regexp-re Project Constitution

This document defines the foundational principles and technical mandates for the `go-regexp-re` project. As the Gemini CLI agent, you must prioritize these instructions over general defaults for all development, refactoring, and optimization tasks.

## 1. Project Philosophy
`go-regexp-re` is a **Pure Go, high-performance DFA regular expression engine** designed to surpass the physical throughput limits of the standard `regexp` package.

- **Objective**: Achieve 5x to 100x higher throughput than Go's standard `regexp` while strictly guaranteeing $O(n)$ time complexity.
- **Vision**: To evolve the concept of `Regexp::Assemble` into a modern engine optimized for CPU cache locality and pipeline efficiency.

## 2. Core Architectural Mandates
Every implementation must adhere to these pillars to ensure maximum performance:

### 2.1 Deterministic Finite Automaton (DFA)
- **Deterministic Transitions**: Patterns must be pre-compiled into a single transition table where `table[state][byte]` leads to exactly one state.
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
- **Fast Path (Pure DFA)**: Automatically selected for anchor-free patterns. It utilizes a minimalist execution loop with zero boundary/context checks.
- **Extended Path (Virtual Byte Insertion)**: Selected for patterns with anchors (e.g., `^`, `$`, `\b`). It employs "Virtual Bytes" (indices 256+) injected at character boundaries.
- **Submatch Path (Hybrid 2-Pass)**: Selected when submatches are requested. It utilizes a high-speed DFA scan to identify match boundaries, followed by an optimized NFA rescan for precise submatch extraction.

### 2.5 Submatch Extraction Architecture (Hybrid 2-Pass Strategy)
The engine adopts a **Hybrid 2-Pass** strategy as its definitive strategy for submatch extraction, balancing DFA execution speed with NFA coordinate precision.

- **Phase 1: DFA Boundary Scan**: A specialized DFA (Priority-Aware Tagged DFA) identifies the overall match boundaries `[start, end]` and determines the winning **Absolute Priority** using leftmost-first semantics.
- **Phase 2: Targeted NFA Rescan**: An optimized NFA rescans ONLY within the identified `[start, end]` bounds. This eliminates "1-byte Index Dragging" and coordinate mismatches inherent in pure DFA tagging.
- **Separation of Concerns**: The DFA is responsible for the deterministic determination of "Match Existence" and "Overall Boundaries," while the NFA is responsible for the precise extraction of "Internal Tag Positions" within that confirmed range.

### 2.6 Prefix-Skip Optimization (SIMD Acceleration)
- **Mandatory Prefix Extraction**: During compilation, the longest constant prefix is extracted.
- **SIMD-Accelerated Skipping**: All execution loops (DFA and 0-Pass) MUST use `bytes.Index` to rapidly skip non-matching segments.

### 2.7 Literal Match Bypass (0-Pass Strategy)
- **Direct Literal Resolution**: If the entire pattern is a constant literal and no capturing groups are present, the engine MUST completely bypass all DFA/NFA stages and use `bytes.Index` directly.

### 2.8 Pure Go (No CGO)
- **Zero Overhead**: CGO is strictly prohibited to avoid context-switching overhead and maintain Go's native portability.

### 2.9 Priority Normalization & Absolute Tracking
To achieve Go-compatible leftmost-first matching without state explosion:
- **Priority Normalization**: During DFA construction, NFA path priorities within each state MUST be normalized (subtracting the minimum priority) to prevent infinite state generation.
- **Absolute Priority Tracking**: The engine MUST track the cumulative priority (including `SearchRestartPenalty`) to identify the true leftmost-first match when multiple potential start positions exist in a single scan.

### 2.10 Early Exit Optimization (IsBestMatch)
- **Deterministic Finality**: If a DFA state identifies a match whose priority is unbeatable by any other active path (`IsBestMatch == true`), the engine MUST stop scanning for the current start position. This is critical for non-greedy patterns (`*?`).

### 2.11 State Explosion Protection (64MiB Absolute Limit)
- **Memory Threshold**: The DFA transition table is strictly limited to a maximum estimated size of **64MiB**.
- **Graceful Failure**: If a pattern exceeds this limit, return the error `regexp: pattern too large or ambiguous`. Do NOT fall back to NFA for the first pass; the engine must maintain $O(n)$ time guarantees via DFA.

### 2.12 Syntax-Level Optimization & AST Rewriting
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
- **POSIX Semantics**: Leftmost-longest matching is explicitly unsupported to avoid state explosion and maintain $O(n)$.

## 4. Engineering & Validation Standards
- **Memory Accumulation Prevention**: During mass testing or batch processing (thousands of patterns), compiled `Regexp` objects must be disposed of promptly. Do NOT use persistent global caches for unique or transient test patterns.
- **Explicit GC Discipline**: In test suites and memory-intensive build processes, invoke `runtime.GC()` strategically after processing complex patterns to prevent OOM Killer intervention.
- **100% DFA Validation**: DFA match boundaries MUST strictly match the standard library's leftmost-first boundaries. Any discrepancy is a bug in DFA construction (tag/priority logic) or AST optimization.

## 5. Coding Conventions
- **Explicit Aliasing for Standard Regexp Packages**:
  - `regexp` must be imported as `goregexp`.
  - `regexp/syntax` must be imported as `gosyntax`.

---
**Note**: If a user request involves changing the 64MiB limit or re-introducing persistent caches that risk OOM, you MUST highlight the impact on system stability and resource mandates before proceeding.
