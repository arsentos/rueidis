package rueidis

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rueian/rueidis/internal/cmds"
)

type mockConn struct {
	DoFn       func(cmd cmds.Completed) RedisResult
	DoCacheFn  func(cmd cmds.Cacheable, ttl time.Duration) RedisResult
	DoMultiFn  func(multi ...cmds.Completed) []RedisResult
	ReceiveFn  func(ctx context.Context, subscribe cmds.Completed, fn func(message PubSubMessage)) error
	InfoFn     func() map[string]RedisMessage
	ErrorFn    func() error
	CloseFn    func()
	DialFn     func() error
	AcquireFn  func() wire
	StoreFn    func(w wire)
	OverrideFn func(c conn)
	IsFn       func(addr string) bool

	DoOverride      map[string]func(cmd cmds.Completed) RedisResult
	ReceiveOverride map[string]func(ctx context.Context, subscribe cmds.Completed, fn func(message PubSubMessage)) error
}

func (m *mockConn) Override(c conn) {
	if m.OverrideFn != nil {
		m.OverrideFn(c)
	}
}

func (m *mockConn) Dial() error {
	if m.DialFn != nil {
		return m.DialFn()
	}
	return nil
}

func (m *mockConn) Acquire() wire {
	if m.AcquireFn != nil {
		return m.AcquireFn()
	}
	return nil
}

func (m *mockConn) Store(w wire) {
	if m.StoreFn != nil {
		m.StoreFn(w)
	}
}

func (m *mockConn) Do(ctx context.Context, cmd cmds.Completed) RedisResult {
	if fn := m.DoOverride[strings.Join(cmd.Commands(), " ")]; fn != nil {
		return fn(cmd)
	}
	if m.DoFn != nil {
		return m.DoFn(cmd)
	}
	return RedisResult{}
}

func (m *mockConn) DoCache(ctx context.Context, cmd cmds.Cacheable, ttl time.Duration) RedisResult {
	if m.DoCacheFn != nil {
		return m.DoCacheFn(cmd, ttl)
	}
	return RedisResult{}
}

func (m *mockConn) DoMulti(ctx context.Context, multi ...cmds.Completed) []RedisResult {
	overrides := make([]RedisResult, 0, len(multi))
	for _, cmd := range multi {
		if fn := m.DoOverride[strings.Join(cmd.Commands(), " ")]; fn != nil {
			overrides = append(overrides, fn(cmd))
		}
	}
	if len(overrides) == len(multi) {
		return overrides
	}
	if m.DoMultiFn != nil {
		return m.DoMultiFn(multi...)
	}
	return nil
}

func (m *mockConn) Receive(ctx context.Context, subscribe cmds.Completed, hdl func(message PubSubMessage)) error {
	if fn := m.ReceiveOverride[strings.Join(subscribe.Commands(), " ")]; fn != nil {
		return fn(ctx, subscribe, hdl)
	}
	if m.ReceiveFn != nil {
		return m.ReceiveFn(ctx, subscribe, hdl)
	}
	return nil
}

func (m *mockConn) Info() map[string]RedisMessage {
	if m.InfoFn != nil {
		return m.InfoFn()
	}
	return nil
}

func (m *mockConn) Error() error {
	if m.ErrorFn != nil {
		return m.ErrorFn()
	}
	return nil
}

func (m *mockConn) Close() {
	if m.CloseFn != nil {
		m.CloseFn()
	}
}

func (m *mockConn) Is(addr string) bool {
	if m.IsFn != nil {
		return m.IsFn(addr)
	}
	return false
}

func TestNewSingleClientNoNode(t *testing.T) {
	if _, err := newSingleClient(&ClientOption{}, nil, func(dst string, opt *ClientOption) conn {
		return nil
	}); err != ErrNoAddr {
		t.Fatalf("unexpected err %v", err)
	}
}

func TestNewSingleClientError(t *testing.T) {
	v := errors.New("dail err")
	if _, err := newSingleClient(&ClientOption{InitAddress: []string{""}}, nil, func(dst string, opt *ClientOption) conn {
		return &mockConn{DialFn: func() error { return v }}
	}); err != v {
		t.Fatalf("unexpected err %v", err)
	}
}

