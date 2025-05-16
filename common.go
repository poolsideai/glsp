package glsp

import (
	contextpkg "context"
	"encoding/json"

	"github.com/sourcegraph/jsonrpc2"
)

type NotifyFunc func(ctx contextpkg.Context, method string, params any) error
type CallFunc func(ctx contextpkg.Context, method string, params any, result any) error

type Context struct {
	Method string
	Params json.RawMessage
	Notify NotifyFunc
	Call   CallFunc
	// The underlying jsonrpc request ID, used for cancellation
	RequestID jsonrpc2.ID
	Context   contextpkg.Context // can be nil
}

type Handler interface {
	Handle(context *Context) (result any, validMethod bool, validParams bool, err error)
}
