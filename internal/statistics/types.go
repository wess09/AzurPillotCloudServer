package statistics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var itemRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

// Alas resource identifiers include names such as "Complete RepairPack".
var lootItemRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{0,127}$`)
var deviceRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
var instanceRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

type Metadata struct {
	UploadID       string  `json:"upload_id"`
	DeviceID       string  `json:"device_id"`
	InstanceID     string  `json:"instance_id"`
	Project        string  `json:"project"`
	Scene          string  `json:"scene"`
	Server         string  `json:"server"`
	CollectedAt    string  `json:"collected_at"`
	Sizes          []int64 `json:"file_sizes"`
	CombatCount    int64   `json:"combat_count,omitempty"`
	LegacyFilename string  `json:"legacy_filename,omitempty"`
	LegacyDeviceID string  `json:"legacy_device_id,omitempty"`
	Submitter      string  `json:"submitter,omitempty"`
}
type SourceFile struct {
	Index  int    `json:"index"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	FileID int64  `json:"file_id"`
}
type Source struct {
	SHA256   string       `json:"sha256"`
	Metadata Metadata     `json:"metadata"`
	Files    []SourceFile `json:"files"`
}
type Item struct {
	Item   string `json:"item"`
	Amount int64  `json:"amount"`
	Tag    string `json:"tag"`
}
type Event struct {
	RecordID    string           `json:"record_id,omitempty"`
	Comm        *string          `json:"comm,omitempty"`
	Name        string           `json:"name,omitempty"`
	Status      *int             `json:"status,omitempty"`
	Unresolved  []UnresolvedItem `json:"unresolved_items,omitempty"`
	Zone        string           `json:"zone,omitempty"`
	ZoneType    string           `json:"zone_type,omitempty"`
	ZoneID      int              `json:"zone_id,omitempty"`
	HazardLevel int              `json:"hazard_level,omitempty"`
	Type        string           `json:"event_type"`
	Series      int              `json:"series"`
	Project     string           `json:"project"`
	FocusSeries int              `json:"focus_series"`
	Duration    int64            `json:"duration_seconds"`
	Items       []Item           `json:"items"`
}

