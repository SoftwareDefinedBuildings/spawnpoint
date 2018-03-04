package service

type Configuration struct {
	Name                string   `yaml:"name"`
	BaseImage           string   `yaml:"image"`
	Source              string   `yaml:"source"`
	BW2Entity           string   `yaml:"bw2Entity"`
	CPUShares           uint64   `yaml:"cpuShares"`
	Memory              uint64   `yaml:"memory"`
	Build               []string `yaml:"build,omitempty"`
	Run                 []string `yaml:"run"`
	IncludedFiles       []string `yaml:"includedFiles,omitempty"`
	IncludedDirectories []string `yaml:"includedDirectories,omitempty"`
	AutoRestart         bool     `yaml:"autoRestart,omitempty"`
	UseHostNet          bool     `yaml:"useHostNet,omitempty"`
	Volumes             []string `yaml:"volumes,omitempty"`
	Devices             []string `yaml:"devices,omitempty"`
}

type LogMessage struct {
	Contents  string
	Timestamp int64
}

type Event int

const (
	Boot    = iota
	Restart = iota
	Stop    = iota
	Die     = iota
	Adopt   = iota
)
