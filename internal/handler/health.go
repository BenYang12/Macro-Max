package handler

import (
	"net/http"
)

const version = "0.0.1"


//Health responds with JSON payload confirming the service is up.
//This is the shape every production API needs - load balancers, uptime
//monitors, and deploy scripts all hit an endpoint like this


//http.ResponseWriter is interface that represents the outgoing response back to the client
func Health(w http.ResponseWriter, r *http.Request){
	payload := map[string] string{
		"status": "available",
		"version": version,
	}
}