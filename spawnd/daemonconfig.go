package main

type DaemonConfig struct {
	Entity      string `yaml:"entity"`
	Alias       string `yaml:"alias"`
	Path        string `yaml:"path"`
	LocalRouter string `yaml:"localRouter"`
	MemAlloc    string `yaml:"memAlloc"`
	CPUShares   uint64 `yaml:"cpuShares"`
}
