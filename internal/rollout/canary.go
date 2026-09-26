package rollout

import (
	"sort"
	"strconv"
)

// CanaryChoice is the repository that receives the target version before the
// rest of the fleet, and why it was chosen.
type CanaryChoice struct {
	Repository string `json:"repository,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// SelectCanary chooses the repository that proves a target version.
//
// The canary is the movable repository whose pin is furthest behind the target:
// it meets the largest engine delta, so a version that breaks the oldest caller
// breaks one repository instead of the whole fleet. distance maps a repository
// to the number of commits between its pin and the target commit; a repository
// without a measurable distance is not eligible. When no distance is measurable
// the canary declared in fleet.yaml is kept, because the manifest stays the
// authority whenever measurement cannot answer. Ties break on repository name so
// a plan is reproducible.
func SelectCanary(movable []string, distance map[string]int, declared, target string) CanaryChoice {
	if len(movable) == 0 {
		return CanaryChoice{Reason: "no repository needs " + target}
	}
	names := append([]string(nil), movable...)
	sort.Strings(names)
	best, bestDistance := "", -1
	for _, name := range names {
		steps, measured := distance[name]
		if !measured || steps <= bestDistance {
			continue
		}
		best, bestDistance = name, steps
	}
	if best == "" {
		if declared == "" {
			return CanaryChoice{Reason: "no pin distance could be measured and fleet.yaml declares no canary"}
		}
		return CanaryChoice{Repository: declared, Reason: "kept the declared canary " + declared + ": no pin distance could be measured"}
	}
	return CanaryChoice{Repository: best, Reason: "oldest pin: " + strconv.Itoa(bestDistance) + " commit(s) behind " + target}
}
