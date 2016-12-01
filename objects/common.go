package objects

import (
	"errors"
	"strconv"
	"time"
)

const SharesPerCore = 1024

const ZombiePeriod = 2 * time.Minute
const MetadataCutoff = 1 * time.Minute

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
	Image         string   `yaml:"image"`
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
	OverlayNet    string   `yaml:"overlayNet,omitempty"`
}

type SpawnPoint struct {
	URI                string
	LastSeen           time.Time
	Alias              string
	AvailableCPUShares int64
	AvailableMem       int64
}

type Service struct {
	Name          string
	HostURI       string
	LastSeen      time.Time
	MemAlloc      uint64
	CPUShares     uint64
	MemUsage      float64
	CPUShareUsage uint64
}

func (sp *SpawnPoint) Good() bool {
	return time.Now().Sub(sp.LastSeen) < 10*time.Second
}

func IsSpawnPointGood(lastSeen time.Time) bool {
	return time.Now().Sub(lastSeen) < 10*time.Second
}

func ParseMemAlloc(alloc string) (uint64, error) {
	if alloc == "" {
		return 0, errors.New("No memory allocation in config")
	}
	suffix := alloc[len(alloc)-1:]
	memAlloc, err := strconv.ParseUint(alloc[:len(alloc)-1], 0, 64)
	if err != nil {
		return 0, err
	}

	if suffix == "G" || suffix == "g" {
		memAlloc *= 1024
	} else if suffix != "M" && suffix != "m" {
		err = errors.New("Memory allocation amount must be in units of M or G")
		return 0, err
	}

	return memAlloc, nil
}

type SPLogMsg struct {
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
