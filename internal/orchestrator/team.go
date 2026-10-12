package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// TeamFileName is the canonical plan file name inside a team directory
// (<teams>/<name>/team.yaml).
const TeamFileName = "team.yaml"

// TeamPlan declares a team as a DAG of nodes (§4.1.2): id/role/toolchain
// per node, depends_on edges, a deliverable node (output.from) and a
// scheduling policy (max_fanout/retry).
type TeamPlan struct {
	Name   string     `yaml:"name"`
	Nodes  []NodeSpec `yaml:"nodes"`
	Output OutputSpec `yaml:"output"`
	Policy PolicySpec `yaml:"policy"`
}

// NodeSpec is one node of the team DAG.
type NodeSpec struct {
	ID        string   `yaml:"id"`
	Role      string   `yaml:"role"`
	Toolchain string   `yaml:"toolchain"`
	DependsOn []string `yaml:"depends_on"`
}

// OutputSpec selects the node whose output is the team deliverable.
type OutputSpec struct {
	From string `yaml:"from"`
}

// PolicySpec tunes scheduling: MaxFanout caps the parallel wave size
// (<=0 means unlimited); Retry is the number of retries after a failure.
type PolicySpec struct {
	MaxFanout int `yaml:"max_fanout"`
	Retry     int `yaml:"retry"`
}

// ParseTeam decodes a team plan from YAML bytes.
func ParseTeam(data []byte) (*TeamPlan, error) {
	var p TeamPlan
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("orchestrator: parse team plan: %w", err)
	}
	return &p, nil
}

// LoadTeam loads team.yaml from dir.
func LoadTeam(dir string) (*TeamPlan, error) {
	data, err := os.ReadFile(filepath.Join(dir, TeamFileName))
	if err != nil {
		return nil, fmt.Errorf("orchestrator: load team plan from %s: %w", dir, err)
	}
	return ParseTeam(data)
}

// Marshal serializes the plan back to YAML.
func (p *TeamPlan) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: marshal team plan: %w", err)
	}
	return data, nil
}
