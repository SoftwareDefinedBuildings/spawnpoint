package main

import "time"

type Manifest struct {
	ServiceName string
	Entity      []byte
	Image       string
	MemAlloc    uint64
	CPUShares   uint64
	Build       []string
	Run         []string
	AutoRestart bool
	RestartInt  time.Duration
	Container   *SpawnPointContainer
	Volumes     []string
	logger      *BWLogger
	OverlayNet  string
	UseHostNet  bool
	eventChan   *chan svcEvent
	restarting  bool
	stopping    bool
}
