// Package profile contains only operator-approved candidate facts, not inferred skills.
package profile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

const MaxDocumentBytes = 256 * 1024

type Fact struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type JobFamily struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}
type Technology struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}
type Capability struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	TechnologyIDs []string `json:"technology_ids,omitempty"`
	ProjectIDs    []string `json:"project_ids,omitempty"`
	RoleFamilyIDs []string `json:"role_family_ids,omitempty"`
}
type Project struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	Responsibilities []Fact   `json:"responsibilities,omitempty"`
	Outcomes         []Fact   `json:"outcomes,omitempty"`
	TechnologyIDs    []string `json:"technology_ids,omitempty"`
	CapabilityIDs    []string `json:"capability_ids,omitempty"`
	ResourceIDs      []string `json:"resource_ids,omitempty"`
	Domains          []string `json:"domains,omitempty"`
}
type Resource struct {
	ID                 string `json:"id"`
	Kind               string `json:"kind"`
	Label              string `json:"label"`
	URL                string `json:"url,omitempty"`
	AvailableOnRequest bool   `json:"available_on_request,omitempty"`
}
type Engagement struct {
	EmploymentTypes []string `json:"employment_types,omitempty"`
	ServiceWork     *bool    `json:"service_work,omitempty"`
	Remote          *bool    `json:"remote,omitempty"`
	Locations       []string `json:"locations,omitempty"`
	Availability    string   `json:"availability,omitempty"`
	Pricing         string   `json:"pricing,omitempty"`
}
type Signature struct {
	Name        string   `json:"name,omitempty"`
	Descriptor  string   `json:"descriptor,omitempty"`
	Phone       string   `json:"phone,omitempty"`
	Location    string   `json:"location,omitempty"`
	ResourceIDs []string `json:"resource_ids,omitempty"`
}
type Document struct {
	Identity          string       `json:"identity"`
	PrimaryStrength   string       `json:"primary_strength"`
	SecondaryPosition string       `json:"secondary_position"`
	DesiredRoles      []JobFamily  `json:"desired_roles"`
	Technologies      []Technology `json:"technologies,omitempty"`
	Capabilities      []Capability `json:"capabilities,omitempty"`
	Projects          []Project    `json:"projects,omitempty"`
	Domains           []Fact       `json:"domains,omitempty"`
	Specializations   []Fact       `json:"specializations,omitempty"`
	Resources         []Resource   `json:"resources,omitempty"`
	Engagement        *Engagement  `json:"engagement,omitempty"`
	Signature         Signature    `json:"signature"`
}

func Decode(raw []byte) (Document, error) {
	var d Document
	if len(raw) > MaxDocumentBytes {
		return d, fmt.Errorf("profile exceeds %d bytes", MaxDocumentBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return d, fmt.Errorf("profile must contain exactly one JSON document")
	}
	return d, d.Validate()
}

func (d Document) Validate() error {
	if d.Identity != "Full-Stack Software Developer" || d.PrimaryStrength != "Backend Engineering" || d.SecondaryPosition != "Full-Stack Development" {
		return fmt.Errorf("retain the approved full-stack identity and backend primary strength")
	}
	sets := map[string]map[string]bool{}
	global := map[string]bool{}
	add := func(kind, id, name string) error {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
			return fmt.Errorf("%s requires an ID and nonempty text/name", kind)
		}
		if strings.ContainsAny(id, " \t\r\n") || len(id) > 100 || global[id] {
			return fmt.Errorf("duplicate or invalid profile item ID %q", id)
		}
		global[id] = true
		if sets[kind] == nil {
			sets[kind] = map[string]bool{}
		}
		sets[kind][id] = true
		return nil
	}
	for _, v := range d.DesiredRoles {
		if err := add("role", v.ID, v.Name); err != nil {
			return err
		}
	}
	for _, v := range d.Technologies {
		if err := add("technology", v.ID, v.Name); err != nil {
			return err
		}
	}
	for _, v := range d.Capabilities {
		if err := add("capability", v.ID, v.Name); err != nil {
			return err
		}
	}
	for _, v := range d.Projects {
		if err := add("project", v.ID, v.Name); err != nil {
			return err
		}
		for _, f := range append(append([]Fact{}, v.Responsibilities...), v.Outcomes...) {
			if err := add("fact", f.ID, f.Text); err != nil {
				return err
			}
		}
	}
	for _, v := range append(append([]Fact{}, d.Domains...), d.Specializations...) {
		if err := add("fact", v.ID, v.Text); err != nil {
			return err
		}
	}
	for _, v := range d.Resources {
		if err := add("resource", v.ID, v.Label); err != nil {
			return err
		}
		if v.Kind != "cv" && v.Kind != "portfolio" && v.Kind != "project" && v.Kind != "article" {
			return fmt.Errorf("invalid resource kind %q", v.Kind)
		}
		if v.URL != "" {
			u, err := url.Parse(v.URL)
			if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
				return fmt.Errorf("resource %s needs a public HTTP(S) URL without credentials", v.ID)
			}
		} else if !v.AvailableOnRequest {
			return fmt.Errorf("resource %s needs a URL or explicit availability on request", v.ID)
		}
	}
	check := func(kind string, ids []string) error {
		for _, id := range ids {
			if !sets[kind][id] {
				return fmt.Errorf("unknown %s reference %q", kind, id)
			}
		}
		return nil
	}
	for _, v := range d.Capabilities {
		for kind, ids := range map[string][]string{"technology": v.TechnologyIDs, "project": v.ProjectIDs, "role": v.RoleFamilyIDs} {
			if err := check(kind, ids); err != nil {
				return err
			}
		}
	}
	for _, v := range d.Projects {
		for kind, ids := range map[string][]string{"technology": v.TechnologyIDs, "capability": v.CapabilityIDs, "resource": v.ResourceIDs} {
			if err := check(kind, ids); err != nil {
				return err
			}
		}
	}
	return check("resource", d.Signature.ResourceIDs)
}
