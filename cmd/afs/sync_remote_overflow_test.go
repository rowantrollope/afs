package main

import (
	"testing"
)

func TestSyncRemoteQueueOverflowRetainsRecovery(t *testing.T) {
	for _, rootReplace := range []bool{false, true} {
		t.Run(map[bool]string{false: "file_change", true: "root_replace"}[rootReplace], func(t *testing.T) {
			_, d := recoveryBaselineDiagnostic(t)
			for i := 0; i < cap(d.pump.out); i++ {
				d.pump.out <- remoteEvent{Path: "/already-queued"}
			}
			// A microVM processing a burst may have a full event queue. The
			// final peer publication must leave a recovery request even if
			// there is no subsequent filesystem event or reconnect.
			d.pump.send(remoteEvent{Path: "/last-publication", RootReplace: rootReplace})
			requests := d.reconciler.fullSweepRequests()
			if rootReplace {
				requests = d.reconciler.rootReplaceRequests()
			}
			select {
			case <-requests:
			default:
				t.Fatal("saturated remote queue silently dropped the last publication and recovery request")
			}
		})
	}
}
