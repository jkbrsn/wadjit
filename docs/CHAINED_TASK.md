# Chained Task Design

## Problem Statement

Current Wadjit tasks are single-hit only: each task executes one operation and emits one response. Users need the ability to chain multiple operations together where later stages consume data from earlier stage responses. The primary use case is hitting the same endpoint twice in quick succession, with the second request built using data extracted from the first response.

**Key requirements**:
- Support both HTTP and WebSocket endpoints
- Optimize connection reuse between stages for low-latency multi-tap operations
- Configurable response emission (intermediate vs. final only)
- Per-stage DNS policy control
- Minimal changes to core Wadjit architecture

## Proposed Solution: ChainedTask

Introduce a new `WatcherTask` implementation that wraps multiple stages, executes them sequentially, and passes data between stages. Each stage represents a single operation (HTTP request, WebSocket message, data transformation) that can consume input from the previous stage and produce output for the next.

### Design Principles

1. **Composition over modification**: New task type, no changes to existing HTTP/WS endpoint implementations
2. **Explicit data flow**: Clear input → stage → output contract
3. **Resource efficiency**: Configurable client/connection reuse between stages
4. **Fail-fast by default**: Chain stops on first error unless configured otherwise
5. **Observability**: Rich response metadata for debugging chain execution

## Architecture

### Core Components

#### ChainedTask

```go
type ChainedTask struct {
    id     string
    stages []TaskStage
    config ChainConfig
    logger zerolog.Logger

    // Shared resources (if enabled)
    httpClient *http.Client
    wsConn     *websocket.Conn

    // State
    initialized atomic.Bool
    closed      atomic.Bool
    closeOnce   sync.Once
}

type ChainConfig struct {
    // Response emission
    EmitIntermediateResponses bool

    // Error handling
    FailFast         bool  // Stop on first error (default true)
    AccumulateErrors bool  // Collect all errors and continue

    // Resource reuse
    ReuseHTTPClient      bool
    ReuseWSConnection    bool
    CloseWSAfterChain    bool  // For persistent WS connections

    // Timeouts
    StageTimeout time.Duration  // Per-stage timeout (0 = no limit)
    ChainTimeout time.Duration  // Total chain timeout (0 = no limit)
}
```

#### TaskStage Interface

```go
// TaskStage represents a single stage in a chained task.
type TaskStage interface {
    // Execute runs the stage operation with optional input data.
    // Returns output data for next stage, response metadata, and error.
    Execute(ctx context.Context, input []byte) (output []byte, resp WatcherResponse, err error)

    // Initialize sets up stage resources (optional).
    Initialize(ctx context.Context) error

    // Close cleans up stage resources (optional).
    Close() error

    // ID returns unique identifier for this stage.
    ID() string
}
```

### Stage Implementations

#### HTTPStage

Wraps HTTP request logic with data extraction.

```go
type HTTPStage struct {
    // Endpoint configuration
    url         string  // Can contain {{.Field}} templates
    method      string
    headersFn   func(input []byte) http.Header
    bodyBuilder func(input []byte) (io.Reader, error)

    // DNS and transport
    dnsPolicy       *DNSPolicy
    dnsPolicyMgr    *dnsPolicyManager
    transport       http.RoundTripper
    client          *http.Client  // Optional: provided by ChainedTask if reusing

    // Response handling
    extractorFn func(resp *http.Response) ([]byte, error)
    readMode    OptReadMode

    // Metadata
    stageID string
}
```

**Key features**:
- URL templating: `https://api.example.com/user/{{.UserID}}/details`
- Dynamic headers/body based on input data
- Extractor function to pull data from response (JSON path, regex, custom logic)
- Full DNS policy control per stage
- Optional client injection for reuse

#### WSStage

Wraps WebSocket message logic.

