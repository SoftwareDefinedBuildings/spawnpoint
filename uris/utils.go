package uris

func SlotPath(base string, suffix string) string {
	return base + "/s.spawnpoint/server/i.spawnpoint/slot/" + suffix
}

func SignalPath(base string, suffix string) string {
	return base + "/s.spawnpoint/server/i.spawnpoint/signal/" + suffix
}
