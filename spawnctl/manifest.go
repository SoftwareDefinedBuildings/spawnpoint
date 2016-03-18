package main

type ManifestEntry struct {
	Entity      string
	Container   string
	AptRequires string
	Source      string
	Build       string
	Run         string
	Params      map[string]string
}
