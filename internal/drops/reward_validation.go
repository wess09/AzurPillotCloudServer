package drops

import (
	_ "embed"
	"encoding/json"
)

// Audited 2026-11-05 normal-source rewards, excluding the confirmed forged batch.
// This pinned baseline cannot be enlarged automatically by incoming submissions.
//
//go:embed reward_baseline.json
var rewardBaselineJSON []byte

type rewardBaseline struct {
	Samples int
	P99     int64
	Max     int64
}

var rewardBaselines = func() map[string]map[string]rewardBaseline {
	var values map[string]map[string]rewardBaseline
	if err := json.Unmarshal(rewardBaselineJSON, &values); err != nil {
		panic(err)
	}
	return values
}()

type rewardRule struct{ id, category string }

func rewardRules(items []analysisItem) map[string]rewardRule {
	rules := make(map[string]rewardRule)
	for _, item := range items {
		rule := rewardRule{item.ID, item.Category}
		rules[item.ID] = rule
		for _, alias := range item.Aliases {
			rules[alias] = rule
		}
	}
	for alias, id := range map[string]string{"Cube": "Cubes", "Gem": "Gems"} {
		if rule, ok := rules[id]; ok {
			rules[alias] = rule
		}
	}
	return rules
}

var pinnedRewardRules = rewardRules(catalog.Items)

func rewardLimit(scene, item, category string) int64 {
	// Floors leave room for rare rewards; ceilings prevent an outlier baseline
	// from normalizing arbitrary quantities. These are sanity bounds, not drop tables.
	floor, ceiling := int64(1000), int64(10000)
	if scene == "commission" {
		floor, ceiling = 10000, 100000
	}
	switch category {
	case "coins":
		floor, ceiling = 1000, 100000
		if scene == "commission" {
			floor = 10000
		}
	case "oil":
		floor, ceiling = 2000, 10000
	case "cube", "blueprint-rainbow", "blueprint-gold", "gear-rainbow", "gear-gold", "gear-other", "book", "books":
		floor, ceiling = 20, 100
	case "gems":
		floor, ceiling = 100, 1000
	case "chips":
		floor, ceiling = 500, 5000
	case "decor":
		floor, ceiling = 1000, 10000
	case "drills":
		floor, ceiling = 20, 100
	case "plate", "plates":
		floor, ceiling = 100, 1000
	case "retrofit":
		floor, ceiling = 20, 100
	case "box", "boxes":
		floor, ceiling = 50, 500
	}
	baseline, ok := rewardBaselines[scene][item]
	if !ok || baseline.Samples < 20 {
		return ceiling
	}
	return min(ceiling, max(floor, baseline.P99*10, baseline.Max*2))
}
