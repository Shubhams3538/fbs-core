package management_test

import (
	"net/http"
	"net/url"
	"testing"
)

func TestManagementDelimitedListingFiltersPrefixBehindCursor(t *testing.T) {
	env := newManagementTestEnv(t)
	for _, key := range []string{"listing/dir/a", "listing/dir/z", "listing/next.txt"} {
		seedStoredObject(t, env, "photos", key, "body")
	}
	query := url.Values{"prefix": {"listing/"}, "delimiter": {"/"}, "cursor": {"listing/dir/m"}, "limit": {"1"}}
	resp := env.do(t, http.MethodGet, "/api/management/buckets/photos/objects?"+query.Encode(), env.adminToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Objects []struct {
			Key string `json:"key"`
		} `json:"objects"`
		CommonPrefixes []string `json:"common_prefixes"`
		IsTruncated    bool     `json:"is_truncated"`
		NextCursor     string   `json:"next_cursor"`
	}
	decodeResponse(t, resp, &body)
	if len(body.CommonPrefixes) != 0 || len(body.Objects) != 1 || body.Objects[0].Key != "listing/next.txt" || body.IsTruncated || body.NextCursor != "" {
		t.Fatalf("listing must contain only listing/next.txt without truncation: %+v", body)
	}
}
