// Package fdc talks to the USDA FoodData Central API
// CLIENT package: knows how to fetch and decode FDC's JSON, and nothing else.
// Does not know about Postgres, HTTP handlers, or this project's Food type
// API docs: https://fdc.nal.usda.gov/api-guide.html

package fdc

import (
	"net/http"
)

// defaultBaseURL is real API
// Tests override to point at local httptest server -> testable is important
const defaultBaseUrl = "https://api.nal.usda.gov/fdc/v1"

// Configured FDC API Client
type Client struct{
	BaseURL string
	APIKey string
	HTTP *http.Client
}