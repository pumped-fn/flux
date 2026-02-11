# Patterns

Usage patterns as sequences. For API details, see Go doc.

## A. Fundamental Usage

### Request Lifecycle

Model a request boundary with cleanup and shared context.

```mermaid
sequenceDiagram
    participant App
    participant Scope
    participant EC as ExecContext
    participant Flow

    App->>Scope: NewScope(ctx, opts...)
    App->>Scope: scope.CreateContext(WithContextTags(...))
    Scope-->>App: ec

    App->>EC: ExecFlow(ec, flow, input)
    EC->>Flow: factory(childEC, input)
    Flow-->>EC: (Out, error)
    EC->>EC: child.Close(err)
    EC-->>App: (Out, error)

    App->>EC: ec.OnClose(func(error) error { cleanup })
    App->>EC: ec.Close(cause)
    EC->>EC: run OnClose handlers LIFO
```

### Extensions Pipeline

Observe and wrap atoms/flows — logging, auth, tracing, transaction boundaries. Extensions register `OnClose(func(error) error)` to finalize based on success or failure.

```mermaid
sequenceDiagram
    participant App
    participant Scope
    participant Ext as Extension
    participant Atom
    participant EC as ExecContext
    participant Flow

    App->>Scope: NewScope(ctx, WithExtensions(ext))
    Scope->>Ext: ext.Init(scope)
    App->>Scope: scope.Ready()

    App->>Scope: Resolve(scope, atom)
    Scope->>Ext: WrapResolve(next, atom, scope)
    Ext->>Ext: before logic
    Ext->>Atom: next()
    Atom-->>Ext: (value, error)
    Ext->>Ext: after logic
    Ext-->>Scope: (value, error)

    App->>EC: ExecFlow(ec, flow, input)
    EC->>Ext: WrapExec(next, target, childEC)
    Ext->>Ext: ec.OnClose(func(err) { if err == nil: commit else: rollback })
    Ext->>Flow: next()
    Flow-->>Ext: (output, error)
    Ext-->>EC: (output, error)

    App->>Scope: scope.Dispose()
    Scope->>Ext: ext.Dispose(scope)
```

### Scoped Isolation + Testing

Swap implementations and isolate tenants/tests.

```mermaid
sequenceDiagram
    participant Test
    participant Scope
    participant Atom

    Test->>Scope: NewScope(ctx, WithPresets(Preset(dbAtom, mockDb)), WithScopeTags(tenantTag.Value(id)))
    Test->>Scope: Resolve(scope, dbAtom)
    Scope-->>Test: mockDb (not real db)

    Test->>Scope: scope.CreateContext()
    Scope-->>Test: ec with tenantTag
```

## B. Advanced Usage

### Controller Reactivity

Mutable state with lifecycle hooks and invalidation.

```mermaid
sequenceDiagram
    participant App
    participant Scope
    participant Ctrl as Controller
    participant Atom

    App->>Scope: GetController(scope, atom)
    Scope-->>App: ctrl

    App->>Ctrl: ctrl.On(EventResolving | EventResolved | EventAll, listener)
    Ctrl-->>App: UnsubscribeFunc

    App->>Ctrl: ctrl.Get()
    Ctrl-->>App: (value, error)

    App->>Ctrl: ctrl.Set(newValue)
    Ctrl->>Ctrl: schedule invalidation, notify listeners
    Ctrl-->>App: error

    App->>Ctrl: ctrl.Update(func(v T) T { return v + 1 })
    Ctrl->>Ctrl: schedule invalidation, notify listeners
    Ctrl-->>App: error

    App->>Ctrl: ctrl.Invalidate()
    Ctrl->>Atom: re-run factory
    Ctrl->>Ctrl: notify listeners
```

### Ambient Context (Tags)

Propagate values without wiring parameters. Tags serve two roles: scope-level config (consumed by atoms via `Required(tag)`) and per-context ambient data (requestId, locale). Use `Required(tag)` as an atom dependency to declare that an atom or flow needs an ambient value — extensions or context setup provide the value, the consumer just depends on it.

