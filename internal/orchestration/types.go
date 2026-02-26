// Package orchestration defines interfaces and types for coordinating agent
// work across gateway slots. These types are separated from the llm and gateway
// packages to avoid import cycles.
package orchestration

import "github.com/jefflinse/toasters/internal/agents"

// GatewaySlot holds a summary of a single gateway slot for operator visibility.
type GatewaySlot struct {
	Index   int
	Team    string
	JobID   string
	Status  string // "running", "done", "idle"
	Elapsed string
}

// GatewaySpawner is the interface satisfied by *gateway.Gateway.
type GatewaySpawner interface {
	SpawnTeam(teamName, jobID, task string, team agents.Team, jobDir string) (slotID int, alreadyRunning bool, err error)
	SlotSummaries() []GatewaySlot
	Kill(slotID int) error
}
