package flux_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/pumped-fn/flux"
)

type DBConn struct {
	ID     int
	closed atomic.Bool
}

func (c *DBConn) Query(query string, args ...any) ([]map[string]any, error) {
	if c.closed.Load() {
		return nil, fmt.Errorf("connection %d is closed", c.ID)
	}
	time.Sleep(2 * time.Millisecond)
	return []map[string]any{{"id": 1, "name": "alice"}}, nil
}

func (c *DBConn) Exec(query string, args ...any) error {
	if c.closed.Load() {
		return fmt.Errorf("connection %d is closed", c.ID)
	}
	time.Sleep(2 * time.Millisecond)
	return nil
}

func (c *DBConn) Close() error {
	c.closed.Store(true)
	return nil
}

type DBPool struct {
	mu      sync.Mutex
	conns   []*DBConn
	nextID  int
	maxSize int
	closed  atomic.Bool
}

func NewDBPool(maxSize int) *DBPool {
	return &DBPool{maxSize: maxSize}
}

func (p *DBPool) Acquire() (*DBConn, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("pool is closed")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	conn := &DBConn{ID: p.nextID}
	p.conns = append(p.conns, conn)
	return conn, nil
}

func (p *DBPool) Close() error {
	p.closed.Store(true)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	return nil
}

type CacheEntry struct {
	Value     any
	ExpiresAt time.Time
}

type Cache struct {
	mu      sync.RWMutex
	entries map[string]*CacheEntry
	ttl     time.Duration
	closed  atomic.Bool
}

func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		entries: make(map[string]*CacheEntry),
		ttl:     ttl,
	}
}

func (c *Cache) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.ExpiresAt) {
		return nil, false
	}
	return entry.Value, true
}

func (c *Cache) Set(key string, value any) {
	c.mu.Lock()
	c.entries[key] = &CacheEntry{Value: value, ExpiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *Cache) Close() error {
	c.closed.Store(true)
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
	return nil
}

type OpRecord struct {
	Kind     string
	Name     string
	Input    any
	Result   any
	Error    error
	Duration time.Duration
}

type Telemetry struct {
	mu      sync.Mutex
	records []OpRecord
}

func (t *Telemetry) Record(r OpRecord) {
	t.mu.Lock()
	t.records = append(t.records, r)
	t.mu.Unlock()
}

func (t *Telemetry) Records() []OpRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]OpRecord, len(t.records))
	copy(out, t.records)
	return out
}

type ObservabilityExtension struct {
	telemetry *Telemetry
}

func (o *ObservabilityExtension) Name() string { return "observability" }

func (o *ObservabilityExtension) Init(s Scope) error {
	o.telemetry.Record(OpRecord{Kind: "init", Name: "observability"})
	return nil
}

func (o *ObservabilityExtension) WrapResolve(next func() (any, error), atom AnyAtom, s Scope) (any, error) {
	start := time.Now()
	val, err := next()
	o.telemetry.Record(OpRecord{
		Kind:     "resolve",
		Name:     atom.Name(),
		Result:   val,
		Error:    err,
		Duration: time.Since(start),
	})
	return val, err
}

func (o *ObservabilityExtension) WrapExec(next func() (any, error), target AnyExecTarget, ec *ExecContext) (any, error) {
	start := time.Now()
	val, err := next()
	o.telemetry.Record(OpRecord{
		Kind:     "exec",
		Name:     target.ExecTargetName(),
		Input:    ec.Input(),
		Result:   val,
		Error:    err,
		Duration: time.Since(start),
	})
	return val, err
}

func (o *ObservabilityExtension) Dispose(s Scope) error {
	o.telemetry.Record(OpRecord{Kind: "dispose", Name: "observability"})
	return nil
}

type OrderRequest struct {
	UserID    int
	ProductID string
	Quantity  int
}

type Order struct {
	ID        string
	UserID    int
	ProductID string
	Quantity  int
	Total     float64
	Status    string
}

type User struct {
	ID    int
	Name  string
	Email string
}

type PaymentRequest struct {
	OrderID string
	Amount  float64
	Method  string
}

type PaymentResult struct {
	TransactionID string
	Status        string
}