```mermaid
sequenceDiagram
    participant App
    participant Scope
    participant Atom
    participant EC as ExecContext
    participant ChildEC
    participant Data as ec.Data()

    App->>Scope: NewScope(ctx, WithScopeTags(configTag.Value(cfg)))
    App->>Scope: Resolve(scope, dbAtom)
    Note right of Atom: dep: Required(configTag)
    Scope->>Atom: factory(rc) resolves Required(configTag) → cfg

    App->>Scope: scope.CreateContext(WithContextTags(requestIdTag.Value(rid)))
    Scope-->>App: ec

    App->>EC: ExecFlow(ec, flow, input, WithExecTags(localeTag.Value("en")))
    EC->>ChildEC: create with merged tags

    ChildEC->>Data: SeekTag(ec.Data(), requestIdTag)
    Data-->>ChildEC: rid (from parent)

    ChildEC->>Data: GetTag(ec.Data(), localeTag)
    Data-->>ChildEC: "en"
```

### Derived State (Select)

Subscribe to a slice of atom state with custom equality.

```mermaid
sequenceDiagram
    participant App
    participant Scope
    participant Handle as SelectHandle
    participant Atom

    App->>Scope: Select(scope, atom, func(v T) S { return v.Count })
    Scope-->>App: handle

    App->>Handle: handle.Get()
    Handle-->>App: selected value

    App->>Handle: handle.Subscribe(listener)
    Handle-->>App: UnsubscribeFunc

    Note over Atom,Handle: atom changes
    Handle->>Handle: eq(prev, next)?
    Handle->>App: notify if changed
```

### Typed Flow Dependencies (NewFlowFrom2)

Resolve atom dependencies automatically before the flow factory runs.

```mermaid
sequenceDiagram
    participant App
    participant EC as ExecContext
    participant ChildEC
    participant Flow
    participant Dep1 as Atom[D1]
    participant Dep2 as Atom[D2]

    Note over Flow: NewFlowFrom2(dep1, dep2, factory, opts...)
    App->>EC: ExecFlow(ec, flow, input)
    EC->>ChildEC: create child context

    ChildEC->>Dep1: Resolve(scope, dep1)
    Dep1-->>ChildEC: d1
    ChildEC->>Dep2: Resolve(scope, dep2)
    Dep2-->>ChildEC: d2

    ChildEC->>Flow: factory(childEC, input, d1, d2)
    Flow-->>ChildEC: (Out, error)
    ChildEC->>ChildEC: child.Close(err)
    ChildEC-->>App: (Out, error)
```

### Inline Function Execution (ExecFn)

Execute ad-hoc logic within context without defining a flow.

```mermaid
sequenceDiagram
    participant App
    participant EC as ExecContext
    participant ChildEC

    App->>EC: ExecFn(ec, func(child) (Out, error) {...}, WithExecName("name"))
    EC->>ChildEC: create child context (name + tags)
    ChildEC->>ChildEC: fn(childEC)
    ChildEC->>ChildEC: child.Close(err)
    ChildEC-->>App: (Out, error)
    Note right of EC: name makes sub-executions observable by extensions
```

### Atom Retention (GC)

Control when atoms are garbage collected or kept alive indefinitely.

```mermaid
sequenceDiagram
    participant App
    participant Scope
    participant Atom

    App->>Scope: NewScope(ctx, WithGC(GCOptions{Enabled: true, GraceMs: 3000}))

    App->>Scope: Resolve(scope, atom)
    Scope-->>App: value
    Note over Scope: no subscribers, no dependents → start grace timer

    alt WithKeepAlive()
        Note over Atom: never GC'd
    else GraceMs expires
        Scope->>Atom: release
        Atom->>Atom: run cleanups LIFO
    end

    App->>Scope: scope.Flush()
    Note over Scope: wait all pending invalidations
```
