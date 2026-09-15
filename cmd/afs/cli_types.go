package main

// Configuration intentionally has one Redis connection and optional retained sync tuning.
type syncSettings struct {
	SyncFileSizeCapMB        int `json:"fileSizeCapMB,omitempty"`
	SyncWatcherQueueCapacity int `json:"watcherQueueCapacity,omitempty"`
}
type config struct {
	Redis        string `json:"redis"`
	syncSettings `json:"sync,omitempty"`
}