```go
type WSStage struct {
    // Connection
    url       string
    mode      WSMode  // OneHitText or PersistentJSONRPC
    conn      *websocket.Conn  // Optional: provided by ChainedTask if reusing
    dialer    *websocket.Dialer

    // Message handling
    msgBuilder  func(input []byte) ([]byte, error)
    extractorFn func(response []byte) ([]byte, error)

    // For PersistentJSONRPC mode
    jsonrpcMethod string

    // Metadata
    stageID string
}
```

**Key features**:
- Connection reuse for quick multi-tap scenarios
- Message building from previous stage output
- Response extraction (for pulling specific fields)
- Both one-hit and persistent modes

#### TransformStage

Pure data transformation without I/O.

```go
type TransformStage struct {
    stageID     string
    transformFn func(input []byte) ([]byte, error)
}
```

**Use cases**:
- Format conversion (JSON → XML, etc.)
- Data filtering/validation
- Complex multi-field extraction
- Rate limiting delays between network stages

### Response Model Extensions

```go
type WatcherResponse struct {
    // ... existing fields ...

    // Chain metadata (nil for non-chained tasks)
    ChainMetadata *ChainMetadata
}

type ChainMetadata struct {
    ChainID       string      // Correlate all responses from same chain execution
    TotalStages   int         // Total number of stages in chain
    StageSequence int         // 0-indexed stage number
    StageID       string      // Identifier for this specific stage
    IsIntermediate bool       // True for non-final responses

    // Timing (see Timing Metadata section)
    StageTiming   *StageTimingData
    ChainTiming   *ChainTimingData  // Only present in final response
}

type StageTimingData struct {
    DNSLookup           time.Duration
    TCPConnection       time.Duration
    TLSHandshake        time.Duration
    RequestTimeFirstByte time.Duration
    DataTransferTime    time.Duration
    Latency             time.Duration  // Total time for this stage
}

type ChainTimingData struct {
    TotalLatency      time.Duration           // End-to-end chain execution
    StageLatencies    []time.Duration          // Per-stage latencies
    AvgStageLatency   time.Duration           // Average across network stages only
    StageTimings      map[string]*StageTimingData  // Full timing per stage ID
}
```

## Timing Metadata Strategy

Given the complexity of timing in multi-stage chains, we adopt a layered approach:

### 1. Per-Stage Timing (Always Collected)

Each stage's `WatcherResponse` includes full timing breakdown:
- DNS lookup time
- TCP connection time (if new connection)
- TLS handshake time (if applicable)
- Time to first byte
- Data transfer time
- Total stage latency

For stages reusing connections, connection establishment times will be zero.

### 2. Chain-Level Timing (Final Response Only)

The final response includes:
- **Total chain latency**: Wall-clock time from first stage start to last stage completion
- **Per-stage latencies array**: Quick overview of each stage's total time
- **Average network stage latency**: Computed across stages that performed network I/O (excludes TransformStage)
- **Full stage timing map**: Keyed by stage ID for detailed analysis

### 3. Open-Ended Extension Points

The timing structures use pointers and optional fields to allow future enhancements:
- Parallel stage timing (when we add fan-out)
- Conditional stage paths (track which branches taken)
- Retry timing (if we add per-stage retries)

**Decision deferred**: Specific aggregation methods (weighted averages, percentiles) can be added as extension methods on `ChainTimingData` without changing the core structure.

## Connection Reuse Strategy

### HTTP Client Reuse

**When `ReuseHTTPClient = true`**:
1. ChainedTask creates single `http.Client` during `Initialize()`
2. Each HTTPStage receives client reference via `SetClient(client)` method
3. Stages use provided client instead of creating their own
4. DNS policy still per-stage, but connection pool is shared
5. ChainedTask closes client during `Close()`

**Per-stage DNS policy with shared client**:
- Stage 1: DNS policy with `ForceLookup` → establishes connection
- Stage 2: DNS policy with `Static` mode → reuses connection from pool
- Stage 3: Different endpoint → new connection via same client

**Trade-off**: Shared client means shared connection pool. If stages target different endpoints, pool may grow. Consider `MaxIdleConnsPerHost` tuning.

