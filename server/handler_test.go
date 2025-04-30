package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	mu := sync.Mutex{}
	received := []string{}
	processed := make(chan string, 20)

	// This handler will sleep for different durations based on the method
	// This allows us to validate that concurrent methods don't block each other
	mockHandler := &MockHandler{
		handler: func(ctx context.Context, conn *jsonrpc2.Conn, request *jsonrpc2.Request) {
			if request.Method == "concurrent-0" {
				// Make the first concurrent method take longer
				time.Sleep(50 * time.Millisecond)
			} else {
				time.Sleep(5 * time.Millisecond)
			}

			mu.Lock()
			received = append(received, request.Method)
			mu.Unlock()

			processed <- request.Method
			wg.Done()
		},
	}

	// Create a handler that marks methods starting with "concurrent" as concurrent
	ah := newLSPHandler(mockHandler, func(method string) bool {
		isConcurrent := method[:10] == "concurrent"
		return isConcurrent
	})

	// First, send a concurrent request (which will take longer)
	wg.Add(1)
	ah.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
		Method: "concurrent-0",
	})

	// Then send a mix of concurrent and sequential requests
	for i := range 5 {
		wg.Add(1)
		if i%2 == 0 {
			// Even numbers are concurrent
			ah.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
				Method: fmt.Sprintf("concurrent-%d", i+1),
			})
		} else {
			// Odd numbers are sequential
			ah.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
				Method: fmt.Sprintf("sequential-%d", i),
			})
		}
	}

	wg.Wait()
	close(processed)

	// Collect all processed methods
	var processedMethods []string
	for method := range processed {
		processedMethods = append(processedMethods, method)
	}

	// Find the position of the slow concurrent method
	slowMethodIndex := -1
	for i, method := range processedMethods {
		if method == "concurrent-0" {
			slowMethodIndex = i
			break
		}
	}

	// Find positions of other concurrent methods
	otherConcurrentPositions := []int{}
	for i, method := range processedMethods {
		if method[:10] == "concurrent" && method != "concurrent-0" {
			otherConcurrentPositions = append(otherConcurrentPositions, i)
		}
	}

	// Check if other concurrent method is executed before the slow method
	concurrentFinishedBeforeSlow := false
	for _, pos := range otherConcurrentPositions {
		if pos < slowMethodIndex {
			concurrentFinishedBeforeSlow = true
			break
		}
	}

	// If the slow method is the last one, that's also a valid concurrent behavior
	if !concurrentFinishedBeforeSlow && slowMethodIndex != len(processedMethods)-1 {
		t.Errorf("Expected methods to run in parallel but got: %v", processedMethods)
	}

	// Verify that sequential methods were processed in order
	sequentialMethods := []string{}
	for _, method := range received {
		if method[:10] == "sequential" {
			sequentialMethods = append(sequentialMethods, method)
		}
	}

	for i, method := range sequentialMethods {
		expected := fmt.Sprintf("sequential-%d", 2*i+1)
		if method != expected {
			t.Errorf("Expected sequential method %s, but got %s", expected, method)
		}
	}
}

func TestNewServerWithOptions(t *testing.T) {
	options := NewServerOptions(func(method string) bool {
		return method == "concurrent"
	})

	handleFunc := func(context *glsp.Context) (any, bool, bool, error) {
		return nil, true, true, nil
	}

	srv := NewServerWithOptions(handlerFunc(handleFunc), "test server", false, options)

	wg := sync.WaitGroup{}
	mu := sync.Mutex{}
	receivedOrder := []string{}

	mockHandler := &MockHandler{
		handler: func(ctx context.Context, conn *jsonrpc2.Conn, request *jsonrpc2.Request) {
			if request.Method == "concurrent" {
				// Concurrent method should finish faster despite being called last
				time.Sleep(10 * time.Millisecond)
			} else {
				// Non-concurrent method should be processed in order
				time.Sleep(30 * time.Millisecond)
			}

			mu.Lock()
			receivedOrder = append(receivedOrder, request.Method)
			mu.Unlock()

			wg.Done()
		},
	}

	handler := newLSPHandler(mockHandler, srv.Options.IsConcurrentMethod)

	// Send requests
	wg.Add(3)

	handler.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
		Method: "sequential-1",
	})
	handler.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
		Method: "sequential-2",
	})
	handler.Handle(context.Background(), &jsonrpc2.Conn{}, &jsonrpc2.Request{
		Method: "concurrent",
	})

	wg.Wait()

	if receivedOrder[0] != "concurrent" {
		t.Errorf("Expected concurrent method to finish the first, but got: %v", receivedOrder)
	}

	// Sequential methods should be in order
	sequentialMethods := []string{}
	for _, method := range receivedOrder {
		if method != "concurrent" {
			sequentialMethods = append(sequentialMethods, method)
		}
	}

	if sequentialMethods[0] == "sequential-1" && sequentialMethods[1] == "sequential-2" {
		// This is the expected order
	} else {
		t.Errorf("Expected sequential methods to be processed in order, but got: %v", sequentialMethods)
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
