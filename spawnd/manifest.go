package main

import "time"

type Manifest struct {
	ServiceName   string
	Entity        []byte
	ContainerType string
	MemAlloc      uint64
	CPUShares     uint64
	Build         []string
	Run           []string
	AutoRestart   bool
	RestartInt    time.Duration
	Container     *SpawnPointContainer
	Volumes       []string
	logger        *BWLogger
}
