package uris

import "fmt"

func SlotPath(base string, suffix string) string {
	return base + "/s.spawnpoint/server/i.spawnpoint/slot/" + suffix
}

func SignalPath(base string, suffix string) string {
	return base + "/s.spawnpoint/server/i.spawnpoint/signal/" + suffix
}

func ServiceSignalPath(base string, name string, suffix string) string {
    return fmt.Sprintf("%s/s.spawnpoint/%s/i.spawnable/signal/%s", base, name, suffix)
}
