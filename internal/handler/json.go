package handler

import (
	"encoding/json"
	"net/http"
)

// envelope is a named type for "a JSON object with arbitrary values."
// map[string]any: keys are strings, values are ANY type. JSON encoding is exactly the case that needs the EMTPY interface

// writeJSON marshals (convert to JSON-formatted string)
// remember, libraries return, callers decide
func writeJSON(w http.ResponseWriter, status int, data envelope) error {
	// MarshalIndent instead of Marshal: slower/bigger but output is readable in terminal with plain curl
	js, err := json.MarshalIndent(data, "", "\t")
	if err != nil {
		return err
	}

	js = append(js, '\n')

	// ORDER MATTERS
	// 1. mutate headers "building and modifying the HTTP headers (using methods like Set() or Add())"
	// 2. Write Header (status) "committing those headers and the HTTP status code to the network"
	// 3. write the body
	// Once I call WriteHeader, Set calls are silently ignored because headers are already on the wire

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(js) // sends actual response body
	return nil

}