### WebSocket Connection Reuse

**When `ReuseWSConnection = true`**:
1. First WSStage establishes connection during `Execute()`
2. Connection stored in ChainedTask
3. Subsequent WSStages check for existing connection before dialing
4. If `CloseWSAfterChain = false`, connection persists across chain iterations (cadence-based reuse)
5. If `CloseWSAfterChain = true`, connection closes after final stage completes

**Use case: Quick two-tap on same WS endpoint**:
```go
chain := NewChainedTask(
    NewWSStage(wsURL, buildQuery1),
    NewWSStage(wsURL, buildQuery2UsingResult1),
).WithConfig(ChainConfig{
    ReuseWSConnection: true,
    CloseWSAfterChain: true,  // Fresh connection each cadence
})
```

**Consideration**: For `PersistentJSONRPC` mode, connection reuse is natural. For `OneHitText` mode, reuse means the connection stays open between messages (effectively becomes persistent for chain duration).

## Error Handling

### Fail-Fast (Default)

```go
ChainConfig{
    FailFast: true,  // default
}
```

- First stage error stops chain immediately
- Returns single `WatcherResponse` with error
- Subsequent stages not executed
- Resources cleaned up immediately

### Continue on Error

```go
ChainConfig{
    FailFast: false,
    AccumulateErrors: true,
}
```

- All stages execute regardless of errors
- Each stage's error recorded in its response
- Final response includes `errors.Join()` of all errors
- Useful for diagnostics/testing

### Per-Stage Error Handling (Future)

Individual stages could have retry policies, fallback values, etc. Deferred to post-initial implementation.

## Implementation Plan

### Phase 1: Core ChainedTask with HTTP Support

**Goal**: Functional chained tasks for HTTP endpoints with basic features.

**Deliverables**:
1. Core types (`ChainedTask`, `ChainConfig`, `TaskStage` interface)
2. `HTTPStage` implementation with:
   - Basic URL templating (simple `{{.Field}}` replacement)
   - JSON extractor helper (JSONPath-like field access)
   - DNS policy per stage
   - Optional client reuse
3. Response model extensions (`ChainMetadata`, timing structures)
4. `ChainedTask.Initialize()`, `Task()`, `Close()` lifecycle
5. Basic error handling (fail-fast only)

**Testing**:
- Unit tests for `HTTPStage` execution
- Integration test: chain two HTTP stages targeting mock server
- Validate response metadata correctness
- Verify client reuse behavior

**Files to create**:
- `chained_task.go` - ChainedTask implementation
- `stage.go` - TaskStage interface and common types
- `http_stage.go` - HTTPStage implementation
- `chained_task_test.go` - Tests
- `examples/chained/` - Example usage

**Estimated effort**: 2-3 days

### Phase 2: WebSocket Support

**Goal**: Enable WS endpoints in chains with connection reuse.

**Deliverables**:
1. `WSStage` implementation with:
   - Connection establishment and reuse
   - Message building from input data
   - Response extraction
   - Support for both `OneHitText` and `PersistentJSONRPC` modes
2. `ChainConfig.ReuseWSConnection` and `CloseWSAfterChain` options
3. Integration with ChainedTask resource management

**Testing**:
- Unit tests for `WSStage`
- Integration test: chain two WS stages targeting same endpoint
- Test connection reuse across stages
- Test connection cleanup after chain completion
- Mixed HTTP + WS chain test

**Files to modify/create**:
- `ws_stage.go` - WSStage implementation
- `chained_task.go` - Add WS connection management
- `ws_stage_test.go` - Tests
- `examples/chained/ws_example.go`

**Estimated effort**: 2-3 days

### Phase 3: Refinements and Production Readiness

**Goal**: Polish, optimize, and prepare for real-world usage.

**Deliverables**:
1. **Timing metadata finalization**:
   - Implement chain-level timing aggregation
   - Add helper methods on `ChainTimingData` (average, max, etc.)
   - Validate timing accuracy with benchmarks

