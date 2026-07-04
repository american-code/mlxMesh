package httpmw

import "testing"

func TestValidateFetchURL(t *testing.T) {
	blocked := []string{
		"http://169.254.169.254/latest/meta-data/", // cloud metadata (SSRF classic)
		"http://127.0.0.1:9000/admin",              // loopback → coordinator/node internals
		"http://localhost/secret",                  // loopback by name
		"https://[::1]/",                           // IPv6 loopback
		"http://0.0.0.0/",                          // unspecified
		"ftp://example.com/x",                      // non-http scheme
		"file:///etc/passwd",                       // file scheme
		"http://169.254.10.5/",                     // link-local
	}
	for _, u := range blocked {
		if err := ValidateFetchURL(u); err == nil {
			t.Errorf("expected %q to be blocked, but it was allowed", u)
		}
	}

	// Allowed: public hosts and general private LAN (legit iOS pointer hosts).
	// Literal IPs so the test needs no DNS.
	allowed := []string{
		"https://1.1.1.1/payload/abc",   // public
		"http://192.168.1.135:8080/p/1", // LAN pointer host
		"http://10.0.0.9/p/2",           // LAN pointer host
		"http://172.16.4.4/p/3",         // LAN pointer host
	}
	for _, u := range allowed {
		if err := ValidateFetchURL(u); err != nil {
			t.Errorf("expected %q to be allowed, got: %v", u, err)
		}
	}
}
