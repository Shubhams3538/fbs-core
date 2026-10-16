package metadata

import (
	"context"
	"slices"
	"testing"
)

func TestDelimitedListingFiltersGroupedKeysBeforeLimit(t *testing.T) {
	for _, test := range []struct{ prefix, delimiter string }{
		{"", "/"}, {"é/", "/"}, {"照片/", "::"}, {"📁/", "🗂️"},
	} {
		t.Run(test.prefix+test.delimiter, func(t *testing.T) {
			db := openTestDB(t)
			bucket := insertTestBucket(t, db)
			repo := NewObjectRepository(db)
			ctx := context.Background()
			group := test.prefix + "dir" + test.delimiter
			for _, key := range []string{group + "a", group + "z", test.prefix + "next.txt", test.prefix + "tail.txt", "unrelated"} {
				if err := repo.Create(ctx, newTestObject(bucket, key)); err != nil {
					t.Fatal(err)
				}
			}
			for _, marker := range []string{group, group + "m", group + "z"} {
				entries, err := repo.ListDelimited(ctx, bucket, test.prefix, marker, test.delimiter, 1)
				if err != nil {
					t.Fatal(err)
				}
				var keys []string
				for _, entry := range entries {
					keys = append(keys, entry.VirtualKey)
					if entry.IsPrefix || entry.Object == nil || entry.CursorKey != entry.VirtualKey {
						t.Errorf("marker %q: expected direct object, got %+v", marker, entry)
					}
				}
				want := []string{test.prefix + "next.txt", test.prefix + "tail.txt"}
				if !slices.Equal(keys, want) {
					t.Errorf("marker %q: keys=%q, want page and lookahead %q", marker, keys, want)
				}
			}
		})
	}
}
