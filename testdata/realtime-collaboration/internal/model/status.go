package model

// Status is shared by the backend JSON response and the frontend renderer.
// The collaboration exercise deliberately asks both agents to extend the
// adjacent field block at the same time.
type Status struct {
	Service string `json:"service"`
	Version string `json:"version"`
}

func NewStatus(service, version string) Status {
	return Status{Service: service, Version: version}
}
