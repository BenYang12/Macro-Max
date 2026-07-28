// Package fdc talks to the USDA FoodData Central API
// CLIENT package: knows how to fetch and decode FDC's JSON, and nothing else.
// Does not know about Postgres, HTTP handlers, or this project's Food type
// API docs: https://fdc.nal.usda.gov/api-guide.html

package fdc

import (
	"net/http"
	"time"
)

// defaultBaseURL is real API
// Tests override to point at local httptest server -> testable is important
const defaultBaseURL = "https://api.nal.usda.gov/fdc/v1"

// Configured FDC API Client
// holds own *http.Client instead of calling http.Get (which uses http.DefaultClient -> NO TIMEOUT)
// Owning client = owning timeout
// ONE client shared across many requests is both correct and faster than creating one per call
type Client struct{
	BaseURL string
	APIKey string
	HTTP *http.Client
}

func New(apiKey string) *Client{
	return &Client{
		BaseURL: defaultBaseURL,
		APIKey:  apiKey,
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}

}