func TestNewSingleClientOverride(t *testing.T) {
	m1 := &mockConn{}
	var m2 conn
	if _, err := newSingleClient(&ClientOption{InitAddress: []string{""}}, m1, func(dst string, opt *ClientOption) conn {
		return &mockConn{OverrideFn: func(c conn) { m2 = c }}
	}); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if m2.(*mockConn) != m1 {
		t.Fatalf("unexpected m2 %v", m2)
	}
}

//gocyclo:ignore
func TestSingleClient(t *testing.T) {
	m := &mockConn{}
	client, err := newSingleClient(&ClientOption{InitAddress: []string{""}}, m, func(dst string, opt *ClientOption) conn {
		return m
	})
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}

	t.Run("Delegate Do", func(t *testing.T) {
		c := client.B().Get().Key("Do").Build()
		m.DoFn = func(cmd cmds.Completed) RedisResult {
			if !reflect.DeepEqual(cmd.Commands(), c.Commands()) {
				t.Fatalf("unexpected command %v", cmd)
			}
			return newResult(RedisMessage{typ: '+', string: "Do"}, nil)
		}
		if v, err := client.Do(context.Background(), c).ToString(); err != nil || v != "Do" {
			t.Fatalf("unexpected response %v %v", v, err)
		}
	})

	t.Run("Delegate DoCache", func(t *testing.T) {
		c := client.B().Get().Key("DoCache").Cache()
		m.DoCacheFn = func(cmd cmds.Cacheable, ttl time.Duration) RedisResult {
			if !reflect.DeepEqual(cmd.Commands(), c.Commands()) || ttl != 100 {
				t.Fatalf("unexpected command %v, %v", cmd, ttl)
			}
			return newResult(RedisMessage{typ: '+', string: "DoCache"}, nil)
		}
		if v, err := client.DoCache(context.Background(), c, 100).ToString(); err != nil || v != "DoCache" {
			t.Fatalf("unexpected response %v %v", v, err)
		}
	})

	t.Run("Delegate Receive", func(t *testing.T) {
		c := client.B().Subscribe().Channel("ch").Build()
		hdl := func(message PubSubMessage) {}
		m.ReceiveFn = func(ctx context.Context, subscribe cmds.Completed, fn func(message PubSubMessage)) error {
			if !reflect.DeepEqual(subscribe.Commands(), c.Commands()) {
				t.Fatalf("unexpected command %v", subscribe)
			}
			return nil
		}
		if err := client.Receive(context.Background(), c, hdl); err != nil {
			t.Fatalf("unexpected response %v", err)
		}
	})

	t.Run("Delegate Close", func(t *testing.T) {
		called := false
		m.CloseFn = func() { called = true }
		client.Close()
		if !called {
			t.Fatalf("Close is not delegated")
		}
	})

	t.Run("Dedicated Err", func(t *testing.T) {
		v := errors.New("fn err")
		if err := client.Dedicated(func(client DedicatedClient) error {
			return v
		}); err != v {
			t.Fatalf("unexpected err %v", err)
		}
	})

	t.Run("Dedicated Delegate", func(t *testing.T) {
		w := &mockWire{
			DoFn: func(cmd cmds.Completed) RedisResult {
				return newResult(RedisMessage{typ: '+', string: "Delegate"}, nil)
			},
			DoMultiFn: func(cmd ...cmds.Completed) []RedisResult {
				return []RedisResult{newResult(RedisMessage{typ: '+', string: "Delegate"}, nil)}
			},
			ReceiveFn: func(ctx context.Context, subscribe cmds.Completed, fn func(message PubSubMessage)) error {
				return ErrClosing
			},
			ErrorFn: func() error {
				return ErrClosing
			},
		}
		m.AcquireFn = func() wire {
			return w
		}
		stored := false
		m.StoreFn = func(ww wire) {
			if ww != w {
				t.Fatalf("received unexpected wire %v", ww)
			}
			stored = true
		}
		if err := client.Dedicated(func(c DedicatedClient) error {
			if v, err := c.Do(context.Background(), c.B().Get().Key("a").Build()).ToString(); err != nil || v != "Delegate" {
				t.Fatalf("unexpected response %v %v", v, err)
			}
			if v := c.DoMulti(context.Background()); len(v) != 0 {
				t.Fatalf("received unexpected response %v", v)
			}
			for _, resp := range c.DoMulti(context.Background(), c.B().Get().Key("a").Build()) {
				if v, err := resp.ToString(); err != nil || v != "Delegate" {
					t.Fatalf("unexpected response %v %v", v, err)
				}
			}
			if err := c.Receive(context.Background(), c.B().Subscribe().Channel("a").Build(), func(msg PubSubMessage) {}); err != ErrClosing {
				t.Fatalf("unexpected ret %v", err)
			}
			return nil
		}); err != nil {
			t.Fatalf("unexpected err %v", err)
		}
		if !stored {
			t.Fatalf("Dedicated desn't put back the wire")
		}
	})
}

