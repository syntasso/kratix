package main

import "time"

func GetPodTTLSecondsAfterFinishedForTest(kratixConfig *KratixConfig) *time.Duration {
	return getPodTTLSecondsAfterFinished(kratixConfig)
}
