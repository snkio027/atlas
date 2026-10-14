package main

import "atlas-refactor/internal/platformcheck"

type runtimePod = platformcheck.Pod

func verifyPods(pods []runtimePod, cluster string) error {
	return platformcheck.VerifyPods(pods, cluster, "atlas-gateway")
}
