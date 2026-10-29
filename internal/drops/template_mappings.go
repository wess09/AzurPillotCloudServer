package drops

import "ap-cloud-server/internal/v3db"

type resolvedItemKey struct{ recordID, item, tag string }

// Resolve copies before aggregation: raw rows stay untouched, while aliases that
// become the same item in one event contribute one occurrence and their full amount.
func resolveCommissionItems(items []v3db.CommissionItem, mappings map[string]v3db.TemplateItemMapping) []v3db.CommissionItem {
	result := make([]v3db.CommissionItem, 0, len(items))
	positions := make(map[resolvedItemKey]int)
	for _, item := range items {
		if mapping, ok := mappings[item.Item]; ok {
			item.Item = mapping.ItemID
			if mapping.Tag != "" {
				item.Tag = mapping.Tag
			}
		}
		key := resolvedItemKey{item.RecordID, item.Item, item.Tag}
		if index, ok := positions[key]; ok {
			result[index].Amount += item.Amount
		} else {
			positions[key] = len(result)
			result = append(result, item)
		}
	}
	return result
}

func resolveResearchItems(items []v3db.ResearchItem, mappings map[string]v3db.TemplateItemMapping) []v3db.ResearchItem {
	result := make([]v3db.ResearchItem, 0, len(items))
	positions := make(map[resolvedItemKey]int)
	for _, item := range items {
		if mapping, ok := mappings[item.Item]; ok {
			item.Item = mapping.ItemID
			if mapping.Tag != "" {
				item.Tag = mapping.Tag
			}
		}
		key := resolvedItemKey{item.RecordID, item.Item, item.Tag}
		if index, ok := positions[key]; ok {
			result[index].Amount += item.Amount
		} else {
			positions[key] = len(result)
			result = append(result, item)
		}
	}
	return result
}
