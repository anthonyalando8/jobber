package domainsearch

import "testing"

func TestIsNoiseHost(t *testing.T) {
	noise := []string{
		"facebook.com", "m.facebook.com", "www.linkedin.com", "en.wikipedia.org",
		"brightermonday.co.ke", "glassdoor.com",
	}
	for _, host := range noise {
		if !isNoiseHost(host) {
			t.Errorf("isNoiseHost(%q) = false, want true", host)
		}
	}

	real := []string{"safalgroup.com", "equitybank.co.ke", "kabarak.ac.ke", "example.co.ke"}
	for _, host := range real {
		if isNoiseHost(host) {
			t.Errorf("isNoiseHost(%q) = true, want false — a real company domain must not be filtered", host)
		}
	}
}

func TestSearcher_Enabled(t *testing.T) {
	s := NewSearcher(nil, "", "", 0)
	if s.Enabled() {
		t.Error("Enabled() = true with no API key/cx configured, want false")
	}

	s2 := NewSearcher(nil, "key", "cx", 0)
	if !s2.Enabled() {
		t.Error("Enabled() = false with both API key and cx set, want true")
	}
}
