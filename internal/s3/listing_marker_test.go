package s3

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

func TestDelimitedListingSkipsPrefixesBehindMarker(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		for _, test := range []struct{ prefix, delimiter string }{
			{"", "/"}, {"é/", "/"}, {"照片/", "::"}, {"📁/", "🗂️"},
		} {
			t.Run(version+"/"+test.prefix+test.delimiter, func(t *testing.T) {
				env := newObjectTestEnv(t)
				group := test.prefix + "dir" + test.delimiter
				for _, key := range []string{group + "a", group + "z", test.prefix + "next.txt"} {
					env.mustPut(t, key, "body")
				}
				for _, marker := range []string{group, group + "m"} {
					for _, limit := range []int{0, 1, 2} {
						query := url.Values{"prefix": {test.prefix}, "delimiter": {test.delimiter}, "max-keys": {strconv.Itoa(limit)}}
						if version == "1" {
							query.Set("marker", marker)
						} else {
							query.Set("list-type", "2")
							query.Set("start-after", marker)
						}
						resp := env.do(t, http.MethodGet, "/"+env.bucket+"?"+query.Encode(), "", nil)
						if resp.Code != http.StatusOK {
							t.Fatalf("list status=%d body=%s", resp.Code, resp.Body.String())
						}
						var result listBucketResult
						if err := xml.Unmarshal(resp.Body.Bytes(), &result); err != nil {
							t.Fatal(err)
						}
						if len(result.CommonPrefixes) != 0 || result.IsTruncated || result.NextContinuationToken != "" {
							t.Errorf("marker=%q max-keys=%d: unexpected prefix/truncation: %s", marker, limit, resp.Body.String())
						}
						wantCount := min(limit, 1)
						if len(result.Contents) != wantCount {
							t.Errorf("marker=%q max-keys=%d: contents=%v, want count %d", marker, limit, result.Contents, wantCount)
						} else if wantCount == 1 && result.Contents[0].Key != test.prefix+"next.txt" {
							t.Errorf("contents=%v, want next.txt", result.Contents)
						}
					}
				}
			})
		}
	}
}

func TestDelimitedListingPaginatesAfterFilteredPrefix(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		for _, test := range []struct{ prefix, delimiter string }{
			{"", "/"}, {"é/", "/"}, {"照片/", "::"}, {"📁/", "🗂️"},
		} {
			t.Run(version+"/"+test.prefix+test.delimiter, func(t *testing.T) {
				env := newObjectTestEnv(t)
				group := test.prefix + "dir" + test.delimiter
				tail := test.prefix + "tail" + test.delimiter
				for _, key := range []string{group + "a", group + "z", test.prefix + "next.txt", tail + "a", tail + "z", test.prefix + "zz.txt"} {
					env.mustPut(t, key, "body")
				}
				query := url.Values{"prefix": {test.prefix}, "delimiter": {test.delimiter}, "max-keys": {"1"}}
				if version == "1" {
					query.Set("marker", group+"m")
				} else {
					query.Set("list-type", "2")
					query.Set("start-after", group+"m")
				}
				for page, want := range []string{test.prefix + "next.txt", tail, test.prefix + "zz.txt"} {
					resp := env.do(t, http.MethodGet, "/"+env.bucket+"?"+query.Encode(), "", nil)
					if resp.Code != http.StatusOK {
						t.Fatalf("page %d: status=%d body=%s", page, resp.Code, resp.Body.String())
					}
					var result struct {
						Contents              []listBucketObject `xml:"Contents"`
						CommonPrefixes        []listCommonPrefix `xml:"CommonPrefixes"`
						IsTruncated           bool               `xml:"IsTruncated"`
						NextMarker            string             `xml:"NextMarker"`
						NextContinuationToken string             `xml:"NextContinuationToken"`
						KeyCount              int                `xml:"KeyCount"`
					}
					if err := xml.Unmarshal(resp.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if page == 1 {
						if len(result.Contents) != 0 || len(result.CommonPrefixes) != 1 || result.CommonPrefixes[0].Prefix != want {
							t.Fatalf("page %d: want prefix %q: %s", page, want, resp.Body.String())
						}
					} else if len(result.CommonPrefixes) != 0 || len(result.Contents) != 1 || result.Contents[0].Key != want {
						t.Fatalf("page %d: want object %q: %s", page, want, resp.Body.String())
					}
					if result.IsTruncated != (page < 2) || version == "2" && result.KeyCount != 1 {
						t.Errorf("page %d: incorrect truncation/count: %s", page, resp.Body.String())
					}
					next := result.NextMarker
					if version == "2" {
						next = result.NextContinuationToken
					}
					if (next != "") != result.IsTruncated {
						t.Fatalf("page %d: continuation must exist exactly when truncated: %s", page, resp.Body.String())
					}
					if version == "1" {
						query.Set("marker", next)
					} else {
						query.Del("start-after")
						query.Set("continuation-token", next)
					}
				}
			})
		}
	}
}
