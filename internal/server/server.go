package server

//server's job is to wire up routes and hand back an http.Hander that main.go can start
//keeping this separate from main.go means I can test the whole server in-process later without spinning up a real network listener

import (
	"net/http"

	"github.com/BenYang12/macro-max/internal/handler"
)

//New function builds the HTTP handler for the API. It wires routes to handlers.
//New returns something that satisfies http.Handler, which is anything with a ServeHTTP(w,r) method
func New() http.Handler{
	mux := http.NewServeMux() //request multiplexer (router) -> looks at each request's URL path and dispatches it to the handler registered for that path
	mux.HandleFunc("Get /v1/healtcheck", handler.Health) //register one route to the mux, and return it as an http.Handler
	return mux
}