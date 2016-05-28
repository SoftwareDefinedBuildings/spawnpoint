package main

type Manifest struct {
	ServiceName   string
	Entity        []byte
	ContainerType string
	MemAlloc      uint64
	CpuShares     uint64
	Build         []string
	Run           []string
	AutoRestart   bool
	Container     *SpawnPointContainer
	logger        *BWLogger
}
