package service

type Configuration struct {
	Name                string   `yaml:"name"`
	BaseImage           string   `yaml:"image"`
	Source              string   `yaml:"source"`
	BW2Entity           string   `yaml:"bw2Entity"`
	CPUShares           uint32   `yaml:"cpuShares"`
	Memory              uint32   `yaml:"memory"`
	Build               []string `yaml:"build,omitempty"`
	Run                 []string `yaml:"run"`
	IncludedFiles       []string `yaml:"includedFiles,omitempty"`
	IncludedDirectories []string `yaml:"includedDirectories,omitempty"`
	AutoRestart         bool     `yaml:"autoRestart,omitempty"`
	UseHostNet          bool     `yaml:"useHostNet,omitempty"`
}

type LogMessage struct {
	Contents  string
	Timestamp int64
}
