//package handler contains HTTP handlers for the API
//Each handler is a function that reads an *http.Request and writes to an http.ResponseWriter
//Handlers should be thin: they parse input, call into business logic, and format response
//They should not contain business logic themselves

package handler //handler package holds functions that respond to invidual HTTP routes

import (
	"encoding/json"
	"net/http"
)

// handler is any function with signature func(w http.ResponseWriter, r *http.Request)
// Ii read the request from r, write the response into w, and the standard library handles all the TCP/http machinery around me
func Health(w http.ResponseWriter, r *http.Request) {
	//anonymous struct -> stuct type defined and instantiated in one move
	response := struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		//backtick-quoted string is struct tag -> metadata attached to the field, readable at runtime via reflection
		//in json version, status and version become lowercased
	}{
		Status:  "ok",
		Version: "0.0.1",
	}

	//w is an http.Responsewriter which is an interface that exposes three methods
	//1. Header() -> headers to send (mutate BEFORE writing body)
	//2. Write([]byte) (int, error) -> write body bytes, sends bytes out over TCP connection to the client
	//3. WriteHeader(statusCode int) -> send status line + headers

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)

}
