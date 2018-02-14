package util

import (
	"os"

	logging "github.com/op/go-logging"
)

func InitLogger(name string) *logging.Logger {
	format := "%{color}%{level} %{time:Jan 1 15:04:05} %{shortfunc} %{color:reset}▶ %{message}"
	backend := logging.NewLogBackend(os.Stderr, "", -1)
	logBackendLeveled := logging.AddModuleLevel(backend)
	logging.SetBackend(logBackendLeveled)
	logging.SetFormatter(logging.MustStringFormatter(format))
	return logging.MustGetLogger(name)
}