func TestExampleOrderProcessing(t *testing.T) {
	telemetry := &Telemetry{}

	requestIDTag := NewTag[string]("request-id")

	dbAtom := NewAtom(func(rc *ResolveContext) (*DBPool, error) {
		pool := NewDBPool(10)
		rc.Cleanup(func() error { return pool.Close() })
		return pool, nil
	}, WithAtomName("db-pool"), WithKeepAlive())

	cacheAtom := NewAtom(func(rc *ResolveContext) (*Cache, error) {
		cache := NewCache(5 * time.Minute)
		rc.Cleanup(func() error { return cache.Close() })
		return cache, nil
	}, WithAtomName("cache"), WithKeepAlive())

	userCounterAtom := NewAtom(func(rc *ResolveContext) (*atomic.Int64, error) {
		return &atomic.Int64{}, nil
	}, WithAtomName("user-query-counter"))

	// Resource: acquire a DB connection from the pool, auto-cleanup on close.
	// Defined once, used by all flows — no more manual pool.Acquire/OnClose boilerplate.
	dbConnResource := NewResource("db-conn", func(ec *ExecContext) (*DBConn, error) {
		pool := MustResolve[*DBPool](ec.Scope(), dbAtom)
		conn, err := pool.Acquire()
		if err != nil {
			return nil, err
		}
		ec.OnClose(func(error) error { return conn.Close() })
		return conn, nil
	})

	getUser := NewFlowFrom(dbConnResource,
		func(ec *ExecContext, userID int, conn *DBConn) (*User, error) {
			cache := MustResolve[*Cache](ec.Scope(), cacheAtom)
			cacheKey := fmt.Sprintf("user:%d", userID)
			if cached, ok := cache.Get(cacheKey); ok {
				return cached.(*User), nil
			}

			counter := MustResolve[*atomic.Int64](ec.Scope(), userCounterAtom)
			counter.Add(1)

			rows, err := conn.Query("SELECT * FROM users WHERE id = ?", userID)
			if err != nil {
				return nil, fmt.Errorf("query user: %w", err)
			}
			if len(rows) == 0 {
				return nil, fmt.Errorf("user %d not found", userID)
			}

			user := &User{
				ID:    rows[0]["id"].(int),
				Name:  rows[0]["name"].(string),
				Email: fmt.Sprintf("%s@example.com", rows[0]["name"]),
			}
			cache.Set(cacheKey, user)
			return user, nil
		}, WithFlowName("get-user"))

	processPayment := NewFlowFrom(dbConnResource,
		func(ec *ExecContext, req PaymentRequest, conn *DBConn) (*PaymentResult, error) {
			if err := conn.Exec("INSERT INTO payments (order_id, amount) VALUES (?, ?)", req.OrderID, req.Amount); err != nil {
				return nil, fmt.Errorf("insert payment: %w", err)
			}

			return &PaymentResult{
				TransactionID: fmt.Sprintf("txn_%s", req.OrderID),
				Status:        "completed",
			}, nil
		}, WithFlowName("process-payment"))

	createOrder := NewFlowFrom(dbConnResource,
		func(ec *ExecContext, req OrderRequest, conn *DBConn) (*Order, error) {
			user, err := ExecFlow(ec, getUser, req.UserID, WithExecName("get-user-for-order"))
			if err != nil {
				return nil, fmt.Errorf("get user: %w", err)
			}
			_ = user

			order := &Order{
				ID:        fmt.Sprintf("ord_%d_%s", req.UserID, req.ProductID),
				UserID:    req.UserID,
				ProductID: req.ProductID,
				Quantity:  req.Quantity,
				Total:     float64(req.Quantity) * 29.99,
				Status:    "pending",
			}

			if err := conn.Exec("INSERT INTO orders (id, user_id) VALUES (?, ?)", order.ID, order.UserID); err != nil {
				return nil, fmt.Errorf("insert order: %w", err)
			}

			payment, err := ExecFlow(ec, processPayment, PaymentRequest{
				OrderID: order.ID,
				Amount:  order.Total,
				Method:  "card",
			}, WithExecName("pay-for-order"))
			if err != nil {
				return nil, fmt.Errorf("payment: %w", err)
			}
			_ = payment

			order.Status = "confirmed"
			return order, nil
		}, WithFlowName("create-order"), WithFlowTags(requestIDTag.Value("test-flow")))

	scope := NewScope(context.Background(),
		WithExtensions(&ObservabilityExtension{telemetry: telemetry}),
		WithScopeTags(requestIDTag.Value("default-request")),
	)
	defer func() { _ = scope.Dispose() }()

	if err := scope.Ready(); err != nil {
		t.Fatal(err)
	}

	ec := scope.CreateContext(WithContextTags(requestIDTag.Value("req-001")))

	order, err := ExecFlow(ec, createOrder, OrderRequest{
		UserID:    42,
		ProductID: "widget-x",
		Quantity:  3,
	})
	if err != nil {
		t.Fatal(err)
	}
	ec.Close(nil)

	if order.ID != "ord_42_widget-x" {
		t.Errorf("unexpected order ID: %s", order.ID)
	}
	if order.Status != "confirmed" {
		t.Errorf("unexpected order status: %s", order.Status)
	}
	if order.Total != 89.97 {
		t.Errorf("unexpected total: %f", order.Total)
	}

	records := telemetry.Records()

	hasInit := false
	resolveCount := 0
	execCount := 0
	for _, r := range records {
		switch r.Kind {
		case "init":
			hasInit = true
		case "resolve":
			resolveCount++
			if r.Duration <= 0 {
				t.Errorf("resolve %q has zero duration", r.Name)
			}
		case "exec":
			execCount++
			if r.Duration <= 0 {
				t.Errorf("exec %q has zero duration", r.Name)
			}
		}
	}

	if !hasInit {
		t.Error("missing init record")
	}
	if resolveCount == 0 {
		t.Error("no resolve records captured")
	}
	if execCount < 3 {
		t.Errorf("expected at least 3 exec records (create-order, get-user, process-payment), got %d", execCount)
	}

	execNames := make(map[string]bool)
	for _, r := range records {
		if r.Kind == "exec" {
			execNames[r.Name] = true
		}
	}
	for _, expected := range []string{"create-order", "get-user", "process-payment"} {
		if !execNames[expected] {
			t.Errorf("missing exec record for %q", expected)
		}
	}

	counter := MustResolve[*atomic.Int64](scope, userCounterAtom)
	if counter.Load() != 1 {
		t.Errorf("expected 1 user query, got %d", counter.Load())
	}

	// Verify resource cleanup: after ec.Close, connections from ec should be closed.
	pool := MustResolve[*DBPool](scope, dbAtom)
	pool.mu.Lock()
	closedAfterFirst := 0
	for _, c := range pool.conns {
		if c.closed.Load() {
			closedAfterFirst++
		}
	}
	connsAfterFirst := len(pool.conns)
	pool.mu.Unlock()
	if closedAfterFirst == 0 {
		t.Error("expected at least one connection closed after ec.Close — resource cleanup not working")
	}

	ec2 := scope.CreateContext()
	order2, err := ExecFlow(ec2, createOrder, OrderRequest{
		UserID:    42,
		ProductID: "gadget-y",
		Quantity:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ec2.Close(nil)

	// Verify resource isolation: ec2 should have acquired new connections.
	pool.mu.Lock()
	connsAfterSecond := len(pool.conns)
	pool.mu.Unlock()
	if connsAfterSecond <= connsAfterFirst {
		t.Errorf("expected new connections for ec2 (resource isolation), conns before=%d after=%d", connsAfterFirst, connsAfterSecond)
	}

	if counter.Load() != 1 {
		t.Errorf("expected 1 user query (second was cache hit), got %d", counter.Load())
	}
	if order2.Status != "confirmed" {
		t.Errorf("unexpected order2 status: %s", order2.Status)
	}

	// Verify dependency graph shows resources as static deps.
	for _, f := range []*Flow[OrderRequest, *Order]{createOrder} {
		deps := f.Deps()
		if len(deps) == 0 {
			t.Errorf("flow %q should declare resource deps in Deps()", f.Name())
		}
		found := false
		for _, d := range deps {
			if d.Name() == "db-conn" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("flow %q should have db-conn resource in Deps()", f.Name())
		}
	}

	_ = scope.Dispose()

	records = telemetry.Records()
	hasDispose := false
	for _, r := range records {
		if r.Kind == "dispose" {
			hasDispose = true
		}
	}
	if !hasDispose {
		t.Error("missing dispose record — extension dispose not captured")
	}

	t.Logf("--- Telemetry Report ---")
	for _, r := range records {
		switch r.Kind {
		case "resolve":
			status := "ok"
			if r.Error != nil {
				status = r.Error.Error()
			}
			t.Logf("[%s] %s → %s (%s)", r.Kind, r.Name, status, r.Duration)
		case "exec":
			status := "ok"
			if r.Error != nil {
				status = r.Error.Error()
			}
			t.Logf("[%s] %s input=%v → %s (%s)", r.Kind, r.Name, r.Input, status, r.Duration)
		default:
			t.Logf("[%s] %s", r.Kind, r.Name)
		}
	}
}
