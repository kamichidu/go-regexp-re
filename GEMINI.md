# GEMINI.md - go-regexp-re Project Constitution

This document defines the foundational principles and technical mandates for the `go-regexp-re` project. As the Gemini CLI agent, you must prioritize these instructions over general defaults for all development, refactoring, and optimization tasks.

## 1. Project Philosophy
`go-regexp-re` is a **Pure Go, high-performance DFA regular expression engine** designed to surpass the physical throughput limits of the standard `regexp` package.

- **Objective**: Achieve 5x to 100x higher throughput than Go's standard `regexp` while strictly guaranteeing $O(n)$ time complexity.
- **Vision**: To evolve the concept of `Regexp::Assemble` into a modern engine optimized for CPU cache locality and pipeline efficiency.

## 2. Core Architectural Mandates
Every implementation must adhere to these four pillars to ensure maximum performance:

### 2.1 Deterministic Finite Automaton (DFA)
- **Deterministic Transitions**: Patterns must be pre-compiled into a single transition table where `table[state][byte]` leads to exactly one state.
- **Constant Time Per Byte**: Processing cost per byte must be fixed at $O(1)$, regardless of pattern complexity.

### 2.2 Byte-Oriented Scanning
- **Eliminate Rune Decoding**: Abandon mandatory UTF-8 to `rune` decoding. Scan `[]byte` directly to maximize CPU pipeline efficiency.
- **Byte-Level Transitions**: All state transitions must operate on raw bytes to minimize branching and memory latency.

### 2.3 Cache Locality Optimization
- **Flattened Memory Layout**: Transition tables MUST be stored as a single, contiguous `int32` array. Access must use `table[state * stride + byte]` to eliminate pointer chasing and maximize L1/L2 cache hit rates.
- **Minimize Memory Latency**: Keep core data structures small enough to fit within L2/L3 caches even for large pattern sets.

### 2.4 Execution Switching Strategy
To maximize throughput, the engine MUST select the most efficient execution loop based on pattern characteristics:
- **Fast Path (Pure DFA)**: Automatically selected for patterns without anchors. It utilizes a minimalist execution loop with zero boundary/context checks to approach raw memory bandwidth speeds.
- **Extended Path (Virtual Byte Insertion)**: Selected for patterns with anchors (e.g., `^`, `$`, `\b`). It employs "Virtual Bytes" (indices 256+) injected at character boundaries to process empty-width assertions within the DFA's $O(n)$ framework.
- **Submatch Path (Transition-Embedded Tagging)**: Selected when submatches are requested. It utilizes "tags" (TagOp) embedded directly into the transition table to record capture offsets without a separate post-processing pass.

### 2.5 Transition-Embedded Tagging for Submatches
- **Static Priority Resolution**: Leftmost-first priority for multiple NFA paths is resolved during DFA construction. Only tags from the highest-priority path are stored on each DFA transition edge.
- **Register-Based Recording**: Capture group offsets are recorded into a fixed-size register array (`int` slice) during the scan. This approach maintains $O(n)$ complexity and minimizes memory allocation.
- **Zero-Cost Dispatch**: Use function variables to bind the appropriate execution loop (Match-only vs. Find-Submatch) at compile/instantiation time, avoiding conditional checks within the hot scanning loop.

### 2.6 Pure Go (No CGO)
- **Zero Overhead**: CGO is strictly prohibited to avoid context-switching overhead and maintain Go's native portability and build simplicity.

## 3. Feature Selection Policy (Performance over Features)

### 3.1 Supported Features
- **Standard Syntax Compatibility**: Accept `syntax.Prog` instruction sequences from the standard Go parser.
- **Anchors & Boundaries**: Support `^`, `$`, `\b`, `\B` and multiline anchors via the **Virtual Byte Insertion** mechanism.
- **Capturing Groups**: Support extraction by recording offsets into fixed-size register arrays using **Transition-Embedded Tagging** during the scan. This ensures $O(n)$ complexity and avoids backtracking.
- **Fixed-Length Lookahead/Lookbehind**: Support assertions that can be statically integrated into the DFA transition graph during compilation.

### 3.2 Excluded Features
- **Backreferences**: Strictly excluded to maintain $O(n)$ complexity and prevent exponential "catastrophic backtracking."
- **Dynamic Lookaround**: Complex or recursive assertions that require significant backtracking are restricted.

## 4. Engineering & Validation Standards
- **Performance-First Benchmarking**: Any change must be validated against the standard `regexp` package. Significant throughput regressions are unacceptable.
- **Scalability for Large Pattern Sets**: Ensure the engine maintains $O(n)$ performance (proportional only to input length) even when merging tens of thousands of patterns.
- **SIMD Utilization**: Proactively use fast-skipping logic (e.g., `bytes.Index`) for pattern prefix matching before engaging the DFA.

## 5. Coding Conventions
- **Explicit Aliasing for Standard Regexp Packages**: To avoid confusion between this engine and the standard library, always use explicit aliases when importing Go's standard `regexp` packages:
  - `regexp` must be imported as `goregexp`.
  - `regexp/syntax` must be imported as `gosyntax`.

---
**Note**: If a user request contradicts these principles, you MUST highlight the conflict and explain the potential performance impact before proceeding.