2. **Response emission configuration**:
   - Implement `EmitIntermediateResponses` option
   - Add filtering logic in `listenForResponses()`
   - Test intermediate vs final-only emission

3. **Advanced extractors**:
   - Regex extractor helper
   - XML/YAML extractor helpers
   - Custom extractor examples

4. **TransformStage**:
   - Implementation with pure function execution
   - Rate limiting helper (sleep between stages)
   - Data validation helpers

5. **Error handling enhancements**:
   - `AccumulateErrors` option
   - Per-stage timeout enforcement
   - Chain-level timeout enforcement

6. **Documentation**:
   - GoDoc for all public types
   - Usage guide with common patterns
   - Migration guide from single-hit tasks

**Testing**:
- End-to-end tests for all configuration combinations
- Performance benchmarks (latency overhead of chaining)
- Race detector runs (`RACE=1 make test`)
- Real-world scenario tests (using actual APIs)

**Files to modify/create**:
- `transform_stage.go` - TransformStage implementation
- `extractors.go` - Helper functions for common extraction patterns
- `chained_task.go` - Timeout and emission logic
- `docs/CHAINED_TASK_USAGE.md` - User guide
- Additional tests and examples

**Estimated effort**: 3-4 days

### Phase 4: Future Enhancements (Stretch Goals)

**Not in immediate scope, but architectural considerations**:

1. **Parallel stage execution**: Fan-out to multiple endpoints, fan-in results
2. **Conditional stages**: Execute stage based on previous result (if-then-else)
3. **Stage composition helpers**: Builders for common patterns
4. **Per-stage retries**: Automatic retry with backoff
5. **Stage result caching**: Cache expensive stage results across chain iterations
6. **Telemetry integration**: OpenTelemetry tracing for distributed chains

## API Examples

### Basic Two-Stage HTTP Chain

```go
// Query user by email, then fetch details by ID
stage1 := wadjit.NewHTTPStage(
    "https://api.example.com/users/search?email={{.Email}}",
    http.MethodGet,
).WithExtractor(wadjit.JSONExtractor("user.id"))

stage2 := wadjit.NewHTTPStage(
    "https://api.example.com/users/{{.ID}}",
    http.MethodGet,
).WithExtractor(wadjit.JSONExtractor("user"))

chain := wadjit.NewChainedTask(stage1, stage2).
    WithConfig(wadjit.ChainConfig{
        ReuseHTTPClient: true,
        EmitIntermediateResponses: false,  // Only final result
    })

watcher := wadjit.NewWatcher("user-lookup", 30*time.Second, chain)
```

### WebSocket Quick Two-Tap

```go
// Send subscribe message, then query message
stage1 := wadjit.NewWSStage(
    "wss://stream.example.com",
    wadjit.WSOneHitText,
).WithMessageBuilder(func(input []byte) ([]byte, error) {
    return []byte(`{"action":"subscribe","channel":"prices"}`), nil
})

stage2 := wadjit.NewWSStage(
    "wss://stream.example.com",
    wadjit.WSOneHitText,
).WithMessageBuilder(func(input []byte) ([]byte, error) {
    // Use data from stage1 if needed
    return []byte(`{"action":"query","symbol":"BTC"}`), nil
}).WithExtractor(wadjit.JSONExtractor("price"))

chain := wadjit.NewChainedTask(stage1, stage2).
    WithConfig(wadjit.ChainConfig{
        ReuseWSConnection: true,
        CloseWSAfterChain: true,
        EmitIntermediateResponses: false,
    })
```

### Mixed HTTP and WS Chain

