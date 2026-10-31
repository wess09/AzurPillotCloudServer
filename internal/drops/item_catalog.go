package drops

import (
	"ap-cloud-server/internal/v3db"
	"context"
	"sort"
)

// CatalogItems overlays published definitions on the pinned game catalog.
func (s *Service) CatalogItems(ctx context.Context) ([]analysisItem, error) {
	var definitions []v3db.TemplateItemDefinition
	if err := s.db.WithContext(ctx).Find(&definitions).Error; err != nil {
		return nil, err
	}
	items := map[string]analysisItem{}
	for _, item := range catalog.Items {
		items[item.ID] = item
	}
	for _, item := range definitions {
		prior := items[item.ID]
		items[item.ID] = analysisItem{ID: item.ID, Name: item.Name, Category: item.Category, Unit: item.Unit, Aliases: prior.Aliases, Scenes: item.Scenes, Rarity: item.Rarity, Series: item.Series, Version: item.Version, GameID: item.GameID, Faction: item.Faction, Subtype: item.Subtype}
	}
	result := make([]analysisItem, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
