package main

const SpawnPointHeartbeatType = "67.0.2.1"

type SpawnPointHb struct {
	Alias              string `yaml: "alias"`
	Time               string `yaml: "time"`
	AvailableMem       uint64 `yaml: "availableMem"`
	AvailableCpuShares uint64 `yaml: "availableCpuShares"`
}