type UnresolvedItem struct {
	LocalKey    string `json:"local_key"`
	CandidateID string `json:"candidate_id,omitempty"`
	Tag         string `json:"tag"`
	Amount      int64  `json:"amount"`
}
type Result struct {
	SHA256        string   `json:"sha256"`
	Revision      int64    `json:"revision"`
	Status        string   `json:"status"`
	Parser        string   `json:"parser_version"`
	Release       int64    `json:"template_release"`
	Source        Source   `json:"source"`
	Events        []Event  `json:"events"`
	Diagnostics   []string `json:"diagnostics"`
	Candidates    []string `json:"candidate_ids"`
	UsedTemplates []string `json:"used_templates"`
}
type Receipt struct {
	SHA256   string `json:"sha256"`
	Revision int64  `json:"revision"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}
type Candidate struct {
	ID         string         `json:"id"`
	Scene      string         `json:"scene"`
	Image      string         `json:"image"`
	Sample     string         `json:"sample_sha256"`
	Occurrence string         `json:"occurrence"`
	Metadata   map[string]any `json:"metadata"`
}
type Template struct {
	IconSHA256 string `json:"icon_sha256,omitempty"`
	Kind       string `json:"kind,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	ID         string `json:"id"`
	ItemID     string `json:"item_id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Rarity     int    `json:"rarity"`
	Series     int    `json:"series"`
	Scene      string `json:"scene"`
	Enabled    bool   `json:"enabled"`
	Image      string `json:"image"`
}

func (r Result) validate() error {
	return r.validateLimits(10<<20, 500<<20)
}
func (r Result) validateLimits(fileBytes, groupBytes int64) error {
	if !hashRE.MatchString(r.SHA256) || r.Source.SHA256 != r.SHA256 || r.Revision < 1 || r.Release < 0 || r.Parser == "" || len(r.Parser) > 100 {
		return fmt.Errorf("invalid result identity/version")
	}
	m := r.Source.Metadata
	if m.CombatCount < 0 || m.CombatCount > 1e9 || len(m.LegacyFilename) > 256 || len(m.LegacyDeviceID) > 64 || len(m.Submitter) > 128 {
		return fmt.Errorf("invalid legacy source metadata")
	}
	if !deviceRE.MatchString(m.DeviceID) || !instanceRE.MatchString(m.InstanceID) || (m.Project != "research" && m.Project != "drop") || len(m.Scene) > 128 {
		return fmt.Errorf("invalid source metadata")
	}
	switch m.Server {
	case "cn", "cn_ios", "cn_channel", "en", "jp", "tw":
	default:
		return fmt.Errorf("invalid source server")
	}
	if _, e := time.Parse(time.RFC3339Nano, m.CollectedAt); e != nil {
		return fmt.Errorf("invalid collected_at")
	}
	if len(r.Source.Files) == 0 || len(r.Source.Files) != len(m.Sizes) || len(m.Sizes) > 1000 {
		return fmt.Errorf("invalid source file count")
	}
	h := sha256.New()
	var total int64
	for i, f := range r.Source.Files {
		if f.Index != i || !hashRE.MatchString(f.SHA256) || f.Size != m.Sizes[i] || f.Size < 1 || f.Size > fileBytes || f.FileID <= 0 {
			return fmt.Errorf("invalid source file")
		}
		if f.Size > groupBytes-total {
			return fmt.Errorf("source size exceeds limit")
		}
		total += f.Size
		b, _ := hex.DecodeString(f.SHA256)
		h.Write(b)
	}
	if total > groupBytes || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return fmt.Errorf("source hash/size mismatch")
	}
	if r.Status != "complete" && r.Status != "needs_review" && r.Status != "invalid" {
		return fmt.Errorf("invalid result status")
	}
	if len(r.Events) > 1000 || len(r.Diagnostics) > 1000 || len(r.Candidates) > 1000 || len(r.UsedTemplates) > 2000 {
		return fmt.Errorf("too many result entries")
	}
	if r.Status == "complete" && (len(r.Events) == 0 || len(r.Diagnostics) > 0 || len(r.Candidates) > 0) {
		return fmt.Errorf("complete results require events and no unresolved diagnostics")
	}
	records := map[string]bool{}
	for _, e := range r.Events {
		if e.RecordID != "" {
			if records[e.RecordID] {
				return fmt.Errorf("duplicate event record_id")
			}
			records[e.RecordID] = true
		}
		if e.RecordID != "" && !hashRE.MatchString(e.RecordID) || e.Comm != nil && (len(*e.Comm) == 0 || len(*e.Comm) > 128) || len(e.Name) > 128 || e.Status != nil && (*e.Status < 0 || *e.Status > 1) {
			return fmt.Errorf("invalid commission identity/status")
		}
		if r.Parser == "azurpilot-adapter-v4" && strings.HasPrefix(m.Scene, "commission") && (e.Status == nil || e.Name == "" || e.Name != e.Project || e.RecordID == "") {
			return fmt.Errorf("commission v4 requires record_id, name and status")
		}
		if len(e.Unresolved)+len(e.Items) > 200 || r.Status == "complete" && len(e.Unresolved) > 0 {
			return fmt.Errorf("invalid unresolved item count/status")
		}
		for _, item := range e.Unresolved {
			if !hashRE.MatchString(item.LocalKey) || item.CandidateID != "" && !hashRE.MatchString(item.CandidateID) || item.Amount < 1 || item.Amount > 1e9 || !validRewardTag(item.Tag) {
				return fmt.Errorf("invalid unresolved item")
			}
		}
		if e.ZoneID < 0 || e.ZoneID > 10000 || e.HazardLevel < 0 || e.HazardLevel > 6 || len(e.Zone) > 128 || len(e.ZoneType) > 32 {
			return fmt.Errorf("invalid zone metadata")
		}
		if e.Duration < 0 || e.Duration > 604800 || e.Series < 0 || e.Series > 100 || e.FocusSeries < 0 || e.FocusSeries > 100 || len(e.Items) > 200 {
			return fmt.Errorf("invalid event")
		}
		if m.Project == "research" {
			if (e.Type != "refresh" && e.Type != "complete") || e.Series < 1 || !itemRE.MatchString(e.Project) {
				return fmt.Errorf("invalid research event")
			}
			if e.Type == "refresh" && len(e.Items) != 0 {
				return fmt.Errorf("refresh cannot have rewards")
			}
		} else if e.Type != "drop" {
			return fmt.Errorf("invalid drop event")
		}
		seen := map[string]bool{}
		for _, item := range e.Items {
			k := item.Item + "/" + item.Tag
			if r.Status == "complete" && clusterRE.MatchString(item.Item) {
				return fmt.Errorf("unmapped cluster cannot be a complete reward")
			}
			if !lootItemRE.MatchString(item.Item) || item.Amount < 1 || item.Amount > 1e9 || seen[k] {
				return fmt.Errorf("invalid/duplicate item")
			}
			switch item.Tag {
			case "", "bonus", "event", "catchup", "meow", "log", "scan":
			default:
				return fmt.Errorf("invalid reward tag")
			}
			seen[k] = true
		}
	}
	return nil
}

func validRewardTag(tag string) bool {
	switch tag {
	case "", "bonus", "event", "catchup", "meow", "log", "scan":
		return true
	}
	return false
}
