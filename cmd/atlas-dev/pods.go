package main

import "github.com/snkio027/atlas/internal/platformcheck"

type runtimePod = platformcheck.Pod

func verifyPods(pods []runtimePod, cluster string) error {
	return platformcheck.VerifyPods(pods, cluster, "atlas-gateway")
}
