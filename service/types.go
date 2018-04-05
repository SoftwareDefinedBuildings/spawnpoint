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

func (config *Configuration) DeepCopy() *Configuration {
    newConfig := Configuration{
        Name: config.Name,
        BaseImage: config.BaseImage,
        Source: config.Source,
        BW2Entity: config.BW2Entity,
        CPUShares: config.CPUShares,
        Memory: config.Memory,
        AutoRestart: config.AutoRestart,
        UseHostNet: config.UseHostNet,
    }

    newConfig.Build = make([]string, len(config.Build))
    copy(newConfig.Build, config.Build)
    newConfig.Run = make([]string, len(config.Run))
    copy(newConfig.Run, config.Run)
    newConfig.IncludedFiles = make([]string, len(config.IncludedFiles))
    copy(newConfig.IncludedFiles, config.IncludedFiles)
    newConfig.IncludedDirectories = make([]string, len(config.IncludedDirectories))
    copy(newConfig.IncludedDirectories, config.IncludedDirectories)
    newConfig.Volumes = make([]string, len(config.Volumes))
    copy(newConfig.Volumes, config.Volumes)
    newConfig.Devices = make([]string, len(config.Devices))
    copy(newConfig.Devices, config.Devices)

    return &newConfig
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
