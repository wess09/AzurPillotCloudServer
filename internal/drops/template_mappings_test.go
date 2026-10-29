package drops

import (
	"ap-cloud-server/internal/v3db"
	"testing"
)

func TestMappingCoalescesAliasesWithoutLosingAmountOrContextTag(t *testing.T) {
	aliases := map[string]v3db.TemplateItemMapping{"U-A": {ItemID: "Blueprint"}, "U-B": {ItemID: "Blueprint"}}
	input := []v3db.CommissionItem{{RecordID: "event", Item: "U-A", Tag: "bonus", Amount: 3}, {RecordID: "event", Item: "U-B", Tag: "bonus", Amount: 4}, {RecordID: "event", Item: "Blueprint", Tag: "bonus", Amount: 2}}
	result := resolveCommissionItems(input, aliases)
	if len(result) != 1 || result[0].Amount != 9 || result[0].Tag != "bonus" {
		t.Fatalf("bad resolution: %+v", result)
	}
	if input[0].Item != "U-A" {
		t.Fatal("raw identity was modified")
	}
	research := resolveResearchItems([]v3db.ResearchItem{{RecordID: "event", Item: "U-A", Tag: "bonus", Amount: 3}, {RecordID: "event", Item: "U-B", Tag: "normal", Amount: 4}}, aliases)
	if len(research) != 2 {
		t.Fatal("different context tags were merged")
	}
}
