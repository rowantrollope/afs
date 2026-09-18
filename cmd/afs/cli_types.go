package main

import "github.com/rowantrollope/afs/internal/managedclient"

// Configuration intentionally has one Redis connection and optional retained sync tuning.
type syncSettings struct {
	SyncFileSizeCapMB        int `json:"fileSizeCapMB,omitempty"`
	SyncWatcherQueueCapacity int `json:"watcherQueueCapacity,omitempty"`
}
type config struct {
	Redis        string                  `json:"redis"`
	ControlPlane *managedclient.Settings `json:"controlPlane,omitempty"`
	// Runtime-only: server credentials must not be replaced by local Redis env.
	redisFromControlPlane bool
	syncSettings          `json:"sync,omitempty"`
}
