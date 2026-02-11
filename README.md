# flux

**Scoped Ambient State** for Go -- a scope-local atom graph with explicit dependencies and opt-in reactivity.

State lives in the scope, not in the caller. Handlers observe -- they don't own or construct dependencies. The same graph works across HTTP servers, background jobs, CLI tools, and tests.

**Servers** -- atoms are infrastructure singletons (db pool, cache client, config). Runtime config enters the scope as tags; atoms consume it via `Required(tag)`. Contexts bound per request carry tags (tenantId, traceId) without parameter drilling. Extensions wrap every resolve/exec for logging, tracing, auth. Cleanup is guaranteed.

**Workers** -- atoms form a dependency graph (`processor <- queue <- config`). Controllers enable live config reload; invalidation cascades to dependents automatically. Workers are projections of state, not owners.

**Both** -- presets swap any atom/flow for testing or multi-tenant isolation. Tags carry runtime config; presets replace implementations. No mocks, no test-only code paths.

```
go get github.com/pumped-fn/flux
```

## How It Works

```mermaid
sequenceDiagram
    participant App
    participant Scope
    participant Ext as Extension
    participant Atom
    participant Ctx as ExecContext
    participant Child as ChildContext
    participant Flow
    participant Ctrl as Controller

    Note over App,Ctrl: (*) stable ref -- same identity until released

    %% Scope Creation & Extension Init
    App->>Scope: NewScope(ctx, WithExtensions, WithPresets, WithScopeTags, WithGC)
    Scope-->>App: Scope (sync return)

    loop each extension (sequential)
        Scope->>Ext: ext.Init(scope)
    end
    Note right of Scope: all Init() done -> scope.Ready() returns nil
    Note right of Scope: any Init() fails -> scope.Ready() returns error
    Note right of Scope: Resolve() auto-awaits scope.Ready()

    %% Observers (register before or after resolve)
    App->>Scope: scope.On(StateResolving | StateResolved | StateFailed, atom, listener)
    Scope-->>App: UnsubscribeFunc
    Note right of Scope: scope.On listens to AtomState transitions

    %% Atom Resolution
    Note right of Scope: singletons -- created once, reused across contexts. deps can include Required(tag)
    App->>Scope: Resolve(scope, atom)
    Scope->>Scope: cache hit? -> return cached
    alt preset hit
        Scope->>Scope: Preset value -> store directly, skip factory
        Scope->>Scope: PresetAtom -> resolve replacement atom instead
        Scope->>Scope: emit StateResolved (no StateResolving)
    end
    Scope->>Scope: state -> StateResolving
    Scope->>Scope: emit StateResolving -> scope.On listeners
    Scope->>Ext: WrapResolve(next, atom, scope)
    Ext->>Atom: next() -> factory(rc)
    Note right of Atom: rc.Cleanup(fn) -> stored per atom
    Note right of Atom: cleanups run LIFO on Release/Invalidate
    Atom-->>Ext: value
    Note right of Ext: ext returns value -- may transform or replace
    alt factory succeeds
        Ext-->>Scope: value (*) cached in entry
        Scope->>Scope: state -> StateResolved
        Scope->>Scope: emit StateResolved -> scope.On + ctrl.On listeners
    else factory returns error
        Atom-->>Scope: error
        Scope->>Scope: state -> StateFailed
        Scope->>Scope: emit StateFailed -> scope.On listeners (ctrl.On EventAll only)
    end

    %% Context Creation
    Note right of Scope: HTTP request, job, transaction -- groups exec calls with shared tags + guaranteed cleanup
    App->>Scope: scope.CreateContext(WithContextTags(...))
    Scope-->>App: *ExecContext

    %% Execution
    alt ExecFlow(ec, flow, input, opts...)
        Ctx->>Ctx: preset? -> PresetFlow: re-exec with replacement / PresetFlowFn: run as factory
        Ctx->>Ctx: flow.parse(input) if defined
        Ctx->>Child: create child (parent = ec, merged tags)
        Child->>Ext: WrapExec(next, flow, childCtx)
        Ext->>Flow: next() -> factory(child, input)
        Note right of Flow: child.OnClose(fn) -> fn receives cause error
        Flow-->>Ext: output
        Note right of Ext: ext returns output -- may transform or replace
        Ext-->>Child: output
    else ExecFn(ec, fn, opts...)
        Ctx->>Child: create child (parent = ec)
        Child->>Ext: WrapExec(next, fn, childCtx)
        Ext->>Child: next() -> fn(child)
        Child-->>Ext: result
    end
    Ctx->>Child: [A] child.Close(cause) -> run OnClose callbacks LIFO
    Child-->>Ctx: output
    Ctx-->>App: output

    %% Reactivity (opt-in)
    rect rgb(240, 248, 255)
        Note over App,Ctrl: Reactivity (opt-in -- atoms are static by default)
        Note right of Scope: live config, cache invalidation -- when values change after initial resolve
        App->>Scope: GetController(scope, atom)
        Scope-->>Ctrl: *Controller[T] (*)

        App->>Ctrl: ctrl.On(EventResolving | EventResolved | EventAll, listener)
        Ctrl-->>App: UnsubscribeFunc
        Note right of Ctrl: ctrl.On listens to per-atom entry events

        App->>Ctrl: ctrl.Set(v) / ctrl.Update(fn)
        Ctrl->>Scope: scheduleInvalidation
        Scope->>Scope: run atom cleanups (LIFO)
        Scope->>Scope: emit StateResolving -> scope.On + ctrl.On
        Scope->>Atom: apply new value (skip factory)
        Scope->>Scope: state -> StateResolved
        Scope->>Scope: emit StateResolved -> scope.On + ctrl.On

        App->>Ctrl: ctrl.Invalidate()
        Ctrl->>Scope: scheduleInvalidation
        Scope->>Scope: run atom cleanups (LIFO)
        Scope->>Scope: emit StateResolving -> scope.On + ctrl.On
        Scope->>Atom: re-run factory
        Scope->>Scope: state -> StateResolved
        Scope->>Scope: emit StateResolved -> scope.On + ctrl.On

        App->>Scope: Select(scope, atom, selector)
        Scope-->>App: *SelectHandle[S] { Get, Subscribe }
    end

    %% Cleanup & Teardown
    rect rgb(255, 245, 238)
        Note over App,Scope: Teardown
        App->>Ctx: ec.Close(cause) -- same as [A]
        Ctx->>Ctx: run OnClose callbacks (LIFO, idempotent via sync.Once)

        App->>Scope: Release(scope, atom)
        Scope->>Scope: run atom cleanups (LIFO)
        Scope->>Scope: remove from cache + controllers

        App->>Scope: scope.Flush()
        Note right of Scope: await pending invalidation chain

        App->>Scope: scope.Dispose()
        loop each extension
            Scope->>Ext: ext.Dispose(scope) (5s timeout)
        end
        Scope->>Scope: release all atoms, run all cleanups
    end

```

API reference: `go doc github.com/pumped-fn/flux` | Patterns: `PATTERNS.md` | Examples: `examples/`

## License

MIT
