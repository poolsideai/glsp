package server

import (
	contextpkg "context"
	"errors"
	"fmt"
	"sync"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/tliron/glsp"
)

// See: https://github.com/sourcegraph/go-langserver/blob/master/langserver/handler.go#L206
func (self *Server) newHandler() jsonrpc2.Handler {
	return newLSPHandler(jsonrpc2.HandlerWithError(self.handle), self.Options.IsConcurrentMethod)
}

// newLSPHandler returns a handler that processes each request goes in its own
// goroutine, processing requests in a FIFO fashion besides $/cancelRequest, which are not queued.
// It allows unbounded goroutines, all stalled on the previous one.
func newLSPHandler(handler jsonrpc2.Handler, isConcurrentMethod func(string) bool) jsonrpc2.Handler {
	head := make(chan struct{})
	close(head)
	return &lspHandler{
		wrapped:            handler,
		head:               head,
		isConcurrentMethod: isConcurrentMethod,
	}
}

type lspHandler struct {
	wrapped            jsonrpc2.Handler
	head               chan struct{}
	mx                 sync.Mutex
	isConcurrentMethod func(string) bool
}

func (a *lspHandler) Handle(ctx contextpkg.Context, conn *jsonrpc2.Conn, request *jsonrpc2.Request) {
	// Don't consider cancel and concurrent requests as part of the request queue
	if request.Method == "$/cancelRequest" || a.isConcurrentMethod(request.Method) {
		go a.wrapped.Handle(ctx, conn, request)
		return
	}

	a.mx.Lock()
	previous := a.head
	thisReq := make(chan struct{})
	a.head = thisReq
	a.mx.Unlock()

	go func() {
		defer close(thisReq)
		<-previous
		a.wrapped.Handle(ctx, conn, request)
	}()
}

func (self *Server) handle(context contextpkg.Context, connection *jsonrpc2.Conn, request *jsonrpc2.Request) (any, error) {
	glspContext := glsp.Context{
		Method:    request.Method,
		RequestID: request.ID,
		Notify: func(method string, params any) error {
			return connection.Notify(context, method, params)
		},
		Call: func(method string, params any, result any) error {
			return connection.Call(context, method, params, result)
		},
		Context: context,
	}

	if request.Params != nil {
		glspContext.Params = *request.Params
	}

	switch request.Method {
	case "exit":
		// We're giving the attached handler a chance to handle it first, but we'll ignore any result
		self.Handler.Handle(&glspContext)
		err := connection.Close()
		return nil, err

	default:
		var jsonRPCErr *jsonrpc2.Error
		// Note: jsonrpc2 will not even call this function if reqest.Params is invalid JSON,
		// so we don't need to handle jsonrpc2.CodeParseError here
		result, validMethod, validParams, err := self.Handler.Handle(&glspContext)
		if !validMethod {
			return nil, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeMethodNotFound,
				Message: fmt.Sprintf("method not supported: %s", request.Method),
			}
		} else if !validParams {
			if err == nil {
				return nil, &jsonrpc2.Error{
					Code: jsonrpc2.CodeInvalidParams,
				}
			} else {
				return nil, &jsonrpc2.Error{
					Code:    jsonrpc2.CodeInvalidParams,
					Message: err.Error(),
				}
			}
		} else if errors.As(err, &jsonRPCErr) {
			return nil, jsonRPCErr
		} else if err != nil {
			return nil, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeInvalidRequest,
				Message: err.Error(),
			}
		} else {
			return result, nil
		}
	}
}
