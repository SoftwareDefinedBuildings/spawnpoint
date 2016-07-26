package objects

import (
	"time"
)

type SpawnPointHb struct {
	Alias              string
	Time               int64
	TotalMem           uint64
	TotalCPUShares     uint64
	AvailableMem       int64
	AvailableCPUShares int64
}

type SvcConfig struct {
	ServiceName   string   `yaml:"serviceName"`
	Entity        string   `yaml:"entity"`
	Container     string   `yaml:"container"`
	Build         []string `yaml:"build,omitempty"`
	Source        string   `yaml:"source,omitempty"`
	AptRequires   string   `yaml:"aptRequires,omitempty"`
	Run           []string `yaml:"run,omitempty"`
	MemAlloc      string   `yaml:"memAlloc"`
	CPUShares     uint64   `yaml:"cpuShares"`
	Volumes       []string `yaml:"volumes,omitempty"`
	IncludedFiles []string `yaml:"includedFiles,omitempty"`
	IncludedDirs  []string `yaml:"includedDirs,omitempty"`
	AutoRestart   bool     `yaml:"autoRestart"`
	RestartInt    string   `yaml:"restartInt,omitempty"`
}

type SpawnPoint struct {
	URI                string
	LastSeen           time.Time
	Alias              string
	AvailableCPUShares int64
	AvailableMem       int64
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
	CPUShares     uint64
	MemUsage      float64
	NetworkRx     float64
	NetworkTx     float64
	MbRead        float64
	MbWritten     float64
	CPUPercent    float64
}
