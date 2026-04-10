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
- **Flattened Memory Layout**: Transition tables MUST be stored as a single, contiguous `int32` array. Access must use `table[state * stride + byte]` to eliminate pointer chasing and maximize L1/L2 cache hit rates.
- **Minimize Memory Latency**: Keep core data structures small enough to fit within L2/L3 caches even for large pattern sets.

### 2.4 Execution Switching Strategy
To maximize throughput, the engine MUST select the most efficient execution loop based on pattern characteristics:
- **0-Pass (Literal Bypass)**: Selected for pure constant strings. Bypasses all regex engines using SIMD-accelerated standard library search (e.g., `bytes.Index`).
- **Fast Path (Pure DFA)**: Automatically selected for patterns without anchors. It utilizes a minimalist execution loop with zero boundary/context checks to approach raw memory bandwidth speeds.
- **Extended Path (Virtual Byte Insertion)**: Selected for patterns with anchors (e.g., `^`, `$`, `\b`). It employs "Virtual Bytes" (indices 256+) injected at character boundaries to process empty-width assertions within the DFA's $O(n)$ framework.
- **Submatch Path (Hybrid 2-Pass)**: Selected when submatches are requested. It utilizes a high-speed DFA scan to identify match boundaries, followed by an optimized NFA second pass for precise submatch extraction.

### 2.5 Submatch Extraction Architecture (Final Hybrid 2-Pass Strategy)
The engine adopts a **Hybrid 2-Pass** architecture as its final and definitive strategy for submatch extraction. This deliberate architectural choice prioritizes system stability, code maintainability, and memory predictability over the theoretical (but practically risky) ideal of 1-pass submatch resolution (TDFA).

- **Intentional Exclusion of TDFA**: Full TDFA implementation is explicitly excluded from the project roadmap. The risk of catastrophic state explosion and the extreme implementation complexity required to mitigate it are deemed incompatible with the project's goal of a lean, high-performance engine.
- **Phase 1: DFA Boundary Scan**: A specialized DFA identifies the overall match boundaries `[start, end]`. By keeping DFA states free of internal submatch tags, we guarantee $O(n)$ execution and constant memory overhead. **DFA MUST respect NFA-style priorities (leftmost-first) to determine the correct boundary `[start, end]` compatible with the standard library.**
- **Phase 2: Targeted Byte-Oriented NFA Rescan**: An optimized NFA rescans ONLY within the identified `[start, end]` bounds.
  - **Eliminate Rune Decoding**: The NFA rescan MUST operate directly on raw bytes. Use of `utf8.DecodeRune` or any rune-based logic is strictly prohibited to maintain consistency with the DFA's performance characteristics.
  - **Bit-Parallel Optimization**: If the NFA has 64 or fewer states, the engine MUST use a bit-parallel implementation.
  - **Pike VM Fallback**: Traditional NFA for patterns exceeding machine word size, refactored for byte-level transitions.

### 2.6 Prefix-Skip Optimization (SIMD Acceleration)
To maximize throughput for patterns with literal prefixes, the engine MUST utilize a **Prefix-Skip** optimization:
- **Mandatory Prefix Extraction**: During compilation, the longest constant prefix is extracted.
- **SIMD-Accelerated Skipping**: All execution loops (DFA and 0-Pass) MUST use `bytes.Index` to rapidly skip non-matching segments.
- **Pre-calculated Prefix State**: The DFA state reached after matching the prefix (`prefixState`) is pre-calculated to allow immediate resumption of DFA execution.

### 2.7 Literal Match Bypass (0-Pass Strategy)
- **Direct Literal Resolution**: If the entire pattern is a constant literal and no capturing groups are present, the engine MUST completely bypass both DFA and NFA stages and use `bytes.Index` directly.

### 2.8 Pure Go (No CGO)
- **Zero Overhead**: CGO is strictly prohibited to avoid context-switching overhead and maintain Go's native portability and build simplicity.

### 2.9 Priority Normalization & Static Priority Resolution
To achieve Go-compatible leftmost-first matching without state explosion:
- **Priority Normalization**: During DFA construction, NFA path priorities within each state MUST be normalized (subtracting the minimum priority) to prevent infinite state generation in patterns like `a*?`.
- **Absolute Priority Tracking**: The values subtracted during normalization MUST be stored as `transPriorityIncrement` on transitions. The execution engine MUST accumulate these increments to reconstruct the **Absolute Priority** of any match, ensuring the correct leftmost-first alternative is selected across different match lengths.
- **Static Priority Resolution (Pruning)**: During `epsilonClosure` (DFA state construction), any NFA path whose priority is numerically greater (lower priority) than a confirmed match at the same position MUST be pruned. This "shadowed path" removal is critical to preventing state explosion in complex alternations.

### 2.10 Early Exit Optimization (IsBestMatch)
- **Deterministic Finality**: If a DFA state contains a match whose priority is equal to the minimum priority of all active NFA paths in that state (`IsBestMatch == true`), the engine MUST stop scanning for the current start position. This guarantees that no better priority match can be found by continuing, providing a critical performance boost for non-greedy patterns and early-exit scenarios.

