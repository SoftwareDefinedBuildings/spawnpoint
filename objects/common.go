package objects

import (
	"time"
)

type SpawnPointHb struct {
	Alias              string
	Time               int64
	TotalMem           uint64
	TotalCpuShares     uint64
	AvailableMem       uint64
	AvailableCpuShares uint64
}

type SvcConfig struct {
	ServiceName string            `yaml:"serviceName"`
	Entity      string            `yaml:"entity"`
	Container   string            `yaml:"container"`
	Build       []string          `yaml:"build"`
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

func IsSpawnPointGood(lastSeen time.Time) bool {
	return time.Now().Sub(lastSeen) < 10*time.Second
}

type SPLog struct {
	Time     int64
	SPAlias  string
	Service  string
	Contents string
}

type SpawnpointSvcHb struct {
	SpawnpointURI string
	Name          string
	Time          int64
	MemAlloc      uint64
	CpuShares     uint64
}
