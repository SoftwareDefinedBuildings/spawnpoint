package main

const SpawnPointConfigType = "67.0.2.0"

type DaemonConfig struct {
	Entity      string `yaml: "entity"`
	Alias       string `yaml: "alias"`
	Path        string `yaml: "path"`
	LocalRouter string `yaml: "localRouter"`
	MemAlloc    string `yaml: "memAlloc"`
	CpuShares   uint64 `yaml: "cpuShares"`
}
