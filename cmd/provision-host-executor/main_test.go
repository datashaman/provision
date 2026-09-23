package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestExecutorRejectsDeploymentAndArbitraryCommands(t *testing.T) {
	for _, args := range [][]string{{"apply"}, {"sh", "-c", "touch /tmp/should-not-exist"}} {
		cmd := exec.Command("go", append([]string{"run", "."}, args...)...)
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "no deployment operations are enabled") {
			t.Fatalf("command %v accepted: %v\n%s", args, err, output)
		}
	}
}
