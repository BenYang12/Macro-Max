package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// every endpoint from here on returns JSON in one of two shapes:
// 1. {"foods": [...]}
// 2. {"error": {"code": "not_found", "message": "..."}}

type envelope map[string]any

// envelope is a named type for "a JSON object with arbitrary values."
// map[string]any: keys are strings, values are ANY type. JSON encoding is exactly the case that needs the EMTPY interface
// writeJSON marshals (convert to JSON-formatted string)
// remember, libraries return, callers decide
// writeJSON is helper that every handler will call as its last step - it converts any Go value into JSON and writes the complete HTTP response
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

// readJSON: client -> JSON bytes -> Go value (unmarshaling)
// decodes a request body into dst (any), dst is any b/c it receives a POINTER to any struct
func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	// 1. Cap body size
	const maxBytes = 1_048_576

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes) // http.MaxBytesReader(http.ResponseWriter, reader, max) takes a reader and returns a new reader that behaves identically but is capped by size
	dec := json.NewDecoder(r.Body)                    // JSON-parsing machine, aim at request body

	// 2. Reject unknown fields. Default JSON behavior is to silently IGNORE fields it doesn't recognize
	dec.DisallowUnknownFields()

	err := dec.Decode(dst) // Decoding actually begins here, translator listens, translates, and writes the result into my struct. WHERE TO: write the result into dst

	if err != nil {
		// Errors are VALUES, so I examine them rather than catching typed exceptions:
		//   errors.Is(err, target)  — "is this error (or anything it wraps)
		//                              this specific SENTINEL value?"
		//   errors.As(err, &target) — "is this error (or anything it wraps)
		//                              of this TYPE? if so, assign it to
		//                              target so I can read its fields"
		var syntaxError *json.SyntaxError
		var unmarshalTypeError *json.UnmarshalTypeError
		var invalidUnmarshalError *json.InvalidUnmarshalError
		var maxBytesError *http.MaxBytesError

		switch {
		// Malformed JSON
		case errors.As(err, &syntaxError):
			return fmt.Errorf("body contains badly-formed JSON (at character %d)", syntaxError.Offset)

		// Truncated JSON
		case errors.Is(err, io.ErrUnexpectedEOF):
			return errors.New("body contains badly-formed JSON")

		// Valid JSON, wrong type: {"protein_g_daily": "lots"} into an int.
		case errors.As(err, &unmarshalTypeError):
			if unmarshalTypeError.Field != "" {
				return fmt.Errorf("body contains incorrect JSON type for field %q", unmarshalTypeError.Field)
			}
			return fmt.Errorf("body contains incorrect JSON type (at character %d)", unmarshalTypeError.Offset)

		// Completely empty body.
		case errors.Is(err, io.EOF):
			return errors.New("body must not be empty")

		// The unknown-field rejection from DisallowUnknownFields. The stdlib
		// gives no typed error for this, so string-matching is the only
		// option — a known wart, not our design choice.
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			fieldName := strings.TrimPrefix(err.Error(), "json: unknown field ")
			return fmt.Errorf("body contains unknown key %s", fieldName)

		// Body exceeded maxBytes.
		case errors.As(err, &maxBytesError):
			return fmt.Errorf("body must not be larger than %d bytes", maxBytesError.Limit)

		// This one means WE passed something non-pointer to Decode — a
		// programmer bug, not bad input. panic is correct: it's unrecoverable
		// and should never survive to production. (The only panic in this
		// project.)
		case errors.As(err, &invalidUnmarshalError):
			panic(err)

		default:
			return err
		}
	}

	// Decode reads ONE JSON value and stops. So a body of
	//   {"label":"cutting"}{"label":"evil"}
	// would decode the first and silently ignore the rest. Calling Decode
	// again must return EOF — anything else means there was trailing data.
	err = dec.Decode(&struct{}{})
	if !errors.Is(err, io.EOF) {
		return errors.New("body must only contain a single JSON value")
	}

	return nil
}

// Error responses

// One helper per HTTP status this API actually returns, so handlers read as
// `notFoundResponse(w)` rather than assembling envelopes inline. Consistency
// for free: every error in the app has the same shape.

// errorResponse is the shared builder the specific helpers below delegate to.
func errorResponse(w http.ResponseWriter, status int, code, message string) {
	env := envelope{"error": map[string]any{
		"code":    code,
		"message": message,
	}}

	// If writing the error response ITSELF fails, there's nothing graceful
	// left to do — log it and fall back to a bare 500.
	if err := writeJSON(w, status, env); err != nil {
		log.Printf("failed to write error response: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// 400: the request was malformed — bad JSON, wrong types, unknown fields.
// The client must change the REQUEST ITSELF to succeed.
func badRequestResponse(w http.ResponseWriter, err error) {
	errorResponse(w, http.StatusBadRequest, "bad_request", err.Error())
}

// 404: valid request, no such resource.
func notFoundResponse(w http.ResponseWriter) {
	errorResponse(w, http.StatusNotFound, "not_found", "the requested resource could not be found")
}

// 422: the JSON parsed fine, but the VALUES are unacceptable (negative
// protein, zero budget). Distinct from 400 on purpose: 400 means "I can't
// read this", 422 means "I read it, and it's wrong". Field-level detail
// lets a frontend highlight the offending input.
func failedValidationResponse(w http.ResponseWriter, fields map[string]string) {
	env := envelope{"error": map[string]any{
		"code":    "validation_failed",
		"message": "the request contained invalid values",
		"fields":  fields,
	}}

	if err := writeJSON(w, http.StatusUnprocessableEntity, env); err != nil {
		log.Printf("failed to write error response: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// 500: our fault. Log the real error for us; tell the client nothing
// specific — internal details are an information leak (stack traces and SQL
// errors reveal schema and library versions to an attacker).
func serverErrorResponse(w http.ResponseWriter, err error) {
	log.Printf("server error: %v", err)
	errorResponse(w, http.StatusInternalServerError, "server_error",
		"the server encountered a problem and could not process your request")
}