### 2.11 State Explosion Protection (Resource Limits)
To ensure system stability and prevent Out-Of-Memory (OOM) conditions during compilation, the engine enforces a strict resource limit on DFA construction.
- **Memory-Based Threshold**: The DFA transition table is limited to a maximum estimated size (default 64MB, approx. 32,000 states).
- **Graceful Failure**: If a pattern is too complex or highly ambiguous (leading to state explosion), `Compile` MUST NOT crash the process. Instead, it MUST return the error `regexp: pattern too large or ambiguous`.
- **Cancellable Compilation**: Compilation supports `context.Context` via `CompileContext` to allow callers to abort resource-intensive builds.

### 2.12 DFA Minimization (Moore's Algorithm)
- **Equivalence-Based Merging**: After the initial transition table construction, the DFA MUST be minimized using Moore's algorithm (Partition Refinement).
- **Equivalence Criteria**: Two states are equivalent if and only if they share identical:
  1. Acceptance properties (Accepting, MatchPriority, IsBestMatch).
  2. Transition targets (mapped to equivalent groups).
  3. Priority increments for every possible byte.

### 2.13 Syntax-Level Factoring & Trie Optimization
Before NFA/DFA compilation, the syntax tree (especially `OpAlternate`) MUST be optimized to reduce redundancy and mitigate state explosion.
- **Prefix/Suffix Factoring**: Identical AST nodes at the beginning or end of alternative branches MUST be extracted (e.g., `a*c|b*c` -> `(?:a*|b*)c`). This unifies the exploration of common trailing or leading structures.
- **Literal Trie Optimization**: Sequences of literals within an alternation MUST be merged into a Trie-like structure (e.g., `apple|applejuice` -> `apple(?:juice|)`).
- **Semantics Preservation**: These optimizations MUST preserve the original leftmost-first priority order, ensuring compatibility with Go's standard library matching behavior.

## 3. Feature Selection Policy (Performance over Features)

### 3.1 Supported Features
- **Standard Syntax Compatibility**: Accept `syntax.Prog` instruction sequences from the standard Go parser.
- **Anchors & Boundaries**: Support `^`, `$`, `\b`, `\B` and multiline anchors via the **Virtual Byte Insertion** mechanism.
- **Capturing Groups**: Support extraction via the **Hybrid 2-Pass Strategy**.
- **Fixed-Length Lookahead/Lookbehind**: Support assertions that can be statically integrated into the DFA transition graph during compilation.

### 3.2 Excluded Features
- **Backreferences**: Strictly excluded to maintain $O(n)$ complexity and prevent exponential "catastrophic backtracking."
- **Dynamic Lookaround**: Complex or recursive assertions that require significant backtracking are restricted.
- **POSIX Semantics**: Standard Go `CompilePOSIX` and POSIX-style leftmost-longest matching are explicitly unsupported. These are **not provided in the API** to ensure compile-time detection of unsupported patterns and prevent accidental performance degradation.
- **Longest Match**: The `Longest()` method is not provided. The engine's matching priority is fixed at compile-time to maintain $O(n)$ and cache-locality mandates.

### 3.3 Interface Compatibility Policy
- **Compile-time Safety Over Runtime Panic**: For functions and methods in the standard `regexp` package that cannot be supported under our $O(n)$ and state-explosion-free mandates, we intentionally omit them from the API. This ensures that users are notified of incompatibilities at compile-time rather than encountering unexpected runtime panics or incorrect behavior.
- **Functional Completeness**: We aim to provide a compatible interface for the most commonly used features (Find, Replace, Split, etc.) while adhering to our performance-first philosophy.

## 4. Engineering & Validation Standards
- **Performance-First Benchmarking**: Any change must be validated against the standard `regexp` package. Significant throughput regressions are unacceptable.
- **Scalability for Large Pattern Sets**: Ensure the engine maintains $O(n)$ performance even when merging tens of thousands of patterns.
- **SIMD Utilization**: Proactively use fast-skipping logic (e.g., `bytes.Index`) for pattern prefix matching before engaging the DFA. Aim for 5x to 100x higher throughput.
- **Submatch Isolation Diagnostics**: When submatch discrepancies occur, use isolation tests to determine if the error lies in Phase 1 (DFA boundary detection) or Phase 2 (NFA submatch extraction). DFA boundaries MUST strictly match the standard library's leftmost-first boundaries.

## 5. Coding Conventions
- **Explicit Aliasing for Standard Regexp Packages**: To avoid confusion between this engine and the standard library, always use explicit aliases when importing Go's standard `regexp` packages:
  - `regexp` must be imported as `goregexp`.
  - `regexp/syntax` must be imported as `gosyntax`.

---
**Note**: If a user request contradicts these principles, you MUST highlight the conflict and explain the potential performance impact before proceeding.