```go
// Fetch auth token via HTTP, then use in WS connection
authStage := wadjit.NewHTTPStage(
    "https://api.example.com/auth",
    http.MethodPost,
).WithBody(authPayload).
  WithExtractor(wadjit.JSONExtractor("token"))

wsStage := wadjit.NewWSStage(
    "wss://stream.example.com",
    wadjit.WSPersistentJSONRPC,
).WithMessageBuilder(func(input []byte) ([]byte, error) {
    // input contains token from authStage
    token := string(input)
    return buildAuthenticatedMessage(token), nil
})

chain := wadjit.NewChainedTask(authStage, wsStage).
    WithConfig(wadjit.ChainConfig{
        StageTimeout: 10 * time.Second,
        ChainTimeout: 30 * time.Second,
    })
```

### Transform Stage for Data Processing

```go
queryStage := wadjit.NewHTTPStage(url, http.MethodGet)

transformStage := wadjit.NewTransformStage(func(input []byte) ([]byte, error) {
    // Parse, validate, reformat data
    var data map[string]interface{}
    if err := json.Unmarshal(input, &data); err != nil {
        return nil, err
    }
    // Extract and transform fields
    result := processData(data)
    return json.Marshal(result)
})

uploadStage := wadjit.NewHTTPStage(uploadURL, http.MethodPost).
    WithBodyFromInput()  // Use transform output as request body

chain := wadjit.NewChainedTask(queryStage, transformStage, uploadStage)
```

## Open Questions and Decisions

### 1. URL Templating Engine

**Question**: How powerful should the URL/message templating be?

**Options**:
- **Simple**: String replacement of `{{.Field}}` with JSON field from input
- **Go templates**: Full `text/template` support with functions, conditionals
- **Custom DSL**: Purpose-built mini-language

**Recommendation**: Start simple (option 1) in Phase 1. Can upgrade to Go templates in Phase 3 if needed. Keep it as an implementation detail of the builder functions.

### 2. Extractor Function Error Handling

**Question**: If an extractor fails to find a field, is that a stage error or should it pass empty data?

**Options**:
- **Strict**: Extractor error = stage error (fail-fast)
- **Lenient**: Missing field = empty []byte, stage succeeds
- **Configurable**: Per-stage setting

**Recommendation**: Strict by default (fail-fast). Users can write custom extractors that return default values if they want lenient behavior.

### 3. Response Size Limits

**Question**: Should we enforce limits on data passed between stages?

**Consideration**: Chaining could amplify memory usage if stage 1 returns MB of data.

**Recommendation**: Document best practices (extractors should pull only needed fields). Consider adding optional `MaxInterstageDataSize` config in Phase 3 if issues arise.

### 4. Stage Identification

**Question**: Auto-generate stage IDs or require explicit IDs?

**Recommendation**: Auto-generate using `xid` if not provided. Users can override with explicit IDs for better logging/debugging.

### 5. Backward Compatibility

**Question**: Can existing single-hit tasks interoperate with chains?

**Consideration**: A watcher could theoretically contain both `HTTPEndpoint` and `ChainedTask` items.

**Recommendation**: Yes, they should coexist. Both implement `WatcherTask` interface. No migration needed for existing code.

## Success Criteria

### Phase 1
- [ ] Can chain two HTTP requests where second uses data from first
- [ ] Response metadata includes chain information
- [ ] Tests pass with and without client reuse
- [ ] Example runs successfully

### Phase 2
- [ ] Can chain two WS messages on same connection
- [ ] Connection properly reused between stages
- [ ] Mixed HTTP+WS chains work
- [ ] Tests cover all WS modes

### Phase 3
- [ ] Timing metadata accurately represents chain execution
- [ ] Intermediate response emission configurable and working
- [ ] All timeout options enforced correctly
- [ ] Documentation complete and examples cover common patterns
- [ ] Performance benchmarks show acceptable overhead (<5% vs single-hit)

## Related Documents

- [TODO.md](../TODO.md) - Project roadmap
- [CLAUDE.md](../CLAUDE.md) - Project conventions
- Future: `docs/CHAINED_TASK_USAGE.md` - User guide (Phase 3 deliverable)

## Revision History

- 2025-11-03: Initial design document created
