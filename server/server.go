package server

import (
	"time"

	"github.com/tliron/commonlog"
	"github.com/tliron/glsp"
)

var DefaultTimeout = time.Minute

// Server configuration options
type ServerOptions struct {
	// Function that determines if a method should be handled concurrently
	IsConcurrentMethod func(method string) bool
}

// Additional server options with the specified predicate function
func NewServerOptions(isConcurrentMethod func(method string) bool) *ServerOptions {
	return &ServerOptions{
		IsConcurrentMethod: isConcurrentMethod,
	}
}

//
// Server
//

type Server struct {
	Handler     glsp.Handler
	LogBaseName string
	Debug       bool

	Log              commonlog.Logger
	Timeout          time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	StreamTimeout    time.Duration
	WebSocketTimeout time.Duration

	Options *ServerOptions
}

func NewServer(handler glsp.Handler, logName string, debug bool) *Server {
	return &Server{
		Handler:          handler,
		LogBaseName:      logName,
		Debug:            debug,
		Log:              commonlog.GetLogger(logName),
		Timeout:          DefaultTimeout,
		ReadTimeout:      DefaultTimeout,
		WriteTimeout:     DefaultTimeout,
		StreamTimeout:    DefaultTimeout,
		WebSocketTimeout: DefaultTimeout,
		Options: &ServerOptions{
			IsConcurrentMethod: func(method string) bool {
				return false
			},
		},
	}
}

// Creates a server with specified options
func NewServerWithOptions(handler glsp.Handler, logName string, debug bool, options *ServerOptions) *Server {
	server := NewServer(handler, logName, debug)
	server.Options = options
	return server
}
