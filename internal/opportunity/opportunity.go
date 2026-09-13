// Package opportunity represents source facts. It deliberately does not classify candidate fit.
package opportunity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxDescriptionBytes = 64 * 1024
const MaxMetadataBytes = 64 * 1024

type DiscoveredOpportunity struct {
	CompanyName       string            `json:"company_name"`
	CompanyID         int64             `json:"company_id,omitempty"` // Explicit legacy/manual association only.
	DomainCandidate   string            `json:"domain_candidate,omitempty"`
	Source            string            `json:"source"`
	ExternalID        string            `json:"external_id,omitempty"`
	URL               string            `json:"url,omitempty"`
	CanonicalURL      string            `json:"canonical_url,omitempty"`
	Title             string            `json:"title"`
	NormalizedTitle   string            `json:"normalized_title"`
	Description       string            `json:"description"`
	Location          string            `json:"location,omitempty"`
	EmploymentType    string            `json:"employment_type,omitempty"`
	RemoteStatus      string            `json:"remote_status"`
	PublishedAt       *time.Time        `json:"published_at,omitempty"`
	ExpiresAt         *time.Time        `json:"expires_at,omitempty"`
	Availability      string            `json:"availability"`
	DescriptionStatus string            `json:"description_status"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

func BoundUTF8(s string, n int) (string, bool) {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s, false
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}
func NormalizeTitle(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	s = strings.NewReplacer("full-stack", "full stack", "fullstack", "full stack", "full–stack", "full stack", "full—stack", "full stack").Replace(s)
	return s
}
func CanonicalURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || k == "gclid" || k == "fbclid" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func Normalize(d DiscoveredOpportunity) (DiscoveredOpportunity, error) {
	d.Source = strings.TrimSpace(d.Source)
	d.CompanyName = strings.TrimSpace(d.CompanyName)
	d.ExternalID = strings.TrimSpace(d.ExternalID)
	if d.Source == "" || len(d.Source) > 80 {
		return d, fmt.Errorf("source required (maximum 80 characters)")
	}
	if len(d.ExternalID) > 512 || len(d.URL) > 4096 || len(d.CompanyName) > 512 || len(d.Title) > 4096 {
		return d, fmt.Errorf("source identity/title exceeds storage limits")
	}
	d.CanonicalURL = CanonicalURL(d.URL)
	d.NormalizedTitle = NormalizeTitle(d.Title)
	var cut bool
	d.Description, cut = BoundUTF8(d.Description, MaxDescriptionBytes)
	if cut {
		d.DescriptionStatus = "partial"
	}
	if d.Description == "" {
		d.DescriptionStatus = "unavailable"
	} else if d.DescriptionStatus == "" {
		d.DescriptionStatus = "partial"
	}
	if d.DescriptionStatus != "complete" && d.DescriptionStatus != "partial" && d.DescriptionStatus != "unavailable" {
		return d, fmt.Errorf("invalid description status")
	}
	if d.RemoteStatus == "" {
		d.RemoteStatus = "unknown"
	}
	if d.Availability == "" {
		d.Availability = "unknown"
	}
	if !oneOf(d.RemoteStatus, "remote", "hybrid", "onsite", "unknown") || !oneOf(d.Availability, "open", "closed", "unknown") {
		return d, fmt.Errorf("invalid opportunity state")
	}
	meta, _ := json.Marshal(d.Metadata)
	if len(meta) > MaxMetadataBytes {
		return d, fmt.Errorf("source metadata exceeds storage limit")
	}
	return d, nil
}
func oneOf(s string, options ...string) bool {
	for _, v := range options {
		if s == v {
			return true
		}
	}
	return false
}
func (d DiscoveredOpportunity) Identity() string {
	if d.ExternalID != "" {
		return "id:" + d.ExternalID
	}
	if d.CanonicalURL != "" {
		return "url:" + d.CanonicalURL
	}
	return ""
}
func (d DiscoveredOpportunity) Hash() string {
	// Tracking URL changes and identity enrichment are observations, not job-content revisions.
	d.URL = ""
	d.ExternalID = ""
	d.CompanyName = ""
	d.CompanyID = 0
	d.DomainCandidate = ""
	// Which search query rediscovered a listing does not revise the vacancy.
	metadata := map[string]string{}
	for key, value := range d.Metadata {
		if key != "listing_url" {
			metadata[key] = value
		}
	}
	d.Metadata = metadata
	b, _ := json.Marshal(d)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