func TestSingleClientRetry(t *testing.T) {
	SetupClientRetry(t, func(m *mockConn) Client {
		c, err := newSingleClient(&ClientOption{InitAddress: []string{""}}, m, func(dst string, opt *ClientOption) conn {
			return m
		})
		if err != nil {
			t.Fatalf("unexpected err %v", err)
		}
		return c
	})
}

//gocyclo:ignore
func SetupClientRetry(t *testing.T, fn func(mock *mockConn) Client) {
	setup := func() (Client, *mockConn) {
		m := &mockConn{}
		return fn(m), m
	}

	makeDoFn := func(results ...RedisResult) func(cmd cmds.Completed) RedisResult {
		count := -1
		return func(cmd cmds.Completed) RedisResult {
			count++
			return results[count]
		}
	}

	makeDoCacheFn := func(results ...RedisResult) func(cmd cmds.Cacheable, ttl time.Duration) RedisResult {
		count := -1
		return func(cmd cmds.Cacheable, ttl time.Duration) RedisResult {
			count++
			return results[count]
		}
	}

	makeReceiveFn := func(results ...error) func(ctx context.Context, subscribe cmds.Completed, fn func(message PubSubMessage)) error {
		count := -1
		return func(ctx context.Context, subscribe cmds.Completed, fn func(message PubSubMessage)) error {
			count++
			return results[count]
		}
	}

	makeDoMultiFn := func(results ...[]RedisResult) func(multi ...cmds.Completed) []RedisResult {
		count := -1
		return func(multi ...cmds.Completed) []RedisResult {
			count++
			return results[count]
		}
	}

	t.Run("Delegate Do ReadOnly Retry", func(t *testing.T) {
		c, m := setup()
		m.DoFn = makeDoFn(
			newErrResult(ErrClosing),
			newResult(RedisMessage{typ: '+', string: "Do"}, nil),
		)
		if v, err := c.Do(context.Background(), c.B().Get().Key("Do").Build()).ToString(); err != nil || v != "Do" {
			t.Fatalf("unexpected response %v %v", v, err)
		}
	})

	t.Run("Delegate Do ReadOnly NoRetry - closed", func(t *testing.T) {
		c, m := setup()
		m.DoFn = makeDoFn(newErrResult(ErrClosing))
		c.Close()
		if v, err := c.Do(context.Background(), c.B().Get().Key("Do").Build()).ToString(); err != ErrClosing {
			t.Fatalf("unexpected response %v %v", v, err)
		}
	})

	t.Run("Delegate Do ReadOnly NoRetry - ctx done", func(t *testing.T) {
		c, m := setup()
		m.DoFn = makeDoFn(newErrResult(ErrClosing))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if v, err := c.Do(ctx, c.B().Get().Key("Do").Build()).ToString(); err != ErrClosing {
			t.Fatalf("unexpected response %v %v", v, err)
		}
	})

	t.Run("Delegate Do Write NoRetry", func(t *testing.T) {
		c, m := setup()
		m.DoFn = makeDoFn(newErrResult(ErrClosing))
		if v, err := c.Do(context.Background(), c.B().Set().Key("Do").Value("V").Build()).ToString(); err != ErrClosing {
			t.Fatalf("unexpected response %v %v", v, err)
		}
	})

	t.Run("Delegate DoCache Retry", func(t *testing.T) {
		c, m := setup()
		m.DoCacheFn = makeDoCacheFn(
			newErrResult(ErrClosing),
			newResult(RedisMessage{typ: '+', string: "Do"}, nil),
		)
		if v, err := c.DoCache(context.Background(), c.B().Get().Key("Do").Cache(), 0).ToString(); err != nil || v != "Do" {
			t.Fatalf("unexpected response %v %v", v, err)
		}
	})

	t.Run("Delegate DoCache NoRetry - closed", func(t *testing.T) {
		c, m := setup()
		m.DoCacheFn = makeDoCacheFn(newErrResult(ErrClosing))
		c.Close()
		if v, err := c.DoCache(context.Background(), c.B().Get().Key("Do").Cache(), 0).ToString(); err != ErrClosing {
			t.Fatalf("unexpected response %v %v", v, err)
		}
	})

	t.Run("Delegate DoCache ReadOnly NoRetry - ctx done", func(t *testing.T) {
		c, m := setup()
		m.DoCacheFn = makeDoCacheFn(newErrResult(ErrClosing))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if v, err := c.DoCache(ctx, c.B().Get().Key("Do").Cache(), 0).ToString(); err != ErrClosing {
			t.Fatalf("unexpected response %v %v", v, err)
		}
	})

	t.Run("Delegate Receive Retry", func(t *testing.T) {
		c, m := setup()
		m.ReceiveFn = makeReceiveFn(ErrClosing, nil)
		if err := c.Receive(context.Background(), c.B().Subscribe().Channel("ch").Build(), nil); err != nil {
			t.Fatalf("unexpected response %v", err)
		}
	})

	t.Run("Delegate Receive NoRetry - closed", func(t *testing.T) {
		c, m := setup()
		m.ReceiveFn = makeReceiveFn(ErrClosing)
		c.Close()
		if err := c.Receive(context.Background(), c.B().Subscribe().Channel("ch").Build(), nil); err != ErrClosing {
			t.Fatalf("unexpected response %v", err)
		}
	})

	t.Run("Delegate Receive NoRetry - ctx done", func(t *testing.T) {
		c, m := setup()
		m.ReceiveFn = makeReceiveFn(ErrClosing)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := c.Receive(ctx, c.B().Subscribe().Channel("ch").Build(), nil); err != ErrClosing {
			t.Fatalf("unexpected response %v", err)
		}
	})

	t.Run("Dedicate Delegate Do ReadOnly Retry", func(t *testing.T) {
		c, m := setup()
		m.DoFn = makeDoFn(
			newErrResult(ErrClosing),
			newResult(RedisMessage{typ: '+', string: "Do"}, nil),
		)
		m.AcquireFn = func() wire { return m }
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			if v, err := cc.Do(context.Background(), c.B().Get().Key("Do").Build()).ToString(); err != nil || v != "Do" {
				t.Fatalf("unexpected response %v %v", v, err)
			}
			return errors.New("done")
		}); ret.Error() != "done" {
			t.Fatalf("Dedicated not executed")
		}
	})

	t.Run("Dedicate Delegate Do ReadOnly NoRetry - broken", func(t *testing.T) {
		c, m := setup()
		m.DoFn = makeDoFn(newErrResult(ErrClosing))
		m.AcquireFn = func() wire { return m }
		m.ErrorFn = func() error { return ErrClosing }
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			return cc.Do(context.Background(), c.B().Get().Key("Do").Build()).Error()
		}); ret != ErrClosing {
			t.Fatalf("unexpected response %v", ret)
		}
	})

	t.Run("Dedicate Delegate Do ReadOnly NoRetry - ctx done", func(t *testing.T) {
		c, m := setup()
		m.DoFn = makeDoFn(newErrResult(ErrClosing))
		m.AcquireFn = func() wire { return m }
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			return cc.Do(ctx, c.B().Get().Key("Do").Build()).Error()
		}); ret != ErrClosing {
			t.Fatalf("unexpected response %v", ret)
		}
	})

	t.Run("Dedicate Delegate Do Write NoRetry", func(t *testing.T) {
		c, m := setup()
		m.DoFn = makeDoFn(newErrResult(ErrClosing))
		m.AcquireFn = func() wire { return m }
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			return cc.Do(context.Background(), c.B().Set().Key("Do").Value("Do").Build()).Error()
		}); ret != ErrClosing {
			t.Fatalf("unexpected response %v", ret)
		}
	})

	t.Run("Dedicate Delegate DoMulti ReadOnly Retry", func(t *testing.T) {
		c, m := setup()
		m.DoMultiFn = makeDoMultiFn(
			[]RedisResult{newErrResult(ErrClosing)},
			[]RedisResult{newResult(RedisMessage{typ: '+', string: "Do"}, nil)},
		)
		m.AcquireFn = func() wire { return m }
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			if v, err := cc.DoMulti(context.Background(), c.B().Get().Key("Do").Build())[0].ToString(); err != nil || v != "Do" {
				t.Fatalf("unexpected response %v %v", v, err)
			}
			return errors.New("done")
		}); ret.Error() != "done" {
			t.Fatalf("Dedicated not executed")
		}
	})

	t.Run("Dedicate Delegate DoMulti ReadOnly NoRetry - broken", func(t *testing.T) {
		c, m := setup()
		m.DoMultiFn = makeDoMultiFn([]RedisResult{newErrResult(ErrClosing)})
		m.AcquireFn = func() wire { return m }
		m.ErrorFn = func() error { return ErrClosing }
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			return cc.DoMulti(context.Background(), c.B().Get().Key("Do").Build())[0].Error()
		}); ret != ErrClosing {
			t.Fatalf("unexpected response %v", ret)
		}
	})

	t.Run("Dedicate Delegate DoMulti ReadOnly NoRetry - ctx done", func(t *testing.T) {
		c, m := setup()
		m.DoMultiFn = makeDoMultiFn([]RedisResult{newErrResult(ErrClosing)})
		m.AcquireFn = func() wire { return m }
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			return cc.DoMulti(ctx, c.B().Get().Key("Do").Build())[0].Error()
		}); ret != ErrClosing {
			t.Fatalf("unexpected response %v", ret)
		}
	})

	t.Run("Dedicate Delegate DoMulti Write NoRetry", func(t *testing.T) {
		c, m := setup()
		m.DoMultiFn = makeDoMultiFn([]RedisResult{newErrResult(ErrClosing)})
		m.AcquireFn = func() wire { return m }
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			return cc.DoMulti(context.Background(), c.B().Set().Key("Do").Value("Do").Build())[0].Error()
		}); ret != ErrClosing {
			t.Fatalf("unexpected response %v", ret)
		}
	})

	////

	t.Run("Delegate Receive Retry", func(t *testing.T) {
		c, m := setup()
		m.ReceiveFn = makeReceiveFn(ErrClosing, nil)
		m.AcquireFn = func() wire { return m }
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			if err := cc.Receive(context.Background(), c.B().Subscribe().Channel("Do").Build(), nil); err != nil {
				t.Fatalf("unexpected response %v", err)
			}
			return errors.New("done")
		}); ret.Error() != "done" {
			t.Fatalf("Dedicated not executed")
		}
	})

	t.Run("Delegate Receive NoRetry - broken", func(t *testing.T) {
		c, m := setup()
		m.ReceiveFn = makeReceiveFn(ErrClosing)
		m.AcquireFn = func() wire { return m }
		m.ErrorFn = func() error { return ErrClosing }
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			return cc.Receive(context.Background(), c.B().Subscribe().Channel("Do").Build(), nil)
		}); ret != ErrClosing {
			t.Fatalf("unexpected response %v", ret)
		}
	})

	t.Run("Delegate Receive NoRetry - ctx done", func(t *testing.T) {
		c, m := setup()
		m.ReceiveFn = makeReceiveFn(ErrClosing)
		m.AcquireFn = func() wire { return m }
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if ret := c.Dedicated(func(cc DedicatedClient) error {
			return cc.Receive(ctx, c.B().Subscribe().Channel("Do").Build(), nil)
		}); ret != ErrClosing {
			t.Fatalf("unexpected response %v", ret)
		}
	})
}
