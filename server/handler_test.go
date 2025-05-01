package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
	"github.com/tliron/glsp"
)

func TestLSPHandler(t *testing.T) {
	wg := sync.WaitGroup{}
	received := make(chan string, 20)
	mockHandler := &MockHandler{
		handler: func(ctx context.Context, conn *jsonrpc2.Conn, request *jsonrpc2.Request) {
			t.Logf("Received request: %s", request.Method)
			time.Sleep(5 * time.Millisecond)
			received <- request.Method
			wg.Done()
		},
	}

	// Use the new function signature that takes a concurrent method check function
	ah := newLSPHandler(mockHandler, func(method string) bool {
		return false // No concurrent methods in this test
	})

	// Test sequential execution
	for i := range 10 {
		wg.Add(1)
		ah.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
			Method: fmt.Sprintf("call-%d", i),
		})
	}

	wg.Wait()
	close(received)
	t.Log("heard all")

	var ordered []string
	for v := range received {
		ordered = append(ordered, v)
	}
	for i := range 10 {
		if ordered[i] != fmt.Sprintf("call-%d", i) {
			t.Errorf("Expected call-%d but got %v", i, ordered)
		}
	}
}

func TestConcurrentMethods(t *testing.T) {
	wg := sync.WaitGroup{}

	method1Started := make(chan struct{})
	method1Blocked := make(chan struct{})
	method1CanFinish := make(chan struct{})
	method2Started := make(chan struct{})

	mockHandler := &MockHandler{
		handler: func(ctx context.Context, conn *jsonrpc2.Conn, request *jsonrpc2.Request) {
			defer wg.Done()

			if request.Method == "concurrent-1" {
				// Signal start, then block until allowed to finish
				close(method1Started)
				close(method1Blocked)
				<-method1CanFinish
			} else if request.Method == "concurrent-2" {
				// Wait for first method to start and block
				<-method1Blocked
				close(method2Started)
			}
		},
	}

	ah := newLSPHandler(mockHandler, func(method string) bool {
		return strings.HasPrefix(method, "concurrent")
	})

	wg.Add(2)

	ah.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
		Method: "concurrent-1",
	})
	ah.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
		Method: "concurrent-2",
	})

	select {
	case <-method2Started:
		// Test is passed
	case <-time.After(100 * time.Millisecond): // Safety timeout
		t.Fatal("Test timed out waiting for concurrent execution")
	}

	close(method1CanFinish)

	wg.Wait()
}

func TestNewServerWithOptions(t *testing.T) {
	testMethodName := "test-method"

	options := NewServerOptions(func(method string) bool {
		return method == testMethodName
	})

	handleFunc := func(context *glsp.Context) (any, bool, bool, error) {
		return nil, true, true, nil
	}

	srv := NewServerWithOptions(handlerFunc(handleFunc), "test server", false, options)

	handler := srv.newHandler()

	lspHandler, ok := handler.(*lspHandler)
	if !ok {
		t.Fatal("Expected handler to be of type *lspHandler")
	}

	// Checking that handler's isConcurrentMethod function is correctly set
	if !lspHandler.isConcurrentMethod(testMethodName) {
		t.Error("Expected isConcurrentMethod to return true for test-method")
	}

	if lspHandler.isConcurrentMethod("different-method") {
		t.Error("Expected isConcurrentMethod to return false for different-method")
	}
}

type streamBuf struct {
	buf           io.ReadWriter
	read          int
	expectedReads int
	mu            sync.Mutex
}

func (b *streamBuf) Read(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err = b.buf.Read(p)
	if n > 0 && b.read < b.expectedReads {
		b.read++
	}
	if errors.Is(err, io.EOF) && b.read < b.expectedReads {
		err = nil
	}
	return
}

func (b *streamBuf) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *streamBuf) Close() error {
	return nil
}

type handlerFunc func(*glsp.Context) (any, bool, bool, error)

func (f handlerFunc) Handle(
	context *glsp.Context,
) (result any, validMethod bool, validParams bool, err error) {
	return f(context)
}

func TestLSPErrHandler(t *testing.T) {
	handleFunc := func(context *glsp.Context) (any, bool, bool, error) {
		time.Sleep(100 * time.Millisecond)
		return nil, true, true, &jsonrpc2.Error{
			Code: jsonrpc2.CodeInternalError,
		}
	}
	srv := NewServer(handlerFunc(handleFunc), "test", false)
	handler := srv.newHandler()

	var buf bytes.Buffer
	rwc := &streamBuf{buf: &buf, expectedReads: 2}
	stream := jsonrpc2.NewPlainObjectStream(rwc)
	conn := jsonrpc2.NewConn(context.Background(), stream, handler)

	var resp jsonrpc2.Response
	err := conn.Call(context.Background(), "test", nil, &resp)
	if err == nil {
		t.Fatalf("Expected error, got nil")
	}
	jerr, ok := err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("Expected jsonrpc2.Error, got %T", err)
	}

	if jerr.Code != jsonrpc2.CodeInternalError {
		t.Errorf("Expected error code %d, got %d", jsonrpc2.CodeInvalidRequest, jerr.Code)
	}
}

func TestLSPHandler_Cancel(t *testing.T) {
	done := make(chan struct{})
	firstCallCtx, cancel := context.WithCancel(context.Background())

	mockHandler := &MockHandler{
		handler: func(ctx context.Context, conn *jsonrpc2.Conn, request *jsonrpc2.Request) {
			switch request.Method {
			case "$/cancelRequest":
				cancel()
			case "call":
				<-ctx.Done()
				close(done)
			}
		},
	}

	ah := newLSPHandler(mockHandler, func(method string) bool {
		return false // No concurrent methods in this test
	})

	ah.Handle(firstCallCtx, &jsonrpc2.Conn{}, &jsonrpc2.Request{
		Method: "call",
	})

	ah.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
		Method: "$/cancelRequest",
	})

	select {
	case <-time.After(50 * time.Millisecond):
		t.Errorf("expected request to be cancelled")
	case <-done:
	}
}

type MockHandler struct {
	handler func(ctx context.Context, conn *jsonrpc2.Conn, request *jsonrpc2.Request)
}

func (m *MockHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, request *jsonrpc2.Request) {
	m.handler(ctx, conn, request)
}
