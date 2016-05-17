package main

type SPLog struct {
	Time     int64
	SPAlias  string
	Service  string
	Contents string
}

type SLM struct {
	Service string
	Message string
}
