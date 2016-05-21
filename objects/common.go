package objects

import (
	"time"
)

type SpawnPointHb struct {
	Alias              string `yaml:"alias"`
	Time               string `yaml:"time"`
	AvailableMem       uint64 `yaml:"availableMem"`
	AvailableCpuShares uint64 `yaml:"availableCpuShares"`
}

type SvcConfig struct {
	ServiceName string            `yaml:"serviceName"`
	Entity      string            `yaml:"entity"`
	Container   string            `yaml:"container"`
	Build       string            `yaml:"build"`
	Source      string            `yaml:"source"`
	AptRequires string            `yaml:"aptRequires,omitempty"`
	Params      map[string]string `yaml:"params"`
	Run         []string          `yaml:"run,omitempty"`
	MemAlloc    string            `yaml:"memAlloc"`
	CpuShares   uint64            `yaml:"cpuShares"`
	AutoRestart bool              `yaml:"autoRestart"`
}

type SpawnPoint struct {
	URI                string
	LastSeen           time.Time
	Alias              string
	AvailableCpuShares uint64
	AvailableMem       uint64
}

func (sp *SpawnPoint) Good() bool {
	return time.Now().Sub(sp.LastSeen) < 10*time.Second
}

type SPLog struct {
	Time     int64
	SPAlias  string
	Service  string
	Contents string
}
